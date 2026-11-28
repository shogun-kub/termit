package hardencmd_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/harden"
	"github.com/shogun-kub/termit/internal/hardencmd"
	"github.com/shogun-kub/termit/internal/uv/install"
)

// This package is the security block both binaries run: the host's table, the ban list, the guard. What is
// tested here is the decision each command makes and the command it hands the host — a wrong nft call is a
// firewall nobody can recover from over SSH.

var testNow = time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

type fakeTarget struct {
	name   string
	table  string
	ports  []int
	config *harden.Config
	paths  hardencmd.Paths
}

func (f fakeTarget) Name() string           { return f.name }
func (f fakeTarget) Table() (string, []int) { return f.table, f.ports }
func (f fakeTarget) Units() hardencmd.Units {
	return hardencmd.Units{}
}

func (f fakeTarget) Open(string) (*hardencmd.Component, error) {
	if f.config == nil {
		return nil, errors.New("no configuration")
	}
	return &hardencmd.Component{
		Security:  f.config,
		Paths:     f.paths,
		Never:     func(context.Context) []netip.Prefix { return nil },
		UnitDir:   filepath.Dir(f.paths.Firewall),
		AccessLog: "",
	}, nil
}

type fakeExec struct {
	cmds    []install.Cmd
	present bool
	fail    error
}

func (f *fakeExec) Run(_ context.Context, cmd install.Cmd) (install.Result, error) {
	f.cmds = append(f.cmds, cmd)
	if f.fail != nil {
		return install.Result{}, f.fail
	}
	if len(cmd.Args) > 0 && cmd.Args[0] == "list" && f.present {
		return install.Result{Stdout: "table inet " + cmd.Args[len(cmd.Args)-1]}, nil
	}
	return install.Result{}, nil
}

func (f *fakeExec) names() []string {
	out := make([]string, 0, len(f.cmds))
	for _, cmd := range f.cmds {
		out = append(out, cmd.Name+" "+strings.Join(cmd.Args, " "))
	}
	return out
}

func (f *fakeExec) applied() bool {
	for _, cmd := range f.cmds {
		if cmd.Name == "nft" && len(cmd.Args) > 1 && cmd.Args[0] == "-f" {
			return true
		}
	}
	return false
}

type fakeRollback struct {
	armed    int
	disarmed int
	fail     error
}

func (r *fakeRollback) Arm(context.Context) error {
	r.armed++
	return r.fail
}

func (r *fakeRollback) Disarm(context.Context) error {
	r.disarmed++
	return nil
}

type harness struct {
	env      *hardencmd.Env
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	exec     *fakeExec
	rollback *fakeRollback
}

func newHarness(t *testing.T, root bool) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{
		stdout:   &bytes.Buffer{},
		stderr:   &bytes.Buffer{},
		exec:     &fakeExec{},
		rollback: &fakeRollback{},
	}
	deny := true
	h.env = &hardencmd.Env{
		Target: fakeTarget{
			name:  "termit-uv",
			table: "termit_uv",
			ports: []int{22, 80, 443},
			paths: hardencmd.Paths{
				Firewall:  filepath.Join(dir, "firewall.json"),
				Bans:      filepath.Join(dir, "bans.json"),
				Blocklist: filepath.Join(dir, "blocklist.txt"),
			},
			config: &harden.Config{Firewall: harden.Firewall{DefaultDeny: &deny}},
		},
		ConfigPath: filepath.Join(dir, "config.yaml"),
		Stdout:     h.stdout,
		Stderr:     h.stderr,
		Exec:       h.exec,
		Now:        func() time.Time { return testNow },
		Root:       func() bool { return root },
		Rollback:   func(*hardencmd.Component) hardencmd.Rollback { return h.rollback },
	}
	return h
}

func (h *harness) paths() hardencmd.Paths {
	return h.env.Target.(fakeTarget).paths
}

func TestFirewallDryRunPrintsTheRulesetAndTouchesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	if code := hardencmd.Firewall(h.env, []string{"on", "--dry-run"}); code != hardencmd.ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	output := h.stdout.String()
	for _, want := range []string{"table inet termit_uv", "tcp dport { 22, 80, 443 } accept"} {
		if !strings.Contains(output, want) {
			t.Errorf("the ruleset does not contain %q", want)
		}
	}
	if !strings.Contains(h.stderr.String(), "not verified with nft") {
		t.Errorf("a non-root dry run did not say it could not verify: %s", h.stderr.String())
	}
	if len(h.exec.cmds) != 0 {
		t.Errorf("a dry run ran %v", h.exec.names())
	}
	if h.rollback.armed != 0 {
		t.Errorf("a dry run armed the rollback timer")
	}
	if applied, err := firewall.LoadApplied(h.paths().Firewall); err != nil || applied != nil {
		t.Errorf("a dry run recorded the table as applied: %+v (%v)", applied, err)
	}
}

func TestFirewallOnNeedsRoot(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	if code := hardencmd.Firewall(h.env, []string{"on"}); code != hardencmd.ExitFailure {
		t.Fatalf("exit %d, want a failure without root", code)
	}
	if !strings.Contains(h.stderr.String(), "needs root") {
		t.Errorf("the refusal does not say why: %s", h.stderr.String())
	}
	if len(h.exec.cmds) != 0 {
		t.Errorf("a refused run ran %v", h.exec.names())
	}
}

