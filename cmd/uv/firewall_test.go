package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/service"
)

// nftHost is a fake host: it answers the nft queries the way a kernel with (or without) the table
// does, records every command, and can be told to fail one.
type nftHost struct {
	table  bool
	counts string
	cmds   []string
	// stdin keeps what nft received, so a test can assert the ruleset itself.
	stdin  []string
	failOn string
}

func (h *nftHost) run(cmd install.Cmd) (install.Result, error) {
	h.cmds = append(h.cmds, cmd.String())
	if h.failOn != "" && strings.Contains(cmd.String(), h.failOn) {
		return install.Result{ExitCode: 1}, &install.ExitError{Cmd: cmd, Result: install.Result{ExitCode: 1}}
	}
	args := strings.Join(cmd.Args, " ")
	switch {
	case strings.Contains(args, "-j list tables"):
		tables := `{"nftables": [{"metainfo": {"version": "1.1.3"}}, {"table": {"family": "inet", "name": "filter"}}`
		if h.table {
			tables += `, {"table": {"family": "inet", "name": "termit_uv"}}`
		}
		return install.Result{Stdout: tables + "]}"}, nil
	case strings.Contains(args, "-j list table"):
		h.stdin = append(h.stdin, cmd.Stdin)
		return install.Result{Stdout: h.counts}, nil
	case strings.HasSuffix(args, "-f -"):
		h.stdin = append(h.stdin, cmd.Stdin)
		// The ruleset is an apply and carries the chain; anything else is the destroy `off` sends.
		h.table = strings.Contains(cmd.Stdin, "chain input")
		return install.Result{}, nil
	}
	return install.Result{}, nil
}

// firewallFixture is a test CLI on a node whose table is (or is not) applied.
type firewallFixture struct {
	app        *application
	host       *nftHost
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	configPath string
}

// newFirewallFixture builds a node whose unit and state directories live in a temporary directory:
// the real ones are /etc and /var/lib, which a test must never touch.
func newFirewallFixture(t *testing.T, enabled, guard bool) *firewallFixture {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.Domain = "node.example.com"
	cfg.Security.Firewall.Enabled = enabled
	cfg.Security.Guard.Enabled = guard
	cfg.Paths.ConfigDir = filepath.Join(dir, "etc")
	cfg.Paths.StateDir = filepath.Join(dir, "state")
	cfg.Paths.CertDir = filepath.Join(dir, "certs")
	cfg.Paths.ACMEWebroot = filepath.Join(dir, "www")
	cfg.Paths.SiteDir = filepath.Join(dir, "site")
	cfg.Paths.BinDir = filepath.Join(dir, "bin")
	cfg.Paths.XrayConfig = filepath.Join(dir, "etc", "xray.json")
	cfg.Paths.NginxSite = filepath.Join(dir, "nginx", "termit")
	cfg.Paths.SitesEnabled = filepath.Join(dir, "nginx", "sites-enabled")
	cfg.Paths.UnitDir = filepath.Join(dir, "units")
	cfg.Paths.ConfigFile = configPath
	cfg.Paths.NginxDropIn = filepath.Join(dir, "nginx", "termit.conf")
	cfg.Paths.DecoySnippet = filepath.Join(dir, "nginx", "decoy.conf")
	cfg.Paths.AccessLog = filepath.Join(dir, "logs", "access.log")
	cfg.Paths.ErrorLog = filepath.Join(dir, "logs", "error.log")
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	host := &nftHost{counts: `{"nftables": []}`}
	app, stdout, stderr := newTestApp(t)
	app.exec = stubExecutor{handler: host.run}
	// Root: these commands change the host's firewall, and a test that pretended to be a normal user
	// would only exercise the refusal.
	app.euid = func() int { return 0 }
	app.executable = "/usr/bin/termit-uv"
	return &firewallFixture{app: app, host: host, stdout: stdout, stderr: stderr, configPath: configPath}
}

// run is the fixture's shorthand for one command.
func (f *firewallFixture) run(args ...string) int {
	return f.app.run(append(args, "--config", f.configPath))
}

