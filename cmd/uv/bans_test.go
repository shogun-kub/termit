package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// bansFixture is the firewall fixture with a table already applied, which is the state the ban
// commands act on.
func bansFixture(t *testing.T) *firewallFixture {
	t.Helper()
	f := newFirewallFixture(t, true, true)
	if got := f.run("firewall", "on"); got != exitOK {
		t.Fatalf("firewall on = %d: %s", got, f.stderr.String())
	}
	f.host.cmds = nil
	f.host.writes = nil
	f.stdout.Reset()
	f.stderr.Reset()
	return f
}

// record reads the bans file the commands wrote.
func (f *firewallFixture) record(t *testing.T) *firewall.Bans {
	t.Helper()
	cfg, err := f.app.loadConfig(f.configPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	bans, err := firewall.LoadBans(cfg.Paths.BansPath())
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	return bans
}

// TestBansBlockIsPermanentAndImmediate: an operator's block is in the file and in the kernel before the
// command returns, because the operator may be closing the session.
func TestBansBlockIsPermanentAndImmediate(t *testing.T) {
	t.Parallel()
	f := bansFixture(t)
	if got := f.run("bans", "block", "203.0.113.7"); got != exitOK {
		t.Fatalf("bans block = %d: %s", got, f.stderr.String())
	}
	ban, found := f.record(t).Get("203.0.113.7")
	if !found || !ban.Permanent() || ban.Signal != firewall.SignalOperator {
		t.Fatalf("record = %+v, found %v", ban, found)
	}
	if len(f.host.writes) != 1 || !strings.Contains(f.host.writes[0], "banned4 { 203.0.113.7 }") {
		t.Errorf("kernel writes = %v", f.host.writes)
	}
	if strings.Contains(f.host.writes[0], "timeout") {
		t.Errorf("a permanent ban was given a timeout: %q", f.host.writes[0])
	}
	if !strings.Contains(f.stdout.String(), "permanently") {
		t.Errorf("output = %q", f.stdout.String())
	}

	// A second block is a no-op, not a second record.
	if got := f.run("bans", "block", "203.0.113.7"); got != exitOK {
		t.Fatalf("second block = %d", got)
	}
	if bans := f.record(t); len(bans.Bans) != 1 {
		t.Errorf("the record has %d entries", len(bans.Bans))
	}
}

// TestBansBlockRefusesTheSelfInflicted: banning loopback, a private range or the management server
// would cut the operator off instead of an attacker.
func TestBansBlockRefusesTheSelfInflicted(t *testing.T) {
	t.Parallel()
	for _, ip := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.5", "::1", "fe80::1"} {
		f := bansFixture(t)
		if got := f.run("bans", "block", ip); got != exitUsage {
			t.Errorf("bans block %s = %d, want %d", ip, got, exitUsage)
		}
		if bans := f.record(t); len(bans.Bans) != 0 {
			t.Errorf("%s was recorded: %+v", ip, bans.Bans)
		}
		if len(f.host.writes) != 0 {
			t.Errorf("%s reached the kernel: %v", ip, f.host.writes)
		}
	}
}

// TestBansBlockRefusesAWord: the address is rendered into an nft command, so anything that is not an
// address is refused before that.
func TestBansBlockRefusesAWord(t *testing.T) {
	t.Parallel()
	f := bansFixture(t)
	if got := f.run("bans", "block", "not-an-address"); got != exitUsage {
		t.Fatalf("bans block = %d, want %d", got, exitUsage)
	}
	if len(f.host.writes) != 0 {
		t.Errorf("the kernel was touched: %v", f.host.writes)
	}
}

// TestBansUnblockLiftsOnlyTheOperatorsBan: `unblock` is for the permanent entry, `unban` for the rest.
func TestBansUnblockLiftsOnlyTheOperatorsBan(t *testing.T) {
	t.Parallel()
	f := bansFixture(t)
	if got := f.run("bans", "block", "203.0.113.7"); got != exitOK {
		t.Fatalf("block = %d", got)
	}
	f.host.writes = nil
	if got := f.run("bans", "unblock", "203.0.113.7"); got != exitOK {
		t.Fatalf("unblock = %d: %s", got, f.stderr.String())
	}
	if ban, found := f.record(t).Get("203.0.113.7"); !found || ban.Active(f.app.now()) {
		t.Error("unblock did not lift the ban")
	}
	if len(f.host.writes) != 1 || !strings.Contains(f.host.writes[0], "delete element") {
		t.Errorf("kernel writes = %v", f.host.writes)
	}

	// An automatic ban is not the operator's to unblock; the message points at `unban`.
	auto := bansFixture(t)
	cfg, err := auto.app.loadConfig(auto.configPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	bans := &firewall.Bans{SchemaVersion: firewall.BansSchemaVersion, Bans: []firewall.Ban{
		{IP: "198.51.100.9", Signal: firewall.SignalScan, Count: 1, Last: auto.app.now(), Until: auto.app.now().Add(time.Hour)},
	}}
	if err := firewall.SaveBans(cfg.Paths.BansPath(), bans); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}
	if got := auto.run("bans", "unblock", "198.51.100.9"); got != exitUsage {
		t.Fatalf("unblock on an automatic ban = %d, want %d", got, exitUsage)
	}
	if got := auto.run("bans", "unban", "198.51.100.9"); got != exitOK {
		t.Fatalf("unban = %d: %s", got, auto.stderr.String())
	}
	if ban, found := auto.record(t).Get("198.51.100.9"); !found || ban.Active(auto.app.now()) {
		t.Error("unban did not lift the ban")
	}
}

