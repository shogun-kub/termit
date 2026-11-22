package main

import (
	"encoding/json"
	"strings"
	"testing"
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
