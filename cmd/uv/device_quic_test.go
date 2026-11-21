package main

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
)

// A local device is reachable over both protocols the node serves, and the CLI has to hand out both:
// before this, the QUIC link existed only on the owner's page, so a device added from a terminal came
// with one way in and no hint that there was another.
func TestLocalLinksCarryTheQUICTransport(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := deviceFixture(t)
	cfg.Hysteria = &config.Hysteria{}
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if code := app.run([]string{"device", "add", "--label", "tablet", "--config", configPath}); code != exitOK {
		t.Fatalf("device add: exit = %d; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	added := app.stdout.(*strings.Builder).String()
	if !strings.Contains(added, "vless://") || !strings.Contains(added, "hy2://") {
		t.Errorf("device add printed one way in, want both: %q", added)
	}
	if !strings.Contains(added, "tablet%20(QUIC)") {
		t.Errorf("the QUIC link is not labelled for the device: %q", added)
	}

	// The node's own links answer the same way.
	app.stdout.(*strings.Builder).Reset()
	if code := app.run([]string{"uri", "--config", configPath}); code != exitOK {
		t.Fatalf("uri: exit = %d; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	own := app.stdout.(*strings.Builder).String()
	if !strings.Contains(own, "vless://") || !strings.Contains(own, "hy2://") {
		t.Errorf("uri printed one way in, want both: %q", own)
	}

	// Turning the transport off takes the second link out of both answers, which is the other half of
	// the promise: a node that no longer serves it must stop handing it out.
	cfg.Hysteria = nil
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}
	app.stdout.(*strings.Builder).Reset()
	if code := app.run([]string{"device", "list", "--config", configPath}); code != exitOK {
		t.Fatalf("device list: exit = %d", code)
	}
	if listed := app.stdout.(*strings.Builder).String(); strings.Contains(listed, "hy2://") {
		t.Errorf("the transport is off but device list still offers it: %q", listed)
	}
}
