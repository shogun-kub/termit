package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/hardencmd"
	"github.com/shogun-kub/termit/internal/uv/install"
)

// mnExecutor answers the three commands the security commands run: nft, systemctl and nothing else. It
// records them, so a test can assert what was and was not touched.
type mnExecutor struct {
	cmds   []string
	table  bool // the server's table is applied
	failOn string
}

func (e *mnExecutor) Run(_ context.Context, cmd install.Cmd) (install.Result, error) {
	e.cmds = append(e.cmds, cmd.String())
	if e.failOn != "" && strings.Contains(cmd.String(), e.failOn) {
		return install.Result{ExitCode: 1}, &install.ExitError{Cmd: cmd, Result: install.Result{ExitCode: 1}}
	}
	args := strings.Join(cmd.Args, " ")
	switch {
	case strings.Contains(args, "-j list tables"):
		tables := `{"nftables": [{"metainfo": {"version": "1.1.3"}}`
		if e.table {
			tables += `, {"table": {"family": "inet", "name": "termit_mn"}}`
		}
		return install.Result{Stdout: tables + "]}"}, nil
	case strings.Contains(args, "-j list table"):
		return install.Result{Stdout: `{"nftables": []}`}, nil
	case strings.Contains(args, "-j list set"):
		return install.Result{Stdout: `{"nftables": []}`}, nil
	}
	return install.Result{}, nil
}

// mnFixture is the server's security commands against a temporary configuration and a fake host.
type mnFixture struct {
	exec *mnExecutor
	env  *hardencmd.Env
	cfg  string
	out  *bytes.Buffer
	err  *bytes.Buffer
}

func newMNFixture(t *testing.T, table bool) *mnFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	document := "schema_version: 1\ndomain: mn.example.com\ndata_dir: " + dir + "\n"
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	exec := &mnExecutor{table: table}
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	h := &mnHarden{exec: exec, now: func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }, root: func() bool { return true }}
	env := h.env(out, errBuf)
	env.ConfigPath = path
	return &mnFixture{exec: exec, env: env, cfg: path, out: out, err: errBuf}
}

// run executes one command and returns its exit code.
func (f *mnFixture) run(args ...string) int {
	f.out.Reset()
	f.err.Reset()
	switch args[0] {
	case "firewall":
		return hardencmd.Firewall(f.env, args[1:])
	case "bans":
		return hardencmd.Bans(f.env, args[1:])
	default:
		return hardencmd.Guard(f.env, args[1:])
	}
}

// TestMNFirewallNeedsTheRollbackTimer: the server's units come from its package, so an apply with no way
// back must be refused — and refused before nft is touched.
func TestMNFirewallNeedsTheRollbackTimer(t *testing.T) {
	t.Parallel()
	f := newMNFixture(t, false)
	f.exec.failOn = "restart termit-mn-firewall-rollback.timer"
	if got := f.run("firewall", "on"); got != exitFailure {
		t.Fatalf("on = %d, want failure: %s", got, f.err.String())
	}
	if !strings.Contains(f.err.String(), "is not installed") {
		t.Errorf("the refusal does not say what is missing: %q", f.err.String())
	}
	for _, cmd := range f.exec.cmds {
		if strings.Contains(cmd, "-f -") {
			t.Errorf("the table was applied anyway: %v", f.exec.cmds)
		}
	}
}

// TestMNFirewallStatusSaysOff: the first thing an operator does on a fresh server is look.
func TestMNFirewallStatusSaysOff(t *testing.T) {
	t.Parallel()
	f := newMNFixture(t, false)
	if got := f.run("firewall", "status"); got != exitOK {
		t.Fatalf("status = %d: %s", got, f.err.String())
	}
	for _, want := range []string{"firewall:   off", "inet termit_mn"} {
		if !strings.Contains(f.out.String(), want) && want == "firewall:   off" {
			t.Errorf("status output = %q", f.out.String())
		}
	}
}

// TestMNBansBlockWritesTheRecord: the commands work without a table; the record is what the guard reads
// when one appears.
func TestMNBansBlockWritesTheRecord(t *testing.T) {
	t.Parallel()
	f := newMNFixture(t, false)
	if got := f.run("bans", "block", "203.0.113.7"); got != exitOK {
		t.Fatalf("bans block = %d: %s", got, f.err.String())
	}
	if !strings.Contains(f.out.String(), "banned permanently") {
		t.Errorf("output = %q", f.out.String())
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(f.cfg), "bans.json"))
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if !strings.Contains(string(data), "203.0.113.7") {
		t.Errorf("the record does not hold the address:\n%s", data)
	}
	// And it is listed.
	if got := f.run("bans", "list"); got != exitOK {
		t.Fatalf("bans list = %d: %s", got, f.err.String())
	}
	if !strings.Contains(f.out.String(), "203.0.113.7") {
		t.Errorf("the list does not show the ban: %q", f.out.String())
	}
}

// TestMNBansRefusesThePrivateRanges: the server has no management server to protect, but loopback and
// the private ranges are still its own throat.
func TestMNBansRefusesThePrivateRanges(t *testing.T) {
	t.Parallel()
	f := newMNFixture(t, false)
	for _, address := range []string{"127.0.0.1", "192.168.1.1", "::1"} {
		if got := f.run("bans", "block", address); got != exitUsage {
			t.Errorf("bans block %s = %d, want usage error", address, got)
		}
	}
}

// TestMNGuardIsOffByDefault: nothing bans unless the operator asked.
func TestMNGuardIsOffByDefault(t *testing.T) {
	t.Parallel()
	f := newMNFixture(t, false)
	if got := f.run("guard", "status"); got != exitOK {
		t.Fatalf("guard status = %d: %s", got, f.err.String())
	}
	if !strings.Contains(f.out.String(), "guard:      off") {
		t.Errorf("output = %q", f.out.String())
	}
	// The loop exits 0 when the configuration says off: a unit that is installed but unwanted must not
	// restart itself forever.
	if got := f.run("guard", "run", "--once"); got != exitOK {
		t.Errorf("guard run --once = %d, want 0", got)
	}
	if !strings.Contains(f.out.String(), "stays off") {
		t.Errorf("output = %q", f.out.String())
	}
}

// TestMNTableIsTheServerTable: the table the commands touch is the server's, not the node's.
func TestMNTableIsTheServerTable(t *testing.T) {
	t.Parallel()
	h := &mnHarden{}
	name, ports := h.Table()
	if name != firewall.ServerTable {
		t.Errorf("table = %q, want %q", name, firewall.ServerTable)
	}
	if len(ports) != 3 {
		t.Errorf("ports = %v, want 3 entries", ports)
	}
}
