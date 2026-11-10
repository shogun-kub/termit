//go:build lab

package lab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestLabSmoke is the first thing a lab run should pass: the three machines answer over the pinned
// transport, run the expected Debian, have the binaries under test, and the management server lists
// its nodes. It is deliberately cheap and read-only.
func TestLabSmoke(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, m := range []*lab.Machine{l.MN, l.UV1, l.UV2} {
		debian := strings.TrimSpace(m.MustRun(t, ctx, "cat /etc/debian_version"))
		if !strings.HasPrefix(debian, "13") {
			t.Errorf("%s runs Debian %q, the lab promises 13 (trixie)", m.Name, debian)
		}
		arch := strings.TrimSpace(m.MustRun(t, ctx, "uname -m"))
		memory := strings.TrimSpace(m.MustRun(t, ctx, "awk '/MemTotal/ {print $2 \" kB\"}' /proc/meminfo"))
		t.Logf("%s: Debian %s, %s, %s", m.Name, debian, arch, memory)
	}

	for _, m := range []*lab.Machine{l.UV1, l.UV2} {
		t.Logf("%s: %s", m.Name, strings.TrimSpace(m.MustRun(t, ctx, "termit-uv version")))
		for _, unit := range []string{"termit-xray", "termit-uv-agent"} {
			if out, err := m.Run(ctx, "systemctl is-active "+unit); err != nil || strings.TrimSpace(out) != "active" {
				t.Errorf("%s: %s is %q (%v)", m.Name, unit, strings.TrimSpace(out), err)
			}
		}
	}

	t.Logf("mn: %s", strings.TrimSpace(l.MN.MustRun(t, ctx, "termit-mn version")))
	list := l.MustMNAdmin(t, ctx, "uv", "list")
	t.Logf("nodes:\n%s", list)
	if !strings.Contains(list, "uv_") {
		t.Errorf("the server lists no nodes:\n%s", list)
	}
}
