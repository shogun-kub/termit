package main

import (
	"bytes"
	"slices"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// The transport is switched on without editing YAML: the flag adds the block and publishes the port it
// listens on. The port is the part an operator forgets, and an unpublished UDP port is counted as scanning
// and then banned — so the switch satisfies that rule itself instead of failing the run on it.
func TestEnableTransportAddsTheBlockAndItsPort(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "node.example.com"
	// A fixture has no WARP identity, so the proxy transport stands in for an upgraded node.
	cfg.Warp.Transport = config.WarpTransportProxy
	cfg.Security.Firewall.UDPPorts = []int{8443, 443}

	enableTransport(cfg, true)
	if cfg.Hysteria == nil {
		t.Fatal("the transport was not enabled")
	}
	if !slices.Contains(cfg.Security.Firewall.UDPPorts, proto.HysteriaPort) {
		t.Errorf("udp_ports = %v, want the transport's port in it", cfg.Security.Firewall.UDPPorts)
	}

	// Converging is not accumulating: a second run leaves one copy of the port, in order.
	before := slices.Clone(cfg.Security.Firewall.UDPPorts)
	enableTransport(cfg, true)
	if !slices.Equal(before, cfg.Security.Firewall.UDPPorts) {
		t.Errorf("a second run changed the ports: %v then %v", before, cfg.Security.Firewall.UDPPorts)
	}
	if cfg.Validate() != nil {
		t.Errorf("the configuration the switch wrote does not validate: %v", cfg.Validate())
	}

	// Off removes the block and leaves the port: it is the operator's line, and a node that stopped
	// serving the transport may still publish it for something else.
	enableTransport(cfg, false)
	if cfg.Hysteria != nil {
		t.Error("the transport was not disabled")
	}
	if !slices.Contains(cfg.Security.Firewall.UDPPorts, proto.HysteriaPort) {
		t.Errorf("disabling dropped the port: %v", cfg.Security.Firewall.UDPPorts)
	}
}

// Both flags at once is a contradiction, not a last-one-wins: the operator asked for the opposite of what
// they asked for, and nothing should change.
func TestInitRefusesContradictoryTransportFlags(t *testing.T) {
	t.Parallel()
	app, _, configPath := hardenApp(t, config.WarpAll)

	if code := app.run([]string{"init", "--config", configPath, "--dry-run", "--hysteria", "--no-hysteria"}); code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr: %s", code, exitUsage, app.stderr.(*bytes.Buffer).String())
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Hysteria != nil {
		t.Error("a refused run changed the configuration")
	}
}
