//go:build lab

package lab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestLabGuard is the M5.2 scenario, in two halves, because the lab is all private addresses.
//
// The first half proves the kernel's meter counts a real scan: the management server sends packets to
// ports nothing serves and its address appears in the scan set. That same address must then NOT be
// banned — it is RFC1918, and the never-ban rule exists exactly so the guard cannot cut the node off
// from its own server.
//
// The second half proves the ladder: a documentation-range address is put into the scan set the way the
// meter would, the guard turns it into an hour, the kernel is told, `bans unban` lifts it and forgets
// the signal, and the next offence earns the day.
func TestLabGuard(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	uv, mn := l.UV2, l.MN
	const configPath = "/etc/termit/uv/config.yaml"
	const offender = "203.0.113.7"

	// The marshalled configuration has `  guard:` followed by `    enabled: false`: a two-line edit that
	// leaves every other key alone.
	uv.MustRun(t, ctx, `sed -i '/^  guard:$/{n;s/enabled: false/enabled: true/}' `+configPath)
	if out := uv.MustRun(t, ctx, "grep -A1 '^  guard:' "+configPath); !strings.Contains(out, "enabled: true") {
		t.Fatalf("the guard could not be enabled:\n%s", out)
	}
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		restoreGuard(cleanCtx, t, uv, configPath)
	})

	// init installs and starts the guard unit; the firewall is applied by hand, as always.
	uv.MustRun(t, ctx, "termit-uv init --config "+configPath)
	uv.MustRun(t, ctx, "termit-uv firewall on --config "+configPath)
	uv.MustRun(t, ctx, "termit-uv firewall confirm --config "+configPath)
	if out := uv.MustRun(t, ctx, "systemctl is-active termit-uv-guard.service || true"); strings.TrimSpace(out) != "active" {
		t.Fatalf("the guard unit is %q", strings.TrimSpace(out))
	}

	serverAddress := strings.TrimSpace(mn.MustRun(t, ctx, `ip -4 route get 1.1.1.1 | awk '{print $7}'`))

	t.Run("the kernel counts a real scan and the guard refuses to ban a private address", func(t *testing.T) {
		scanFromMN(t, ctx, mn, uv.Host)
		// The meter is in the kernel, so the element appears without the guard doing anything.
		if out := uv.MustRun(t, ctx, "nft list set inet termit_uv scan4"); !strings.Contains(out, serverAddress) {
			t.Fatalf("the server's scan is not in the meter (%s):\n%s", serverAddress, out)
		}
		// A cycle later the guard has read it and left it alone: it is the management server.
		time.Sleep(35 * time.Second)
		if out := uv.MustRun(t, ctx, "termit-uv bans list --config "+configPath); strings.Contains(out, serverAddress) {
			t.Errorf("the guard banned the management server:\n%s", out)
		}
	})

	t.Run("a public scan earns an hour and reaches the kernel", func(t *testing.T) {
		injectScan(t, ctx, uv, offender)
		line := waitForBan(t, uv, configPath, 90*time.Second)
		t.Logf("ban: %s", line)
		if !strings.Contains(line, "scan") {
			t.Errorf("the ban is not for a scan: %q", line)
		}
		if strings.Contains(line, "23h") {
			t.Errorf("a first scan earned the repeat ban: %q", line)
		}
		if out := uv.MustRun(t, ctx, "nft list set inet termit_uv banned4"); !strings.Contains(out, offender) {
			t.Errorf("the ban did not reach the kernel:\n%s", out)
		}
	})

	t.Run("the other node keeps working", func(t *testing.T) {
		code := strings.TrimSpace(uv.MustRun(t, ctx,
			"curl -sk -o /dev/null -w '%{http_code}' --max-time 20 https://"+l.UV1.Host+"/"))
		if code != "200" {
			t.Errorf("the second node answered %q while a third address was banned", code)
		}
	})

	t.Run("an unban lifts it and forgets the signal", func(t *testing.T) {
		uv.MustRun(t, ctx, "termit-uv bans unban "+offender+" --config "+configPath)
		if out := uv.MustRun(t, ctx, "termit-uv bans list --config "+configPath); strings.Contains(out, offender) {
			t.Errorf("the address is still listed after the unban:\n%s", out)
		}
		// The meter's memory is cleared too, or the next cycle would ban again and the unban would look
		// broken.
		if out := uv.MustRun(t, ctx, "nft list set inet termit_uv scan4"); strings.Contains(out, offender) {
			t.Errorf("the unban left the scan element behind:\n%s", out)
		}
		time.Sleep(35 * time.Second)
		if out := uv.MustRun(t, ctx, "termit-uv bans list --config "+configPath); strings.Contains(out, offender) {
			t.Errorf("the guard banned again after the unban:\n%s", out)
		}
	})

	t.Run("the next scan earns the day", func(t *testing.T) {
		injectScan(t, ctx, uv, offender)
		line := waitForBan(t, uv, configPath, 90*time.Second)
		t.Logf("ban: %s", line)
		if !strings.Contains(line, "23h") {
			t.Errorf("a repeat scan did not earn the 24-hour ban: %q", line)
		}
	})
}

// scanFromMN sends a burst of packets to ports nothing serves on the node. UDP needs no handshake, so
// forty attempts take a moment and the kernel's meter sees all of them.
func scanFromMN(t *testing.T, ctx context.Context, mn *lab.Machine, host string) {
	t.Helper()
	mn.MustRun(t, ctx, "for i in $(seq 1 40); do echo -n x > /dev/udp/"+host+"/9000 || true; done")
}

// injectScan puts an address in the kernel's scan set the way the meter would. The lab has no public
// source to scan from, and every address it does have is one the guard must not ban.
func injectScan(t *testing.T, ctx context.Context, uv *lab.Machine, address string) {
	t.Helper()
	uv.MustRun(t, ctx, "nft add element inet termit_uv scan4 { "+address+" timeout 15m }")
}

// waitForBan polls the ban list until it holds a row, and returns it.
func waitForBan(t *testing.T, uv *lab.Machine, configPath string, timeout time.Duration) string {
	t.Helper()
	var line string
	err := lab.WaitFor(context.Background(), timeout, 5*time.Second, "a ban to appear", func(ctx context.Context) (bool, error) {
		out, err := uv.Run(ctx, "termit-uv bans list --config "+configPath)
		if err != nil {
			return false, err
		}
		for _, candidate := range strings.Split(out, "\n") {
			if strings.Contains(candidate, "scan") || strings.Contains(candidate, "path404") {
				line = candidate
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	return line
}

// restoreGuard puts the node back: the guard is disabled, its unit removed, the table taken down and
// the bans forgotten.
func restoreGuard(ctx context.Context, t *testing.T, uv *lab.Machine, configPath string) {
	t.Helper()
	steps := []string{
		"termit-uv firewall off --config " + configPath,
		"systemctl disable --now termit-uv-guard.service || true",
		"rm -f /etc/systemd/system/termit-uv-guard.service",
		"systemctl daemon-reload",
		`sed -i '/^  guard:$/{n;s/enabled: true/enabled: false/}' ` + configPath,
		"rm -f /var/lib/termit/uv/bans.json",
	}
	for _, step := range steps {
		if out, err := uv.Run(ctx, step); err != nil {
			t.Logf("cleanup: %s: %v\n%s", step, err, out)
		}
	}
}
