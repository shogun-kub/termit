package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
)

// The proxy transport cannot carry UDP at all: warp-cli's SOCKS5 listener has no UDP association, so the
// generator sends UDP direct and the address WARP exists to hide is not hidden for it. §10.1's acceptance
// allows that outcome, but only if the node says so instead of reporting a plain `warp=on`.
func TestWarpStatusSaysTheProxyTransportDoesNotCarryUDP(t *testing.T) {
	t.Parallel()
	app, _, configPath := optionsFixture(t, "")

	if code := app.run([]string{"warp", "status", "--config", configPath}); code != exitOK {
		t.Fatalf("warp status: exit %d, stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	text := app.stdout.(*strings.Builder).String()
	if !strings.Contains(text, "udp:") || !strings.Contains(text, "not carried") {
		t.Errorf("warp status does not name the UDP gap:\n%s", text)
	}

	// The machine-readable form carries it too, because that is what a check reads.
	app.stdout = &strings.Builder{}
	if code := app.run([]string{"warp", "status", "--json", "--config", configPath}); code != exitOK {
		t.Fatalf("warp status --json: exit %d", code)
	}
	var report struct {
		UDP string `json:"udp"`
	}
	if err := json.Unmarshal([]byte(app.stdout.(*strings.Builder).String()), &report); err != nil {
		t.Fatalf("the report is not JSON: %v", err)
	}
	if report.UDP != "direct" {
		t.Errorf("udp = %q, want direct for the proxy transport", report.UDP)
	}
}

// The MASQUE sidecar carries UDP, so the report must not keep telling the owner that UDP escapes: it says
// so only when the tunnel is actually up, and the mode it names is the sidecar's.
func TestWarpStatusNamesTheSidecarAndItsUDP(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	cfg.Warp.Transport = config.WarpTransportUsque
	cfg.Warp.Mode = config.WarpAll
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if code := app.run([]string{"warp", "status", "--json", "--config", configPath}); code != exitOK {
		t.Fatalf("warp status --json: exit %d, stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	var report struct {
		Transport string `json:"transport"`
		State     string `json:"state"`
		UDP       string `json:"udp"`
	}
	if err := json.Unmarshal([]byte(app.stdout.(*strings.Builder).String()), &report); err != nil {
		t.Fatalf("the report is not JSON: %v", err)
	}
	if report.Transport != config.WarpTransportUsque {
		t.Errorf("transport = %q, want usque", report.Transport)
	}
	if report.UDP != "direct" {
		t.Errorf("udp = %q, want direct while the sidecar does not answer", report.UDP)
	}

	app.stdout = &strings.Builder{}
	if code := app.run([]string{"warp", "status", "--config", configPath}); code != exitOK {
		t.Fatalf("warp status: exit %d", code)
	}
	if text := app.stdout.(*strings.Builder).String(); !strings.Contains(text, "udp:") || !strings.Contains(text, "WARP is not on") {
		t.Errorf("the text report does not explain UDP:\n%s", text)
	}
}