// TestBansListAndOffendersRenderTheRecord: what the operator reads.
func TestBansListAndOffendersRenderTheRecord(t *testing.T) {
	t.Parallel()
	f := bansFixture(t)
	cfg, err := f.app.loadConfig(f.configPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	now := f.app.now()
	bans := &firewall.Bans{SchemaVersion: firewall.BansSchemaVersion, Bans: []firewall.Ban{
		{IP: "203.0.113.7", Signal: firewall.SignalScan, Count: 2, Last: now, Until: now.Add(24 * time.Hour)},
		{IP: "203.0.113.8", Signal: firewall.SignalOperator, Count: 1, Last: now},
		{IP: "203.0.113.9", Signal: firewall.SignalPath404, Count: 6, Last: now, Until: now.Add(time.Hour), Offender: true},
	}}
	if err := firewall.SaveBans(cfg.Paths.BansPath(), bans); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}

	if got := f.run("bans", "list"); got != exitOK {
		t.Fatalf("list = %d: %s", got, f.stderr.String())
	}
	out := f.stdout.String()
	for _, want := range []string{"203.0.113.7", "scan", "permanent", "203.0.113.9", "ladder exhausted"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output %q does not contain %q", out, want)
		}
	}

	f.stdout.Reset()
	if got := f.run("bans", "offenders"); got != exitOK {
		t.Fatalf("offenders = %d: %s", got, f.stderr.String())
	}
	out = f.stdout.String()
	if !strings.Contains(out, "203.0.113.9") {
		t.Errorf("offenders output = %q", out)
	}
	if strings.Contains(out, "203.0.113.7") {
		t.Errorf("offenders lists a source that is merely banned: %q", out)
	}

	// The JSON form is what a script reads.
	f.stdout.Reset()
	if got := f.run("bans", "list", "--json"); got != exitOK {
		t.Fatalf("list --json = %d: %s", got, f.stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(f.stdout.Bytes(), &rows); err != nil {
		t.Fatalf("the list is not JSON: %v\n%s", err, f.stdout.String())
	}
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3", len(rows))
	}
	if rows[0]["ip"] != "203.0.113.8" || rows[0]["active"] != true {
		t.Errorf("the permanent ban is not first: %v", rows[0])
	}
}

// TestBansListSaysNone: an empty record is a sentence, not an empty table.
func TestBansListSaysNone(t *testing.T) {
	t.Parallel()
	f := bansFixture(t)
	if got := f.run("bans", "list"); got != exitOK {
		t.Fatalf("list = %d: %s", got, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "none active") {
		t.Errorf("output = %q", f.stdout.String())
	}
}

// TestGuardStatusReportsTheConfiguration: off says so; on says where the log is and what is banned.
func TestGuardStatusReportsTheConfiguration(t *testing.T) {
	t.Parallel()
	off := newFirewallFixture(t, true, false)
	if got := off.run("guard", "status"); got != exitOK {
		t.Fatalf("status = %d: %s", got, off.stderr.String())
	}
	if !strings.Contains(off.stdout.String(), "guard:      off") {
		t.Errorf("output = %q", off.stdout.String())
	}

	on := newFirewallFixture(t, true, true)
	if got := on.run("guard", "status"); got != exitOK {
		t.Fatalf("status = %d: %s", got, on.stderr.String())
	}
	out := on.stdout.String()
	for _, want := range []string{"guard:      on", "table:      inet termit_uv", "log:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// TestGuardRunExitsWhenDisabled: a unit that is still installed while the operator turned the guard off
// must exit successfully, or systemd restarts it every thirty seconds forever.
func TestGuardRunExitsWhenDisabled(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, false)
	if got := f.run("guard", "run"); got != exitOK {
		t.Fatalf("guard run = %d, want %d: %s", got, exitOK, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "stays off") {
		t.Errorf("output = %q", f.stdout.String())
	}
}

// TestGuardRunOnceWithNoTable: one pass with no table applied reads nothing and reports success, which
// is what a drill on a fresh node sees.
func TestGuardRunOnceWithNoTable(t *testing.T) {
	t.Parallel()
	f := newFirewallFixture(t, true, true)
	if got := f.run("guard", "run", "--once"); got != exitOK {
		t.Fatalf("guard run --once = %d: %s", got, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "one pass done") {
		t.Errorf("output = %q", f.stdout.String())
	}
}
