package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/tunnelprobe"
	"github.com/shogun-kub/termit/internal/uv/warp"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// warpProbeStep is the last thing an install does when WARP is meant to carry traffic: it asks the
// tunnel where the internet sees it from. Registration can succeed and the tunnel still be dead — the
// API answers over HTTPS while the WireGuard endpoint is filtered — and a node that silently exits
// directly is exactly the failure this project exists to avoid.
//
// The answer is a warning, never a failure: a cloud that cannot be reached from this network is a
// reason for the operator to know, not a reason to leave the node half-installed.
type warpProbeStep struct{ e *stepEnv }

func (*warpProbeStep) Name() string { return "warp-egress" }

// Check is offline and says only whether there is anything to verify.
func (s *warpProbeStep) Check(ctx context.Context) (CheckResult, error) {
	if !s.e.cfg.Warp.UsesWarp() {
		return CheckResult{Satisfied: true, Detail: "off"}, nil
	}
	if s.e.cfg.Warp.UsesWireGuard() {
		if _, err := warp.LoadIdentity(s.e.cfg.Paths.WarpIdentityPath()); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return CheckResult{Satisfied: true, Detail: "not registered, so there is no tunnel to verify"}, nil
			}
			return CheckResult{Satisfied: true, Detail: "the identity cannot be read: " + err.Error()}, nil
		}
	}
	// The probe runs the core as a temporary client against the running one; without the service there
	// is nothing to talk to yet.
	if res, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", "termit-xray"}}); err != nil {
		return CheckResult{Satisfied: true, Detail: "the core is not running: " + firstLine(res.Stderr)}, nil
	}
	return CheckResult{Detail: "verify the exit through the tunnel"}, nil
}

// Run fetches Cloudflare's trace through the node's own tunnel and reads where it came from.
func (s *warpProbeStep) Run(ctx context.Context) error {
	// The MASQUE relay was enabled by the previous step; probing before it listens sends the request
	// through the balancer's direct fallback and reports the node's own address, which reads like a
	// broken tunnel rather than a race. Wait for it, and if it never comes up, say exactly that.
	if s.e.cfg.Warp.UsesUsque() && !listeningFor(ctx, xray.DefaultUsqueSocks, s.e.readyWait) {
		return &Warning{Reason: "the MASQUE sidecar is not listening yet, so the egress through it was not " +
			"verified; `termit-uv warp status` says when it comes up"}
	}
	st, err := state.LoadInstalled(s.e.cfg.Paths.StatePath())
	if err != nil {
		return &Warning{Reason: "the tunnel was not verified: " + err.Error()}
	}
	trace, err := s.e.probeWarp(ctx, s.e.cfg, st)
	if err != nil {
		return &Warning{Reason: "WARP is configured but the exit could not be verified: " + err.Error() +
			"; check `termit-uv warp status`"}
	}
	if !trace.ThroughWarp() {
		return &Warning{Reason: fmt.Sprintf("WARP is configured but the tunnel exits at %s instead of Cloudflare; "+
			"proxied traffic is going out directly", trace.IP)}
	}
	s.e.log.Info("the tunnel exits through WARP", "ip", trace.IP)
	return nil
}

// listeningFor reports whether addr accepts a TCP connection within wait; zero means one attempt.
func listeningFor(ctx context.Context, addr string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		conn, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// probeWarpEgress is the production probe: the node's own core, started as a temporary client, fetches
// Cloudflare's trace through the tunnel the way a phone would.
func probeWarpEgress(ctx context.Context, cfg *config.Config, st *state.State) (warp.Trace, error) {
	if st == nil || st.Identity.SelfUUID == "" || st.Identity.XHTTPPath == "" {
		return warp.Trace{}, errors.New("the node has no identity to probe with")
	}
	body, err := tunnelprobe.Fetch(ctx, tunnelprobe.Options{
		XrayBinary: cfg.Paths.XrayBinary(),
		ProxyAddr:  net.JoinHostPort(xray.DefaultListen, strconv.Itoa(xray.DefaultPort)),
		Path:       "/" + st.Identity.XHTTPPath + "/",
		UUID:       st.Identity.SelfUUID,
		TargetURL:  xray.WarpProbeURL,
	})
	if err != nil {
		return warp.Trace{}, err
	}
	return warp.ParseTrace(body), nil
}
