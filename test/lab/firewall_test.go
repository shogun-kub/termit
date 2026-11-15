//go:build lab

package lab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/test/lab"
)

// TestLabFirewall runs the M5.1 scenario on one node: the table is applied, SSH and 443 survive it, the
// rollback timer is armed and does its job, `off` puts the host back byte for byte, and the rollback
// removes an unconfirmed table by itself.
//
// The five-minute wait is not spent here: the timer's oneshot is started by hand, which is the same
// command systemd would run. The sleep-protected property — that the timer really fires — was checked
// by hand once on this lab; what the suite guards is that every part is wired and that the host is
// never left behind.
func TestLabFirewall(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	uv := l.UV2
	const configPath = "/etc/termit/uv/config.yaml"
	domain := statusDomain(t, ctx, uv)

	if out := uv.MustRun(t, ctx, "command -v nft"); strings.TrimSpace(out) == "" {
		t.Skip("nft is not installed on this lab node")
	}
	// Whatever happens, the node must not be left with a firewall nobody asked for.
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := uv.Run(cleanCtx, "termit-uv firewall off --config "+configPath); err != nil {
			t.Logf("cleanup: firewall off: %v\n%s", err, out)
		}
	})

	baseline := strings.Fields(uv.MustRun(t, ctx, "nft list ruleset | sha256sum"))[0]
	t.Logf("baseline ruleset digest: %s", baseline)

	t.Run("status says off on a node nobody hardened", func(t *testing.T) {
		out := uv.MustRun(t, ctx, "termit-uv firewall status --config "+configPath)
		if !strings.Contains(out, "firewall:   off") {
			t.Errorf("status before `on`:\n%s", out)
		}
	})

	t.Run("dry run prints the table and changes nothing", func(t *testing.T) {
		out := uv.MustRun(t, ctx, "termit-uv firewall on --dry-run --config "+configPath)
		for _, want := range []string{"destroy table inet termit_uv", "chain input", "counter drop"} {
			if !strings.Contains(out, want) {
				t.Errorf("the printed ruleset is missing %q:\n%s", want, out)
			}
		}
		if got := strings.Fields(uv.MustRun(t, ctx, "nft list ruleset | sha256sum"))[0]; got != baseline {
			t.Errorf("a dry run changed the ruleset: %s != %s", got, baseline)
		}
	})

	t.Run("on applies the table and keeps SSH and 443 alive", func(t *testing.T) {
		out := uv.MustRun(t, ctx, "termit-uv firewall on --config "+configPath)
		t.Logf("on:\n%s", out)
		if !strings.Contains(out, "termit-uv firewall confirm") {
			t.Errorf("`on` did not say how to keep the rules:\n%s", out)
		}
		// This command is a new SSH connection: reaching it means port 22 survived.
		if got := strings.TrimSpace(uv.MustRun(t, ctx, "echo ssh-alive")); got != "ssh-alive" {
			t.Errorf("the new session printed %q", got)
		}
		if out := uv.MustRun(t, ctx, "nft list tables"); !strings.Contains(out, "table inet termit_uv") {
			t.Errorf("the table is not in the ruleset:\n%s", out)
		}
		// The decoy site must still answer, and so must the tunnel's port.
		code := strings.TrimSpace(uv.MustRun(t, ctx,
			"curl -sk -o /dev/null -w '%{http_code}' --max-time 20 https://"+domain+"/"))
		if code != "200" {
			t.Errorf("the decoy answered %q through the firewall", code)
		}
		if out := uv.MustRun(t, ctx, "ss -H -ltn | grep -c ':443 '"); strings.TrimSpace(out) == "0" {
			t.Error("nothing listens on 443 through the firewall")
		}
		// The rollback timer has to be armed before the operator walks away.
		if out := uv.MustRun(t, ctx, "systemctl is-active "+service.FirewallRollbackTimerName+" || true"); strings.TrimSpace(out) != "active" {
			t.Errorf("the rollback timer is %q", strings.TrimSpace(out))
		}
		status := uv.MustRun(t, ctx, "termit-uv firewall status --config "+configPath)
		if !strings.Contains(status, "on, not confirmed") || !strings.Contains(status, "confirm") {
			t.Errorf("status while unconfirmed:\n%s", status)
		}
	})

	t.Run("the rollback removes a table nobody confirmed", func(t *testing.T) {
		// The timer's oneshot, started by hand instead of waiting five minutes.
		uv.MustRun(t, ctx, "systemctl start "+service.FirewallRollbackUnitName)
		if out := uv.MustRun(t, ctx, "nft list tables"); strings.Contains(out, "table inet termit_uv") {
			t.Fatalf("the rollback left the table behind:\n%s", out)
		}
		status := uv.MustRun(t, ctx, "termit-uv firewall status --config "+configPath)
		if !strings.Contains(status, "firewall:   off") {
			t.Errorf("status after the rollback:\n%s", status)
		}
		if out := uv.MustRun(t, ctx, "journalctl -u "+service.FirewallRollbackUnitName+" -n 20 --no-pager -o cat"); !strings.Contains(out, "never confirmed") {
			t.Errorf("the rollback did not explain itself in the journal:\n%s", out)
		}
	})

	t.Run("confirm keeps the rules and off restores the host", func(t *testing.T) {
		// ssh_from is exercised with an unrestricted network on purpose: the lab's own addresses are
		// not in this repository, and a set that excludes the harness would lock the suite out. What is
		// asserted here is that the networks reach the table and that the restriction rules exist; the
		// behaviour drill, with an address that is refused, is the by-hand run in M5.1.
		uv.MustRun(t, ctx, "termit-uv firewall on --ssh-from 0.0.0.0/0 --config "+configPath)
		if out := uv.MustRun(t, ctx, "nft list set inet termit_uv ssh4"); !strings.Contains(out, "0.0.0.0/0") {
			t.Errorf("ssh_from did not reach the table:\n%s", out)
		}
		if out := uv.MustRun(t, ctx, "nft list chain inet termit_uv input"); !strings.Contains(out, "ip saddr @ssh4 accept") ||
			!strings.Contains(out, "tcp dport 22 drop") {
			t.Errorf("the ssh_from rules are not in the chain:\n%s", out)
		}
		uv.MustRun(t, ctx, "termit-uv firewall confirm --config "+configPath)
		if out := uv.MustRun(t, ctx, "systemctl is-active "+service.FirewallRollbackTimerName+" || true"); strings.TrimSpace(out) == "active" {
			t.Error("the timer is still armed after `confirm`")
		}
		status := uv.MustRun(t, ctx, "termit-uv firewall status --config "+configPath)
		if !strings.Contains(status, "confirmed") {
			t.Errorf("status after `confirm`:\n%s", status)
		}
		uv.MustRun(t, ctx, "termit-uv firewall off --config "+configPath)
		if got := strings.Fields(uv.MustRun(t, ctx, "nft list ruleset | sha256sum"))[0]; got != baseline {
			t.Errorf("`off` left the ruleset at %s, baseline was %s", got, baseline)
		}
		if out := uv.MustRun(t, ctx, "systemctl is-enabled "+service.FirewallRollbackTimerName+" 2>&1 || true"); strings.Contains(out, "enabled") {
			t.Errorf("the rollback timer is still installed:\n%s", out)
		}
	})
}
