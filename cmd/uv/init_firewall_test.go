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

// TestAppliedLacksQUIC: the hint after `init` is only true when a table is in force and does not carry
// the QUIC port. A node with nothing applied has nothing to re-render, a table that already carries the
// port needs no second apply, and a table the operator removed is not in force at all.
func TestAppliedLacksQUIC(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Paths.StateDir = t.TempDir()
	cfg.Paths.ConfigDir = t.TempDir()
	path := cfg.Paths.FirewallPath()

	if appliedLacksQUIC(cfg) {
		t.Error("a missing record was reported as a table without the QUIC port")
	}
	writeApplied(t, path, firewall.Applied{
		Table: "inet termit", Ports: []int{22, 80, 443}, UDPPorts: []int{443},
		AppliedAt: time.Now(),
	})
	if appliedLacksQUIC(cfg) {
		t.Error("a table with UDP/443 was reported as missing it")
	}
	writeApplied(t, path, firewall.Applied{
		Table: "inet termit", Ports: []int{22, 80, 443}, AppliedAt: time.Now(),
	})
	if !appliedLacksQUIC(cfg) {
		t.Error("a table without UDP ports was not reported as missing the QUIC port")
	}
	writeApplied(t, path, firewall.Applied{
		Table: "inet termit", Ports: []int{22, 80, 443}, AppliedAt: time.Now().Add(-time.Hour),
		ConfirmedAt: time.Now().Add(-time.Hour), RemovedAt: time.Now().Add(-time.Minute),
	})
	if appliedLacksQUIC(cfg) {
		t.Error("a table the operator removed was reported as in force")
	}
}
