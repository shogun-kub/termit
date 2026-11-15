//go:build lab

package lab_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/test/lab"
)

// mnUnits are the server's security units. They are plain files in the repository, not rendered by the
// binary, so this test installs them the way the documentation tells an operator to.
var mnUnits = []string{
	"termit-mn-firewall-rollback.service",
	"termit-mn-firewall-rollback.timer",
	"termit-mn-guard.service",
	"termit-mn-blocklist.service",
	"termit-mn-blocklist.timer",
}

// TestLabMNFirewall is the M5.5 acceptance: the management server can carry the same table as a node,
// named for itself and sized for its own ports, and the mesh keeps working through it — the admin socket,
// a node's sync over :443 and the prober all survive the apply. `off` puts the host back byte for byte.
func TestLabMNFirewall(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	mn, uv := l.MN, l.UV1
	const configPath = "/etc/termit/mn/config.yaml"

	if out := mn.MustRun(t, ctx, "command -v nft"); strings.TrimSpace(out) == "" {
		t.Skip("nft is not installed on the lab server")
	}
	if out, err := mn.Run(ctx, "termit-mn firewall -h"); err != nil {
		t.Skipf("this server runs a binary without `firewall`: %s", oneLine(out))
	}
	for _, name := range mnUnits {
		if err := mn.Upload(ctx, filepath.Join("..", "..", "deploy", "systemd", name), "/etc/systemd/system/"+name); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}
	mn.MustRun(t, ctx, "systemctl daemon-reload")
	// Whatever happens, the server must not be left with a firewall nobody asked for.
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if out, err := mn.Run(cleanCtx, "termit-mn firewall off --config "+configPath); err != nil {
			t.Logf("cleanup: firewall off: %v\n%s", err, oneLine(out))
		}
		for _, unit := range []string{"termit-mn-guard.service", "termit-mn-blocklist.timer"} {
			if out, err := mn.Run(cleanCtx, "systemctl disable --now "+unit); err != nil {
				t.Logf("cleanup: disable %s: %v\n%s", unit, err, oneLine(out))
			}
		}
	})

	baseline := strings.Fields(mn.MustRun(t, ctx, "nft list ruleset | sha256sum"))[0]
	t.Logf("baseline ruleset digest: %s", baseline)

	t.Run("status says off on a server nobody hardened", func(t *testing.T) {
		out := mn.MustRun(t, ctx, "termit-mn firewall status --config "+configPath)
		if !strings.Contains(out, "firewall:   off") {
			t.Errorf("status before `on`:\n%s", out)
		}
	})

	var confirmed bool
	t.Run("on applies the server's table and the mesh keeps working", func(t *testing.T) {
		out := mn.MustRun(t, ctx, "termit-mn firewall on --config "+configPath)
		for _, want := range []string{"table inet " + firewall.ServerTable, "default deny", "ports 22, 80, 443"} {
			if !strings.Contains(out, want) {
				t.Errorf("`on` output is missing %q:\n%s", want, out)
			}
		}
		// Confirm immediately: the five-minute rollback timer is real, and this test is not trying to
		// race it.
		if out := mn.MustRun(t, ctx, "termit-mn firewall confirm --config "+configPath); !strings.Contains(out, "confirmed") {
			t.Errorf("confirm:\n%s", out)
		}
		confirmed = true

		// The admin socket is a unix socket, so it must be unaffected; that is also how the harness
		// reaches the server.
		if out, err := l.MNAdmin(ctx, "uv", "list"); err != nil {
			t.Errorf("the admin socket is broken by the table: %v\n%s", err, out)
		}
		// A node's sync goes to :443, which the table has to keep open.
		if out, err := uv.Run(ctx, "termit-uv sync"); err != nil {
			t.Errorf("a node cannot sync through the table: %v\n%s", err, oneLine(out))
		}
		// And the prober's own checks, which are outbound, must all still pass.
		if out, err := l.MNAdmin(ctx, "check", "now", "--all"); err != nil {
			t.Errorf("check --all through the table: %v\n%s", err, out)
		}
		status := mn.MustRun(t, ctx, "termit-mn firewall status --config "+configPath)
		for _, want := range []string{"confirmed", "inet " + firewall.ServerTable, "default deny"} {
			if !strings.Contains(status, want) {
				t.Errorf("status is missing %q:\n%s", want, status)
			}
		}
	})

	t.Run("the ban commands work on the server", func(t *testing.T) {
		if out := mn.MustRun(t, ctx, "termit-mn bans block 203.0.113.7"); !strings.Contains(out, "permanently") {
			t.Errorf("bans block:\n%s", out)
		}
		sets := mn.MustRun(t, ctx, "nft list set inet "+firewall.ServerTable+" banned4 2>/dev/null; true")
		if !strings.Contains(sets, "203.0.113.7") {
			t.Errorf("the banned address is not in the kernel:\n%s", sets)
		}
		if out := mn.MustRun(t, ctx, "termit-mn bans unblock 203.0.113.7"); !strings.Contains(out, "no longer banned") {
			t.Errorf("bans unblock:\n%s", out)
		}
	})

	t.Run("off puts the host back byte for byte", func(t *testing.T) {
		if !confirmed {
			t.Skip("`on` did not run")
		}
		if out, err := mn.Run(ctx, "termit-mn firewall off --config "+configPath); err != nil {
			t.Fatalf("off: %v\n%s", err, oneLine(out))
		}
		after := strings.Fields(mn.MustRun(t, ctx, "nft list ruleset | sha256sum"))[0]
		if after != baseline {
			t.Errorf("the ruleset changed: before %s, after %s", baseline, after)
		}
		// The record survives the table, which is the point: the kernel's sets are a copy, and the guard
		// refills them from the file. The address is listed as an offence even though it was lifted.
		out := mn.MustRun(t, ctx, "grep -c 203.0.113.7 /var/lib/termit/mn/bans.json || true")
		if strings.TrimSpace(out) == "0" {
			t.Errorf("the offence was forgotten with the table:\n%s", out)
		}
	})
}
