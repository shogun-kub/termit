package agent

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// certFile is the leaf chain nginx reads and Xray never does.
const certFile = "fullchain.pem"

// certWarnDays is when an expiring certificate stops being a detail and starts being a warning.
// The renewal timer renews 30 days out; 14 gives it two weeks of visible failure before an outage.
const certWarnDays = 14

// SelfCheckOptions wire the local half of a node's health. The function seams exist so tests do not
// need a host with systemd and a listening core.
type SelfCheckOptions struct {
	Config *config.Config
	State  *state.State
	Now    func() time.Time
	// Active reports whether a systemd unit is running. Nil makes the service check unknown.
	Active func(ctx context.Context, unit string) (bool, error)
	// Dial checks that something accepts a TCP connection on addr. Nil makes port checks unknown.
	Dial func(ctx context.Context, addr string) error
}

// RunSelfCheck answers "is this node broken on its own terms": the core is running and listening,
// the web tier is up, the certificate is valid, and WARP is in the state the configuration claims.
// A check that cannot be performed is unknown, never a failure — the same rule the server applies.
func RunSelfCheck(ctx context.Context, opts SelfCheckOptions) state.SelfCheckReport {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	started := now()
	report := state.SelfCheckReport{At: started.UTC()}

	report.Items = append(report.Items, checkUnit(ctx, opts.Active, service.XrayUnitName, "xray"))
	report.Items = append(report.Items, checkPort(ctx, opts.Dial, "proxy", net.JoinHostPort(xray.DefaultListen, strconv.Itoa(xray.DefaultPort))))
	report.Items = append(report.Items, checkPort(ctx, opts.Dial, "api", net.JoinHostPort(xray.DefaultListen, strconv.Itoa(xray.DefaultAPIPort))))
	// nginx owns :443 and is not managed by the core. A node whose core is fine but whose web tier is
	// down is still unreachable, and that difference is exactly what the server cannot see.
	report.Items = append(report.Items, checkPort(ctx, opts.Dial, "web", "127.0.0.1:443"))
	report.Items = append(report.Items, checkCertificate(opts.Config, now()))
	report.Items = append(report.Items, checkWarp(opts.Config))

	report.OK = true
	for _, item := range report.Items {
		if !item.OK && !item.Unknown {
			report.OK = false
		}
	}
	report.LatencyMS = int(now().Sub(started) / time.Millisecond)
	return report
}

func checkUnit(ctx context.Context, active func(context.Context, string) (bool, error), unit, name string) state.SelfCheckItem {
	item := state.SelfCheckItem{Name: name}
	if active == nil {
		item.Unknown = true
		item.Detail = "systemctl is not available"
		return item
	}
	running, err := active(ctx, unit)
	switch {
	case err != nil:
		item.Unknown = true
		item.Detail = "could not ask systemd: " + err.Error()
	case running:
		item.OK = true
	default:
		item.Detail = unit + " is not running"
	}
	return item
}

func checkPort(ctx context.Context, dial func(context.Context, string) error, name, addr string) state.SelfCheckItem {
	item := state.SelfCheckItem{Name: name}
	if dial == nil {
		item.Unknown = true
		item.Detail = "no dialer"
		return item
	}
	if err := dial(ctx, addr); err != nil {
		item.Detail = fmt.Sprintf("%s is not accepting connections: %v", addr, err)
		return item
	}
	item.OK = true
	item.Detail = addr
	return item
}

// checkCertificate reads the file nginx serves rather than the expiry recorded in the state: a
// renewal that wrote nothing must not look like a renewal that worked.
func checkCertificate(cfg *config.Config, now time.Time) state.SelfCheckItem {
	item := state.SelfCheckItem{Name: "certificate"}
	if cfg == nil {
		item.Unknown = true
		item.Detail = "no configuration"
		return item
	}
	path := filepath.Join(cfg.Paths.CertDir, certFile)
	data, err := os.ReadFile(path) // #nosec G304 -- a path from our own configuration.
	if err != nil {
		item.Detail = "cannot read " + path + ": " + err.Error()
		return item
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		item.Detail = path + " holds no PEM certificate"
		return item
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		item.Detail = "cannot parse " + path + ": " + err.Error()
		return item
	}
	left := cert.NotAfter.Sub(now)
	days := int(left.Hours() / 24)
	switch {
	case left <= 0:
		item.Detail = fmt.Sprintf("expired %d day(s) ago", -days)
	case days < certWarnDays:
		// The server's own certificate check fails below this window, so the local check says the
		// same thing: a node that cannot pass its next probe is locally broken, not a path problem.
		item.Detail = fmt.Sprintf("expires in %d day(s)", days)
	default:
		item.OK = true
		item.Detail = fmt.Sprintf("expires in %d day(s)", days)
	}
	return item
}

// checkWarp reports the egress mode the configuration asks for. WARP itself arrives in M4.1; until
// then a configuration that enables it is reported as unknown instead of being silently ignored.
func checkWarp(cfg *config.Config) state.SelfCheckItem {
	item := state.SelfCheckItem{Name: "warp"}
	if cfg == nil || !cfg.Warp.Enabled {
		item.OK = true
		item.Detail = "off"
		return item
	}
	item.Unknown = true
	item.Detail = "warp is configured but this build does not manage it"
	return item
}