// TestFirewallOnAppliesAndArms: the order of the commands is the safety property. The timer is armed
// before nft sees a rule, so a firewall that cannot be rolled back is never installed.
func TestFirewallOnAppliesAndArms(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("run = %d, want %d: %s", got, exitOK, f.stderr.String())
	}
	want := []string{
		"systemctl daemon-reload",
		"systemctl restart " + service.FirewallRollbackTimerName,
		"nft -f -",
	}
	if len(f.host.cmds) != len(want) {
		t.Fatalf("commands = %v, want %v", f.host.cmds, want)
	}
	for i := range want {
		if f.host.cmds[i] != want[i] {
			t.Fatalf("command %d = %q, want %q", i, f.host.cmds[i], want[i])
		}
	}
	if !strings.Contains(f.host.stdin[0], "table inet termit_uv {") {
		t.Errorf("nft received %q", f.host.stdin[0])
	}
	out := f.stdout.String()
	if !strings.Contains(out, "firewall: on") || !strings.Contains(out, "termit-uv firewall confirm") {
		t.Errorf("output = %q", out)
	}
	// The units are on disk, so the timer has something to run.
	for _, name := range []string{service.FirewallRollbackUnitName, service.FirewallRollbackTimerName} {
		cfg, err := config.Load(f.configPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cfg.Paths.UnitDir, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
}

// TestFirewallOnArmsBeforeItApplies: when the timer cannot be armed, no rule may be applied.
func TestFirewallOnArmsBeforeItApplies(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	f.host.failOn = "systemctl restart"
	if got := f.run("firewall", "on"); got != exitFailure {
		t.Fatalf("run = %d, want %d", got, exitFailure)
	}
	for _, cmd := range f.host.cmds {
		if cmd == "nft -f -" {
			t.Fatal("the table was applied although the rollback timer could not be armed")
		}
	}
	if f.stderr.Len() == 0 {
		t.Error("the failure said nothing")
	}
}

// TestFirewallOnDryRunPrintsAndChangesNothing: --dry-run is what an operator uses before trusting the
// command, and what the lab asserts.
func TestFirewallOnDryRunPrintsAndChangesNothing(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, false, true)
	if got := f.run("firewall", "on", "--dry-run"); got != exitOK {
		t.Fatalf("run = %d, want %d: %s", got, exitOK, f.stderr.String())
	}
	for _, cmd := range f.host.cmds {
		if strings.HasPrefix(cmd, "systemctl") || cmd == "nft -f -" {
			t.Errorf("a dry run ran %q", cmd)
		}
	}
	out := f.stdout.String()
	for _, want := range []string{"destroy table inet termit_uv", "chain input", "counter drop", "meter scan4m"} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed ruleset is missing %q", want)
		}
	}
}

// TestFirewallNeedsRoot: the refusal has to come before anything is written.
func TestFirewallNeedsRoot(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	f.app.euid = func() int { return 1000 }
	if got := f.run("firewall", "on"); got != exitFailure {
		t.Fatalf("run = %d, want %d", got, exitFailure)
	}
	if len(f.host.cmds) != 0 {
		t.Errorf("a non-root run still ran %v", f.host.cmds)
	}
	if !strings.Contains(f.stderr.String(), "needs root") {
		t.Errorf("stderr = %q", f.stderr.String())
	}
}

// TestFirewallConfirmKeepsTheRules: confirm changes no rule and stands the timer down.
func TestFirewallConfirmKeepsTheRules(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("on = %d: %s", got, f.stderr.String())
	}
	f.host.cmds = nil
	f.stdout.Reset()
	if got := f.run("firewall", "confirm"); got != exitOK {
		t.Fatalf("confirm = %d: %s", got, f.stderr.String())
	}
	for _, cmd := range f.host.cmds {
		if strings.HasPrefix(cmd, "nft") {
			t.Errorf("confirm ran %q; it changes no rule", cmd)
		}
	}
	if !strings.Contains(f.stdout.String(), "confirmed") {
		t.Errorf("output = %q", f.stdout.String())
	}
	// A second confirm is a no-op, not an error.
	if got := f.run("firewall", "confirm"); got != exitOK {
		t.Errorf("second confirm = %d, want %d", got, exitOK)
	}
	// And the rollback afterwards must leave the table alone.
	f.host.cmds = nil
	if got := f.run("firewall", "rollback"); got != exitOK {
		t.Fatalf("rollback = %d", got)
	}
	for _, cmd := range f.host.cmds {
		if cmd == "nft -f -" {
			t.Error("the rollback removed a confirmed table")
		}
	}
}

// TestFirewallRollbackRemovesAnUnconfirmedTable is the safety net working.
func TestFirewallRollbackRemovesAnUnconfirmedTable(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("on = %d: %s", got, f.stderr.String())
	}
	f.host.cmds = nil
	f.stderr.Reset()
	if got := f.run("firewall", "rollback"); got != exitOK {
		t.Fatalf("rollback = %d: %s", got, f.stderr.String())
	}
	found := false
	for _, cmd := range f.host.cmds {
		if cmd == "nft -f -" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the rollback did not remove anything: %v", f.host.cmds)
	}
	if !strings.Contains(f.stderr.String(), "never confirmed") {
		t.Errorf("the rollback did not say why: %q", f.stderr.String())
	}
	if f.host.table {
		t.Error("the table is still there after a rollback")
	}
}

// TestFirewallOffIsTheWayBack: off removes the table and the timer, from any state.
func TestFirewallOffIsTheWayBack(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("on = %d: %s", got, f.stderr.String())
	}
	f.host.cmds = nil
	f.stdout.Reset()
	if got := f.run("firewall", "off"); got != exitOK {
		t.Fatalf("off = %d: %s", got, f.stderr.String())
	}
	var removed, disarmed bool
	for _, cmd := range f.host.cmds {
		if cmd == "nft -f -" {
			removed = true
		}
		if strings.HasPrefix(cmd, "systemctl disable") {
			disarmed = true
		}
	}
	if !removed || !disarmed {
		t.Errorf("off ran %v", f.host.cmds)
	}
	if !strings.Contains(f.stdout.String(), "firewall: off") {
		t.Errorf("output = %q", f.stdout.String())
	}
	cfg, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range []string{service.FirewallRollbackUnitName, service.FirewallRollbackTimerName} {
		if _, err := os.Stat(filepath.Join(cfg.Paths.UnitDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived `firewall off`", name)
		}
	}
}

