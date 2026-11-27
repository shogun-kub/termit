package install

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/tunnelprobe"
	"github.com/shogun-kub/termit/internal/uv/warp"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// warpSlow is where a tunnel that says it is on stops being usable: the trace through it takes seconds
// while the same request from the node takes tens of milliseconds. Production set the number — a
// wireguard egress answered "on" with 7–12 s handshakes for ten minutes, and every client felt it while
// every status line was green.
const warpSlow = 2 * time.Second

// diagnoseWarp is the one check that leaves the host: it fetches Cloudflare's trace through the
// tunnel, which is the only way to know whether the egress is really WARP and not merely a listener
// that accepts bytes.
//
// A host that cannot run WARP is a skip, not a failure: the node works, it just exits directly. A
// host that should run it and does not is a failure, because then the address the owner wanted to
// hide is the node's own.
func diagnoseWarp(ctx context.Context, cfg *config.Config, st *state.State, client warp.Client) Check {
	check := Check{Name: "warp"}
	if !cfg.Warp.UsesWarp() {
		check.Status = StatusSkip
		check.Detail = "off: proxied traffic exits directly"
		return check
	}
	if cfg.Warp.UsesWireGuard() {
		return diagnoseWireGuardWarp(ctx, cfg, st)
	}
	if cfg.Warp.UsesUsque() {
		return diagnoseUsqueWarp(ctx)
	}
	if err := client.Host.Check(); err != nil {
		check.Status = StatusSkip
		check.Detail = err.Error()
		check.Hint = "the node exits directly; this is expected in a container without /dev/net/tun"
		return check
	}
	status, err := client.Status(ctx)
	switch {
	case err != nil:
		check.Status = StatusFail
		check.Detail = errorText(err)
	case status.State == "missing":
		check.Status = StatusFail
		check.Detail = "cloudflare-warp is not installed"
		check.Hint = "run `termit-uv init --only warp` to install and register it"
	case status.Healthy():
		check.Status = StatusOK
		check.Detail = status.Detail
	default:
		check.Status = StatusFail
		check.Detail = status.Detail
		if cfg.Warp.FailOpenEnabled() {
			check.Hint = "fail-open is on, so proxied traffic exits directly until the tunnel is back"
		} else {
			check.Hint = "fail-open is off, so proxied traffic is failing until the tunnel is back"
		}
	}
	return check
}

// diagnoseWireGuardWarp checks the transport that lives inside the core. There is no sidecar to ask, so
// the evidence is the one that matters: the address the internet sees for a request that went through
// the node's own tunnel. It is the same fetch a client would make, which is why it can fail while the
// config looks perfect.
func diagnoseWireGuardWarp(ctx context.Context, cfg *config.Config, st *state.State) Check {
	check := Check{Name: "warp"}
	if _, err := warp.LoadIdentity(cfg.Paths.WarpIdentityPath()); err != nil {
		// Not a failure: a node that could not register is a working node with direct egress, which is
		// what the installer reports as a warning too. It is worth saying loudly, though, because the
		// address the owner wanted to hide is the node's own until registration succeeds.
		check.Status = StatusWarn
		check.Detail = "WARP is on but this node is not registered: " + errorText(err)
		check.Hint = "run `termit-uv init --only warp` when Cloudflare is reachable; until then proxied traffic exits directly"
		return check
	}
	if st == nil || st.Identity.SelfUUID == "" || st.Identity.XHTTPPath == "" {
		check.Status = StatusSkip
		check.Detail = "the node has no identity to probe with yet"
		return check
	}
	started := time.Now()
	body, err := tunnelprobe.Fetch(ctx, tunnelprobe.Options{
		XrayBinary: cfg.Paths.XrayBinary(),
		ProxyAddr:  net.JoinHostPort(xray.DefaultListen, strconv.Itoa(xray.DefaultPort)),
		Path:       "/" + st.Identity.XHTTPPath + "/",
		UUID:       st.Identity.SelfUUID,
		TargetURL:  WarpProbeURLForDiagnosis(cfg.Warp.FamilyOrDefault()),
	})
	took := time.Since(started)
	if err != nil {
		check.Status = StatusSkip
		check.Detail = errorText(err)
		check.Hint = "the probe runs the core as a temporary client; a failure here says nothing about the tunnel itself"
		return check
	}
	trace := warp.ParseTrace(body)
	if trace.ThroughWarp() {
		return warpEgressCheck("wireguard", trace, took)
	}
	check.Status = StatusFail
	check.Detail = "the tunnel exits at " + trace.IP + " without WARP"
	check.Hint = "the core's WireGuard outbound is not being used for this egress; check `termit-uv warp status`"
	return check
}

// diagnoseUsqueWarp checks the MASQUE sidecar. It has no status command, so the evidence is the one the
// node's traffic produces: a trace fetched through its loopback SOCKS5 listener. A listener that is not
// there is a failure, not a skip — this transport exists precisely to carry the traffic, and its absence
// puts the node's own address on the wire.
func diagnoseUsqueWarp(ctx context.Context) Check {
	check := Check{Name: "warp"}
	started := time.Now()
	trace, err := warp.Client{Socks: xray.DefaultUsqueSocks}.Trace(ctx)
	took := time.Since(started)
	if err != nil {
		check.Status = StatusFail
		check.Detail = "the MASQUE sidecar is not answering on " + xray.DefaultUsqueSocks + ": " + errorText(err)
		check.Hint = "systemctl status termit-usque.service, then `termit-uv init --only warp`"
		return check
	}
	if trace.ThroughWarp() {
		return warpEgressCheck("usque (MASQUE)", trace, took)
	}
	check.Status = StatusFail
	check.Detail = "the sidecar exits at " + trace.IP + " without WARP"
	check.Hint = "the sidecar is up but the tunnel is not; check `termit-uv warp status`"
	return check
}

// warpEgressCheck is the check both transports share once the trace says WARP carried the request: the
// address Cloudflare saw, and how long the request took. A tunnel that answers in seconds is reported as
// a warning, because "on" and "usable" stopped being the same statement the day this was measured.
func warpEgressCheck(prefix string, trace warp.Trace, took time.Duration) Check {
	check := Check{Name: "warp", Status: StatusOK,
		Detail: fmt.Sprintf("%s, via %s in %s", prefix, trace.IP, took.Round(time.Millisecond))}
	if took > warpSlow {
		check.Status = StatusWarn
		check.Detail = fmt.Sprintf("%s, via %s, but the request took %s", prefix, trace.IP, took.Round(time.Millisecond))
		check.Hint = "the tunnel is up and too slow to use; check the node's uplink and `termit-uv warp status`"
	}
	return check
}

// WarpProbeURLForDiagnosis is the URL the probe fetches. It is a variable so a test can point the probe
// at a local server instead of Cloudflare.
var WarpProbeURLForDiagnosis = func(family string) string { return xray.WarpProbeURLFor(family) }
