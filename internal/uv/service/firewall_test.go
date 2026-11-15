package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/service"
)

// TestFirewallRollbackUnit: the oneshot is what turns a lockout into a five-minute outage, so it has
// to name the binary and the configuration it was installed with.
func TestFirewallRollbackUnit(t *testing.T) {
	t.Parallel()
	unit, err := service.FirewallRollback("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml")
	if err != nil {
		t.Fatalf("FirewallRollback: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"Type=oneshot",
		"ExecStart=/usr/bin/termit-uv firewall rollback --config /etc/termit/uv/config.yaml",
		"TimeoutStartSec=60s",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit is missing %q:\n%s", want, text)
		}
	}
	if _, err := service.FirewallRollback("termit-uv", "/etc/termit/uv/config.yaml"); err == nil {
		t.Error("a relative binary path was accepted")
	}
	if _, err := service.FirewallRollback("/usr/bin/termit-uv", "config.yaml"); err == nil {
		t.Error("a relative config path was accepted")
	}
}

// TestFirewallRollbackTimer: the window is a one-shot delay, not a calendar, and it is written in the
// whole seconds OnActiveSec accepts.
func TestFirewallRollbackTimer(t *testing.T) {
	t.Parallel()
	unit, err := service.FirewallRollbackTimer(service.ConfirmWindow)
	if err != nil {
		t.Fatalf("FirewallRollbackTimer: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"[Timer]",
		"OnActiveSec=300s",
		"RemainAfterElapse=no",
		"AccuracySec=1s",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("timer is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "WantedBy=") {
		t.Error("the rollback timer installs itself; only `firewall on` may enable it")
	}
	if _, err := service.FirewallRollbackTimer(0); err == nil {
		t.Error("a zero window was accepted")
	}
	if _, err := service.FirewallRollbackTimer(-time.Minute); err == nil {
		t.Error("a negative window was accepted")
	}
	odd, err := service.FirewallRollbackTimer(90 * time.Second)
	if err != nil {
		t.Fatalf("FirewallRollbackTimer: %v", err)
	}
	if !strings.Contains(string(odd), "OnActiveSec=90s") {
		t.Errorf("a 90s window rendered as %q", string(odd))
	}
}