// TestFirewallStatusReportsTheHost: the status has to describe the host, not the configuration.
func TestFirewallStatusReportsTheHost(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)

	// Nothing applied yet, and the configuration wants it: that is a note, not a lie.
	if got := f.run("firewall", "status"); got != exitOK {
		t.Fatalf("status = %d: %s", got, f.stderr.String())
	}
	out := f.stdout.String()
	if !strings.Contains(out, "firewall:   off") {
		t.Errorf("output = %q", out)
	}
	if !strings.Contains(out, "no table is applied") {
		t.Errorf("output does not note the drift: %q", out)
	}

	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("on = %d: %s", got, f.stderr.String())
	}
	f.host.counts = `{"nftables": [{"set": {"name": "banned4", "elem": [{"elem": {"val": "203.0.113.7", "expires": 100}}]}},
	                            {"set": {"name": "blocked4", "elem": ["192.0.2.0/24", "198.51.100.0/24"]}}]}`
	f.stdout.Reset()
	if got := f.run("firewall", "status"); got != exitOK {
		t.Fatalf("status = %d: %s", got, f.stderr.String())
	}
	out = f.stdout.String()
	for _, want := range []string{"firewall:   on, not confirmed", "1 bans, 2 blocked", "ports 22, 80, 443"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// TestFirewallStatusJSON is what a script reads.
func TestFirewallStatusJSON(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("on = %d: %s", got, f.stderr.String())
	}
	f.stdout.Reset()
	if got := f.run("firewall", "status", "--json"); got != exitOK {
		t.Fatalf("status --json = %d: %s", got, f.stderr.String())
	}
	var report map[string]any
	if err := json.Unmarshal(f.stdout.Bytes(), &report); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, f.stdout.String())
	}
	if report["table"] != firewall.NodeTable {
		t.Errorf("table = %v", report["table"])
	}
	if report["applied"] != true || report["confirmed"] != false {
		t.Errorf("applied/confirmed = %v/%v", report["applied"], report["confirmed"])
	}
	if report["default_deny"] != true {
		t.Errorf("default_deny = %v", report["default_deny"])
	}
	if _, ok := report["confirm_by"].(string); !ok {
		t.Errorf("confirm_by = %v, want a timestamp", report["confirm_by"])
	}
}

// TestFirewallOnOverridesTheConfiguration: the flags exist for the one-off case, and the record has to
// keep what was actually applied so `status` does not lie afterwards.
func TestFirewallOnOverridesTheConfiguration(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, false, true)
	if got := f.run("firewall", "on", "--ssh-from", "192.0.2.0/24", "--default-deny=false"); got != exitOK {
		t.Fatalf("on = %d: %s", got, f.stderr.String())
	}
	out := f.stdout.String()
	if !strings.Contains(out, "ssh:      port 22 from 192.0.2.0/24") {
		t.Errorf("output = %q", out)
	}
	if !strings.Contains(out, "other inbound allowed") {
		t.Errorf("output does not report default-deny=false: %q", out)
	}
	cfg, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil {
		t.Fatalf("LoadApplied: %v", err)
	}
	if applied.DefaultDeny || len(applied.SSHFrom) != 1 {
		t.Errorf("the record says %+v", applied)
	}
	// The configuration itself is untouched: the flags are a one-off, not an edit.
	if cfg.Security.Firewall.Enabled || !cfg.Security.Firewall.DenyOthers() {
		t.Errorf("the configuration changed: %+v", cfg.Security.Firewall)
	}
}

// TestFirewallOnRefusesABadNetwork stops a typo before the table exists.
func TestFirewallOnRefusesABadNetwork(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on", "--ssh-from", "192.0.2.0/33"); got != exitUsage {
		t.Fatalf("run = %d, want %d", got, exitUsage)
	}
	if len(f.host.cmds) != 0 {
		t.Errorf("a bad network still ran %v", f.host.cmds)
	}
}

// TestFirewallHelpIsInTheUsage: the command has to be discoverable.
func TestFirewallHelpIsInTheUsage(t *testing.T) {
	t.Parallel()
	app, stdout, _ := newTestApp(t)
	if got := app.run([]string{"help"}); got != exitOK {
		t.Fatalf("help = %d", got)
	}
	if !strings.Contains(stdout.String(), "firewall") {
		t.Errorf("the usage text does not mention the firewall:\n%s", stdout.String())
	}
}

// TestFirewallUnknownSubcommand: a typo is a usage error, not a status report.
func TestFirewallUnknownSubcommand(t *testing.T) {
	t.Parallel()
	app, _, _ := newTestApp(t)
	if got := app.run([]string{"firewall", "enable"}); got != exitUsage {
		t.Fatalf("run = %d, want %d", got, exitUsage)
	}
}
