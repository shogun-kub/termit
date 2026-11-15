//go:build lab

package lab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestLabBlocklist is the M5.3 acceptance: a list is fetched over a URL, parsed, put in force in the
// kernel, and a source that is truncated, empty or unreachable leaves the list already in force alone.
//
// The timer's own unit is checked and then started by hand, which is exactly what systemd does once a
// day: waiting for the schedule would test systemd, not this code.
//
// The node keeps the blocklist configuration afterwards — the file is a valid list and the timer's daily
// run succeeds — so the test leaves a working node rather than a half-reverted one. `init` does not prune
// units that are no longer configured, so a revert would leave an enabled timer with nothing to fetch.
func TestLabBlocklist(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	uv := l.UV2
	const (
		configPath = "/etc/termit/uv/config.yaml"
		source     = "/root/blocklist.txt"
		broken     = "/root/blocklist-broken.txt"
		timerUnit  = "termit-uv-blocklist.timer"
	)
	if out := uv.MustRun(t, ctx, "command -v nft"); strings.TrimSpace(out) == "" {
		t.Skip("nft is not installed on this lab node")
	}
	if out, err := uv.Run(ctx, "termit-uv firewall refresh -h"); err != nil {
		t.Skipf("this node runs a binary without `firewall refresh`: %s", oneLine(out))
	}
	// Whatever happens, the node must not be left with a firewall nobody asked for.
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := uv.Run(cleanCtx, "termit-uv firewall off --config "+configPath); err != nil {
			t.Logf("cleanup: firewall off: %v\n%s", err, oneLine(out))
		}
	})

	uv.MustRun(t, ctx, "printf '198.18.0.0/15\\n203.0.113.0/24\\n2001:db8::/32\\n' > "+source)
	// The timer needs a URL in the configuration. The node keeps it after the test on purpose.
	if out := uv.MustRun(t, ctx, "grep -c blocklist_url "+configPath+" || true"); strings.TrimSpace(out) == "0" {
		uv.MustRun(t, ctx, `sed -i 's|^  firewall:$|  firewall:\n    blocklist_url: file://`+source+`|' `+configPath)
	}
	if out, err := uv.Run(ctx, "termit-uv init --config "+configPath); err != nil {
		t.Fatalf("init with a blocklist URL: %v\n%s", err, oneLine(out))
	}

	t.Run("the timer is installed and enabled", func(t *testing.T) {
		uv.MustRun(t, ctx, "test -f /etc/systemd/system/"+timerUnit)
		uv.MustRun(t, ctx, "test -f /etc/systemd/system/termit-uv-blocklist.service")
		if out := uv.MustRun(t, ctx, "systemctl is-enabled "+timerUnit); !strings.Contains(out, "enabled") {
			t.Errorf("the timer is %q, want enabled", strings.TrimSpace(out))
		}
	})

	// The table is the operator's to apply, so the test applies it — and confirms it, or the rollback
	// timer would remove it in five minutes.
	uv.MustRun(t, ctx, "termit-uv firewall on --config "+configPath)
	uv.MustRun(t, ctx, "termit-uv firewall confirm --config "+configPath)

	t.Run("the timer's oneshot puts the list in force", func(t *testing.T) {
		// The service is started by hand rather than waited for: this is the command systemd runs.
		if out, err := uv.Run(ctx, "systemctl start termit-uv-blocklist.service"); err != nil {
			t.Fatalf("the blocklist oneshot failed: %v\n%s", err, oneLine(out))
		}
		// A oneshot's ExecStart is asynchronous; give it the moment it needs.
		deadline := time.Now().Add(30 * time.Second)
		for {
			sets := blockedSets(t, ctx, uv)
			if strings.Contains(sets, "203.0.113.0/24") && strings.Contains(sets, "2001:db8::/32") {
				break
			}
			if time.Now().After(deadline) {
				job := uv.MustRun(t, ctx, "systemctl status termit-uv-blocklist.service --no-pager -l | tail -5")
				t.Fatalf("the kernel does not hold the list after the oneshot:\n%s\n%s", sets, job)
			}
			time.Sleep(time.Second)
		}
		if out := uv.MustRun(t, ctx, "termit-uv firewall status --config "+configPath); !strings.Contains(out, "3 networks") {
			t.Errorf("status does not report the list:\n%s", out)
		}
	})

	t.Run("a truncated source is refused and the list in force stays", func(t *testing.T) {
		before := blockedSets(t, ctx, uv)
		uv.MustRun(t, ctx, "printf '198.18.0.0/15\\n203.0.113' > "+broken)
		if out, err := uv.Run(ctx, "termit-uv firewall refresh --url file://"+broken); err == nil {
			t.Fatalf("a truncated list was accepted:\n%s", out)
		}
		if after := blockedSets(t, ctx, uv); after != before {
			t.Errorf("the kernel changed:\nbefore\n%s\nafter\n%s", before, after)
		}
	})

	t.Run("an unreachable source is refused and the list in force stays", func(t *testing.T) {
		before := blockedSets(t, ctx, uv)
		if out, err := uv.Run(ctx, "termit-uv firewall refresh --url file:///root/there-is-no-list-here"); err == nil {
			t.Fatalf("a missing source was accepted:\n%s", out)
		}
		if after := blockedSets(t, ctx, uv); after != before {
			t.Errorf("the kernel changed:\nbefore\n%s\nafter\n%s", before, after)
		}
	})

	t.Run("a network that left the source leaves the kernel", func(t *testing.T) {
		uv.MustRun(t, ctx, "printf '203.0.113.0/24\\n' > "+source)
		out := uv.MustRun(t, ctx, "termit-uv firewall refresh --url file://"+source)
		if !strings.Contains(out, "1 networks, loaded") {
			t.Errorf("refresh output:\n%s", out)
		}
		if sets := blockedSets(t, ctx, uv); strings.Contains(sets, "198.18.0.0/15") {
			t.Errorf("the network that left the source is still in the kernel:\n%s", sets)
		}
	})

	t.Run("on puts the list back after the table was removed", func(t *testing.T) {
		uv.MustRun(t, ctx, "termit-uv firewall off --config "+configPath)
		if sets := blockedSets(t, ctx, uv); strings.Contains(sets, "203.0.113.0/24") {
			t.Errorf("the list survived `off`:\n%s", sets)
		}
		out := uv.MustRun(t, ctx, "termit-uv firewall on --config "+configPath)
		uv.MustRun(t, ctx, "termit-uv firewall confirm --config "+configPath)
		if !strings.Contains(out, "blocklist: 1 networks loaded") {
			t.Errorf("`on` did not load the list:\n%s", out)
		}
		if sets := blockedSets(t, ctx, uv); !strings.Contains(sets, "203.0.113.0/24") {
			t.Errorf("the list is not in the kernel after `on`:\n%s", sets)
		}
	})
}

// blockedSets renders both blocklist sets, so a test can compare the kernel before and after. With no
// table applied nft has nothing to list, which is an empty set for this purpose, not a failure.
func blockedSets(t *testing.T, ctx context.Context, uv *lab.Machine) string {
	t.Helper()
	out := uv.MustRun(t, ctx, "nft list set inet termit_uv blocked4 2>/dev/null; "+
		"nft list set inet termit_uv blocked6 2>/dev/null; true")
	return out
}