// The timer is armed before the table goes in: if applying succeeds and the operator never confirms, the
// timer is what brings the host back.
func TestFirewallOnArmsTheTimerFirstAndAppliesTheTable(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	if code := hardencmd.Firewall(h.env, []string{"on"}); code != hardencmd.ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if h.rollback.armed != 1 {
		t.Fatalf("the rollback timer was armed %d times, want once", h.rollback.armed)
	}
	if !h.exec.applied() {
		t.Fatalf("the table was never applied: %v", h.exec.names())
	}
	applied, err := firewall.LoadApplied(h.paths().Firewall)
	if err != nil {
		t.Fatalf("the applied record is missing: %v", err)
	}
	if applied.Table != "termit_uv" || !applied.DefaultDeny {
		t.Errorf("the record says %+v", applied)
	}
	if h.rollback.disarmed != 0 {
		t.Error("the timer was disarmed without a reason")
	}
	if !strings.Contains(h.stdout.String(), "firewall: on, table inet termit_uv") {
		t.Errorf("the command did not report what it did: %s", h.stdout.String())
	}
}

func TestFirewallOnRefusesANetworkThatIsNotOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	if code := hardencmd.Firewall(h.env, []string{"on", "--ssh-from", "nonsense"}); code != hardencmd.ExitUsage {
		t.Fatalf("exit %d, want a usage error", code)
	}
	if len(h.exec.cmds) != 0 {
		t.Errorf("a refused run ran %v", h.exec.names())
	}
}

func TestFirewallOffRemovesTheTableAndDisarms(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	if code := hardencmd.Firewall(h.env, []string{"off"}); code != hardencmd.ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if h.rollback.disarmed != 1 {
		t.Errorf("the timer was disarmed %d times, want once", h.rollback.disarmed)
	}
	var removed bool
	for _, cmd := range h.exec.cmds {
		if cmd.Name == "nft" && strings.Join(cmd.Args, " ") == "-f -" &&
			strings.TrimSpace(cmd.Stdin) == "destroy table inet termit_uv" {
			removed = true
		}
	}
	if !removed {
		t.Errorf("the table was not removed: %v", h.exec.names())
	}
}

// A ban is a record first and a kernel entry second: with no table applied there is nothing to update, and
// the record is what the guard acts on later.
func TestBansBlockRecordsTheAddress(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	if err := firewall.SaveBans(h.paths().Bans, &firewall.Bans{}); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}
	if code := hardencmd.Bans(h.env, []string{"block", "192.0.2.7"}); code != hardencmd.ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	bans, err := firewall.LoadBans(h.paths().Bans)
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	if _, found := bans.Get("192.0.2.7"); !found {
		t.Fatalf("the address was not recorded: %+v", bans)
	}
	if !strings.Contains(h.stdout.String(), "banned permanently") {
		t.Errorf("the command did not report the ban: %s", h.stdout.String())
	}

	h.stdout.Reset()
	if code := hardencmd.Bans(h.env, []string{"list"}); code != hardencmd.ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "192.0.2.7") {
		t.Errorf("the list does not show the ban: %s", h.stdout.String())
	}
}

func TestBansBlockRefusesWhatIsNotAnAddress(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	if err := firewall.SaveBans(h.paths().Bans, &firewall.Bans{}); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}
	for _, raw := range []string{"nonsense", "192.0.2.0/33"} {
		if code := hardencmd.Bans(h.env, []string{"block", raw}); code != hardencmd.ExitUsage {
			t.Errorf("block %q exited %d, want a usage error", raw, code)
		}
	}
}

func TestBansUnblockLiftsOnlyTheOperatorsBan(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	bans := &firewall.Bans{}
	bans.Put(firewall.Ban{IP: "192.0.2.9", Signal: firewall.SignalScan, Count: 2,
		Last: testNow.Add(-time.Hour), Until: testNow.Add(time.Hour)})
	if err := firewall.SaveBans(h.paths().Bans, bans); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}

	// A scan's ban is not the operator's to lift with unblock; unban is the way, and it says so.
	if code := hardencmd.Bans(h.env, []string{"unblock", "192.0.2.9"}); code != hardencmd.ExitUsage {
		t.Fatalf("unblock exited %d, want a usage error", code)
	}
	if !strings.Contains(h.stderr.String(), "use `bans unban`") {
		t.Errorf("the refusal does not name the right command: %s", h.stderr.String())
	}

	h.stdout.Reset()
	h.stderr.Reset()
	if code := hardencmd.Bans(h.env, []string{"unban", "192.0.2.9"}); code != hardencmd.ExitOK {
		t.Fatalf("unban exited %d: %s", code, h.stderr.String())
	}
	lifted, err := firewall.LoadBans(h.paths().Bans)
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	record, found := lifted.Get("192.0.2.9")
	if !found {
		t.Fatal("the record was forgotten, not lifted")
	}
	if record.Count != 2 {
		t.Errorf("the offence kept %d, want 2", record.Count)
	}
	if record.Active(testNow) {
		t.Error("the ban is still active")
	}
}

// The record a failed apply leaves is the thing an operator would trust; there must not be one.
func TestFirewallOnReportsAnApplyThatFailed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, true)
	h.exec.fail = errors.New("nft: permission denied")
	if code := hardencmd.Firewall(h.env, []string{"on"}); code != hardencmd.ExitFailure {
		t.Fatalf("exit %d, want a failure", code)
	}
	if !strings.Contains(h.stderr.String(), "permission denied") {
		t.Errorf("the error was swallowed: %s", h.stderr.String())
	}
	if h.rollback.disarmed != 1 {
		t.Errorf("the timer was left armed after a failed apply")
	}
	if applied, err := firewall.LoadApplied(h.paths().Firewall); err != nil || applied != nil {
		t.Errorf("a failed apply recorded the table as applied: %+v (%v)", applied, err)
	}
}
