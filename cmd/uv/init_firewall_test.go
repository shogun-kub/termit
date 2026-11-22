package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// writeApplied puts a table record where `firewall on` leaves one.
func writeApplied(t *testing.T, path string, applied firewall.Applied) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := json.Marshal(applied)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestFirewallHasQUIC: the hint after `--hysteria` is only true when the table in force lacks the port.
// Saying it about a table that already carries it sends the owner to re-apply a correct table.
func TestFirewallHasQUIC(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	cfg.Paths.ConfigDir = t.TempDir()
	path := cfg.Paths.FirewallPath()

	if firewallHasQUIC(cfg) {
		t.Error("a missing record was reported as carrying the QUIC port")
	}
	writeApplied(t, path, firewall.Applied{
		Table: "inet termit", Ports: []int{22, 80, 443}, UDPPorts: []int{443},
		AppliedAt: time.Now(),
	})
	if !firewallHasQUIC(cfg) {
		t.Error("a table with UDP/443 was not reported as carrying the QUIC port")
	}
	writeApplied(t, path, firewall.Applied{
		Table: "inet termit", Ports: []int{22, 80, 443}, AppliedAt: time.Now(),
	})
	if firewallHasQUIC(cfg) {
		t.Error("a table without UDP ports was reported as carrying the QUIC port")
	}
}
