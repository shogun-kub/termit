//go:build lab

package lab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/test/lab"
)

// TestLabHarden is the M5.6 acceptance on a node: `init --harden` configures the firewall and the ban
// engine, installs the guard's unit, and then stops — the table is still the operator's `firewall on`,
// and `status` says so. The wizard asks the same question and passes the same flag, so this covers both.
//
// The test reverts the configuration and removes the table at the end: it must not leave a node the rest of
// the suite would find strange.
func TestLabHarden(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	uv := l.UV3
	const configPath = "/etc/termit/uv/config.yaml"

	if out := uv.MustRun(t, ctx, "command -v nft"); strings.TrimSpace(out) == "" {
		t.Skip("nft is not installed on this lab node")
	}
	if out, err := uv.Run(ctx, "termit-uv init -h"); err != nil || !strings.Contains(out, "-harden") {
		t.Skipf("this node runs a binary without `init --harden`: %s", oneLine(out))
	}
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if out, err := uv.Run(cleanCtx, "termit-uv firewall off --config "+configPath); err != nil {
			t.Logf("cleanup: firewall off: %v\n%s", err, oneLine(out))
		}
		if out, err := uv.Run(cleanCtx, "termit-uv init --harden=false --config "+configPath); err != nil {
			t.Logf("cleanup: init --harden=false: %v\n%s", err, oneLine(out))
		}
	})

	// Whatever the node's configuration says now, get it to the "configured but not applied" state.
	uv.MustRun(t, ctx, "termit-uv init --harden --config "+configPath)
	for _, want := range []string{"firewall:", "enabled: true"} {
		if out := uv.MustRun(t, ctx, "grep -c 'enabled: true' "+configPath+" || true"); strings.TrimSpace(out) == "0" {
			t.Fatalf("the configuration has no enabled: true after --harden (%s):\n%s", want, out)
		}
	}

	t.Run("the guard unit is installed and running", func(t *testing.T) {
		uv.MustRun(t, ctx, "test -f /etc/systemd/system/"+service.GuardUnitName)
		if out := uv.MustRun(t, ctx, "systemctl is-enabled "+service.GuardUnitName); !strings.Contains(out, "enabled") {
			t.Errorf("the guard unit is %q, want enabled", strings.TrimSpace(out))
		}
		// It is running before any table exists, and does nothing: that is the design.
		if out := uv.MustRun(t, ctx, "systemctl is-active "+service.GuardUnitName); strings.TrimSpace(out) != "active" {
			t.Errorf("the guard unit is %q, want active", strings.TrimSpace(out))
		}
	})

	t.Run("status says the table is not applied", func(t *testing.T) {
		out := uv.MustRun(t, ctx, "termit-uv status")
		if !strings.Contains(out, "firewall:") || !strings.Contains(out, "the configuration asks for it") {
			t.Errorf("status does not tell the operator what to run:\n%s", out)
		}
	})

	t.Run("on applies it and the guard reads the table", func(t *testing.T) {
		uv.MustRun(t, ctx, "termit-uv firewall on --config "+configPath)
		uv.MustRun(t, ctx, "termit-uv firewall confirm --config "+configPath)
		if out := uv.MustRun(t, ctx, "termit-uv firewall status --config "+configPath); !strings.Contains(out, "confirmed") {
			t.Errorf("the table is not confirmed:\n%s", out)
		}
		// The guard's own pass against a live table: it must not need to ban anybody to succeed.
		if out, err := uv.Run(ctx, "termit-uv guard run --once --config "+configPath); err != nil {
			t.Errorf("a guard pass against the table failed: %v\n%s", err, oneLine(out))
		}
		if out := uv.MustRun(t, ctx, "termit-uv guard status --config "+configPath); !strings.Contains(out, "guard:      on") {
			t.Errorf("guard status:\n%s", out)
		}
	})

	t.Run("the boot unit puts a confirmed table back", func(t *testing.T) {
		uv.MustRun(t, ctx, "test -f /etc/systemd/system/"+service.FirewallBootUnitName)
		if out := uv.MustRun(t, ctx, "systemctl is-enabled "+service.FirewallBootUnitName); !strings.Contains(out, "enabled") {
			t.Errorf("the boot unit is %q, want enabled", strings.TrimSpace(out))
		}
		// A reboot empties the kernel and leaves the record. Deleting the table is the same thing,
		// without the wait. `restart`, not `start`: an operator who typed `start` after deleting the
		// table by hand must get it back, and that is the same no-op-if-active trap.
		uv.MustRun(t, ctx, "nft delete table inet "+firewall.NodeTable)
		if out, err := uv.Run(ctx, "systemctl restart "+service.FirewallBootUnitName); err != nil {
			t.Fatalf("the boot unit failed: %v\n%s", err, oneLine(out))
		}
		if out := uv.MustRun(t, ctx, "nft list tables"); !strings.Contains(out, firewall.NodeTable) {
			t.Errorf("the table did not come back:\n%s", out)
		}
		// Confirmed means confirmed: no rollback timer guards a table the operator already accepted.
		if out := uv.MustRun(t, ctx, "systemctl is-active "+service.FirewallRollbackTimerName+" || true"); strings.TrimSpace(out) == "active" {
			t.Error("the boot unit armed the rollback timer for a confirmed table")
		}
		if out := uv.MustRun(t, ctx, "termit-uv firewall status --config "+configPath); !strings.Contains(out, "confirmed") {
			t.Errorf("status after the restore:\n%s", out)
		}
		// And a table the operator took down stays down: the boot unit reads the record, and `off`
		// rewrote it.
		uv.MustRun(t, ctx, "termit-uv firewall off --config "+configPath)
		if out, err := uv.Run(ctx, "systemctl restart "+service.FirewallBootUnitName); err != nil {
			t.Fatalf("the boot unit failed after off: %v\n%s", err, oneLine(out))
		}
		if out := uv.MustRun(t, ctx, "nft list tables"); strings.Contains(out, firewall.NodeTable) {
			t.Errorf("a table the operator removed came back:\n%s", out)
		}
	})
}
