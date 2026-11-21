package firewall

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// nodeTable is the shape a node renders: three ports, the scan detector on, no SSH restriction.
func nodeTable(deny bool) Table {
	return Table{
		Name:        NodeTable,
		Ports:       NodePorts,
		DefaultDeny: deny,
		Scan: Scan{
			Enabled:  true,
			Attempts: 5,
			Window:   time.Minute,
			Remember: 15 * time.Minute,
		},
	}
}

// sshTable restricts port 22 to two networks, one of each family.
func sshTable(prefixes ...string) Table {
	table := nodeTable(true)
	parsed, err := Prefixes(prefixes)
	if err != nil {
		panic(err)
	}
	table.SSHFrom = parsed
	return table
}

// serverTable is the same mechanism with a server's ports and no scan detector.
func serverTable() Table {
	return Table{
		Name:        ServerTable,
		Ports:       []int{22, 80, 443, 8443},
		DefaultDeny: true,
	}
}

// quicTable publishes one port over both protocols, which is what a node with a QUIC transport looks
// like: 443 for nginx over TCP and for the transport over UDP.
func quicTable() Table {
	table := nodeTable(true)
	table.UDPPorts = []int{443}
	return table
}

// TestRenderGolden pins every shape of the table. The file is what nft executes, so a change a
// reviewer cannot see in a diff is a change nobody can review.
func TestRenderGolden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		table Table
	}{
		{"node-default", nodeTable(true)},
		{"node-open", nodeTable(false)},
		{"node-sshfrom", sshTable("192.0.2.0/24", "2001:db8::/32")},
		{"server", serverTable()},
		{"node-quic", quicTable()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := tc.table.Render()
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			golden := filepath.Join("testdata", tc.name+".nft")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(golden, data, 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden) // #nosec G304 -- a fixed test path.
			if err != nil {
				t.Fatalf("read %s: %v (run `go test ./internal/firewall -update`)", golden, err)
			}
			if !bytes.Equal(want, data) {
				t.Errorf("%s differs from the rendered table:\n%s\nregenerate with -update and review the diff", golden, data)
			}
		})
	}
}

// TestRenderIsDeterministic guards the property the golden files rest on: two renders of the same
// table are the same bytes, whatever order the ports and networks were given in.
// TestUDPAcceptComesBeforeTheMeter: the meter counts what was not accepted, and the guard bans for what
// the meter counted. A published UDP port placed after the meter would ban the very participants it is
// there for — a QUIC transport banning its own users.
func TestUDPAcceptComesBeforeTheMeter(t *testing.T) {
	t.Parallel()
	data, err := quicTable().Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(data)
	udp := strings.Index(text, "udp dport 443 accept")
	meter := strings.Index(text, "meter scan4m")
	if udp < 0 {
		t.Fatalf("no UDP accept line:\n%s", text)
	}
	if meter < 0 {
		t.Fatalf("no scan meter:\n%s", text)
	}
	if udp > meter {
		t.Error("the UDP accept comes after the meter, so published QUIC traffic would be counted as scanning")
	}
	if !strings.Contains(text, "tcp dport { 22, 80, 443 } accept") {
		t.Errorf("the TCP accept is missing:\n%s", text)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	t.Parallel()
	first, err := sshTable("2001:db8::/32", "192.0.2.0/24").Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := sshTable("192.0.2.0/24", "2001:db8::/32").Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Error("the rendered table depends on the order of ssh_from")
	}
}

// TestRenderRejects checks that a table which could publish nothing, or publish it to the wrong
// people, is refused before nft sees it.
func TestRenderRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*Table)
		want   string
	}{
		{"port zero", func(t *Table) { t.Ports = []int{0, 22} }, "outside 1-65535"},
		{"port too large", func(t *Table) { t.Ports = []int{22, 70000} }, "outside 1-65535"},
		{"duplicate port", func(t *Table) { t.Ports = []int{22, 22} }, "listed twice"},
		{"no ports", func(t *Table) { t.Ports = nil }, "no ports"},
		{"bad name", func(t *Table) { t.Name = "termit uv; drop" }, "lower-case letters"},
		{"empty name", func(t *Table) { t.Name = "" }, "needs a name"},
		{"long name", func(t *Table) { t.Name = "termit_" + strings.Repeat("a", 40) }, "longer than"},
		{"ssh_from without 22", func(t *Table) {
			t.Ports = []int{80, 443}
			t.SSHFrom = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
		}, "SSH would be closed"},
		{"scan threshold of one", func(t *Table) { t.Scan.Attempts = 1 }, "at least 2 attempts"},
		{"scan window with no spelling", func(t *Table) { t.Scan.Window = 10 * time.Minute }, "exactly 1s, 1m, 1h, 1d or 1w"},
		{"scan memory of zero", func(t *Table) { t.Scan.Remember = 0 }, "positive duration"},
		{"udp port zero", func(t *Table) { t.UDPPorts = []int{0} }, "outside 1-65535"},
		{"udp duplicate port", func(t *Table) { t.UDPPorts = []int{443, 443} }, "listed twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			table := nodeTable(true)
			tc.mutate(&table)
			_, err := table.Render()
			if err == nil {
				t.Fatalf("Render accepted a table it should refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestRenderRejectsHostBits: an operator who writes 192.0.2.5/24 means a network, and the table has to
// say so rather than silently widen or narrow it.
func TestRenderRejectsHostBits(t *testing.T) {
	t.Parallel()
	table := nodeTable(true)
	prefix, err := ParsePrefix("192.0.2.5/24")
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}
	if prefix.String() != "192.0.2.0/24" {
		t.Fatalf("ParsePrefix normalised to %s, want the network", prefix)
	}
	raw := table
	raw.SSHFrom = []netip.Prefix{netip.MustParsePrefix("192.0.2.5/24")}
	if _, err := raw.Render(); err == nil || !strings.Contains(err.Error(), "host bits") {
		t.Errorf("a network with host bits was not reported: %v", err)
	}
}

// TestPrefixes reads what an operator may put in the configuration.
func TestPrefixes(t *testing.T) {
	t.Parallel()
	got, err := Prefixes([]string{"192.0.2.5", "2001:db8::/32", " 198.51.100.0/24 "})
	if err != nil {
		t.Fatalf("Prefixes: %v", err)
	}
	want := []string{"192.0.2.5/32", "198.51.100.0/24", "2001:db8::/32"}
	if len(got) != len(want) {
		t.Fatalf("Prefixes returned %d networks, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("network %d is %s, want %s", i, got[i], want[i])
		}
	}
	if _, err := Prefixes([]string{"192.0.2.0/33"}); err == nil {
		t.Error("Prefixes accepted /33")
	}
	if _, err := Prefixes([]string{"not-an-address"}); err == nil {
		t.Error("Prefixes accepted a word")
	}
}

// TestFormatDuration checks the one-unit spellings nftables accepts. A compound form like 1h30m would
// be a ruleset nft refuses at load time, which is exactly the failure a golden file should not carry.
func TestFormatDuration(t *testing.T) {
	t.Parallel()
	cases := map[time.Duration]string{
		time.Second:        "1s",
		15 * time.Minute:   "15m",
		2 * time.Hour:      "2h",
		36 * time.Hour:     "36h",
		7 * 24 * time.Hour: "1w",
		90 * time.Second:   "90s",
	}
	for duration, want := range cases {
		if got := formatDuration(duration); got != want {
			t.Errorf("formatDuration(%s) = %q, want %q", duration, got, want)
		}
	}
}

// TestScanUnit pins the windows that have a spelling and the one that does not.
func TestScanUnit(t *testing.T) {
	t.Parallel()
	for _, window := range []time.Duration{time.Second, time.Minute, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
		if _, err := (Scan{Enabled: true, Attempts: 5, Window: window, Remember: time.Minute}).unit(); err != nil {
			t.Errorf("window %s should be expressible: %v", window, err)
		}
	}
	for _, window := range []time.Duration{10 * time.Minute, 90 * time.Second, 0, -time.Minute} {
		if _, err := (Scan{Enabled: true, Attempts: 5, Window: window, Remember: time.Minute}).unit(); err == nil {
			t.Errorf("window %s was accepted, but nftables cannot express it", window)
		}
	}
}

// fakeRunner records the commands the applier runs.
type fakeRunner struct {
	calls []string
	stdin []string
	reply func(args []string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdin string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	f.stdin = append(f.stdin, stdin)
	if f.reply != nil {
		return f.reply(args)
	}
	return "", nil
}

// TestApplyPipesTheRuleset is the property the whole package rests on: the ruleset reaches nft on
// standard input, so nothing is written to disk and no shell is involved.
func TestApplyPipesTheRuleset(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	ruleset, err := nodeTable(true).Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := (Applier{Runner: runner}).Apply(context.Background(), ruleset); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "nft -f -" {
		t.Fatalf("calls = %v, want one `nft -f -`", runner.calls)
	}
	if runner.stdin[0] != string(ruleset) {
		t.Error("the ruleset was not what nft received")
	}
}

// TestApplyReportsTheFailure: a rejected ruleset must not look like a success.
func TestApplyReportsTheFailure(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{reply: func([]string) (string, error) {
		return "", errors.New("Error: syntax error, unexpected drop")
	}}
	if err := (Applier{Runner: runner}).Apply(context.Background(), []byte("table inet x {}\n")); err == nil {
		t.Fatal("Apply reported success for a rejected ruleset")
	}
}

// TestRemoveIsIdempotent: `destroy` is what makes `firewall off` work on a host that never had the
// table, which is the state every fresh machine is in.
func TestRemoveIsIdempotent(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	if err := (Applier{Runner: runner}).Remove(context.Background(), NodeTable); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := runner.stdin[0]; got != "destroy table inet termit_uv\n" {
		t.Errorf("Remove sent %q", got)
	}
	if err := (Applier{Runner: runner}).Remove(context.Background(), "bad name"); err == nil {
		t.Error("Remove accepted a name that is not a table name")
	}
}

const tablesJSON = `{"nftables": [{"metainfo": {"version": "1.1.3"}},
 {"table": {"family": "inet", "name": "filter", "use": 0}},
 {"table": {"family": "inet", "name": "termit_uv", "use": 0}},
 {"table": {"family": "ip", "name": "termit_uv", "use": 0}}]}`

// TestPresentIgnoresOtherFamilies: only the inet table is ours, and a table of the same name in
// another family must not be mistaken for it.
func TestPresentIgnoresOtherFamilies(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{reply: func([]string) (string, error) { return tablesJSON, nil }}
	applier := Applier{Runner: runner}
	present, err := applier.Present(context.Background(), NodeTable)
	if err != nil {
		t.Fatalf("Present: %v", err)
	}
	if !present {
		t.Error("Present did not find inet termit_uv")
	}
	present, err = applier.Present(context.Background(), ServerTable)
	if err != nil {
		t.Fatalf("Present: %v", err)
	}
	if present {
		t.Error("Present found a table that is not there")
	}
}

const tableJSON = `{"nftables": [{"metainfo": {"version": "1.1.3"}},
 {"table": {"family": "inet", "name": "termit_uv"}},
 {"set": {"name": "blocked4", "elem": ["192.0.2.0/24", "198.51.100.0/24"]}},
 {"set": {"name": "banned4", "elem": [{"elem": {"val": "203.0.113.7", "timeout": 3600, "expires": 3599}}]}},
 {"set": {"name": "scan4"}}]}`

// TestCounts reads the element counts a status line needs, including the two element shapes nft
// returns: a bare string for an element without a timeout, an object for one with.
func TestCounts(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{reply: func(args []string) (string, error) {
		if len(args) > 1 && args[1] == "tables" {
			return tablesJSON, nil
		}
		return tableJSON, nil
	}}
	counts, err := (Applier{Runner: runner}).Counts(context.Background(), NodeTable)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts["blocked4"] != 2 || counts["banned4"] != 1 || counts["scan4"] != 0 {
		t.Errorf("counts = %v", counts)
	}
	if got := Describe(counts); got != "1 bans, 2 blocked" {
		t.Errorf("Describe = %q", got)
	}
}

// TestCountsOnAnAbsentTable: nothing to count is not an error, and it must not cost a second nft call.
func TestCountsOnAnAbsentTable(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{reply: func([]string) (string, error) { return tablesJSON, nil }}
	counts, err := (Applier{Runner: runner}).Counts(context.Background(), "termit_nope")
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("counts = %v, want none", counts)
	}
	if len(runner.calls) != 1 {
		t.Errorf("Counts made %d calls for an absent table, want 1", len(runner.calls))
	}
}

// TestAppliedRecord pins the three questions status asks of it.
func TestAppliedRecord(t *testing.T) {
	t.Parallel()
	applied := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	record := &Applied{AppliedAt: applied, Deadline: applied.Add(5 * time.Minute)}
	if !record.Pending(applied) {
		t.Error("a fresh apply is not pending")
	}
	if record.Confirmed() {
		t.Error("a fresh apply is confirmed")
	}
	record.ConfirmedAt = applied.Add(time.Minute)
	if record.Pending(applied.Add(2 * time.Minute)) {
		t.Error("a confirmed apply is still pending")
	}
	if !record.Confirmed() {
		t.Error("Confirmed does not see the confirmation")
	}
	record.RemovedAt = applied.Add(3 * time.Minute)
	if record.Pending(applied.Add(4 * time.Minute)) {
		t.Error("a removed apply is pending")
	}
	var nothing *Applied
	if nothing.Pending(applied) || nothing.Confirmed() {
		t.Error("a missing record is pending or confirmed")
	}
}

// TestAppliedRoundTrip: the rollback unit runs in a separate process and reads this file, so it has to
// survive a write and a read unchanged.
func TestAppliedRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "firewall.json")
	applied := &Applied{
		Table:       NodeTable,
		Ports:       NodePorts,
		SSHFrom:     []string{"192.0.2.0/24"},
		DefaultDeny: true,
		AppliedAt:   time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
		Deadline:    time.Date(2026, 11, 15, 12, 5, 0, 0, time.UTC),
	}
	if err := SaveApplied(path, applied); err != nil {
		t.Fatalf("SaveApplied: %v", err)
	}
	back, err := LoadApplied(path)
	if err != nil {
		t.Fatalf("LoadApplied: %v", err)
	}
	if back.Table != applied.Table || back.DefaultDeny != applied.DefaultDeny ||
		!back.AppliedAt.Equal(applied.AppliedAt) || !back.Deadline.Equal(applied.Deadline) {
		t.Errorf("the record changed in the round trip: %+v", back)
	}
	if len(back.SSHFrom) != 1 || back.SSHFrom[0] != "192.0.2.0/24" {
		t.Errorf("ssh_from = %v", back.SSHFrom)
	}
}

// TestLoadAppliedMissingIsNothing: a node that never applied a firewall has no file, and that is not
// an error.
func TestLoadAppliedMissingIsNothing(t *testing.T) {
	t.Parallel()
	record, err := LoadApplied(filepath.Join(t.TempDir(), "firewall.json"))
	if err != nil {
		t.Fatalf("LoadApplied: %v", err)
	}
	if record != nil {
		t.Errorf("LoadApplied returned %+v for a missing file", record)
	}
}

// TestLoadAppliedRefusesGarbage: a truncated file must not be read as "no firewall".
func TestLoadAppliedRefusesGarbage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "firewall.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadApplied(path); err == nil {
		t.Fatal("LoadApplied accepted a broken record")
	}
}

// TestDurationYAML pins how the configuration spells a duration.
func TestDurationYAML(t *testing.T) {
	t.Parallel()
	var parsed struct {
		Window Duration `yaml:"window"`
	}
	if err := yaml.Unmarshal([]byte("window: 15m"), &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.Window.Std() != 15*time.Minute {
		t.Errorf("window = %s, want 15m", parsed.Window.Std())
	}
	if err := yaml.Unmarshal([]byte("window: soon"), &parsed); err == nil {
		t.Error("a word was accepted as a duration")
	}
	if err := yaml.Unmarshal([]byte("window: 15"), &parsed); err == nil {
		t.Error("a bare number was accepted as a duration")
	}
	if got := parsed.Window.String(); got != "15m" {
		t.Errorf("String = %q, want 15m: the loader has to read it back", got)
	}
	for _, spellings := range []struct {
		duration time.Duration
		want     string
	}{
		{24 * time.Hour, "24h"},
		{90 * time.Second, "90s"},
		{7 * 24 * time.Hour, "168h"},
		{90 * time.Minute, "90m"},
	} {
		if got := Duration(spellings.duration).String(); got != spellings.want {
			t.Errorf("Duration(%s) = %q, want %q", spellings.duration, got, spellings.want)
		}
		if back, err := time.ParseDuration(Duration(spellings.duration).String()); err != nil || back != spellings.duration {
			t.Errorf("%q does not parse back to %s: %v", Duration(spellings.duration).String(), spellings.duration, err)
		}
	}
}

// TestRestoreIsDecidedByTheRecord is the boot unit's whole judgement, and the four cases are the four
// kinds of record an operator can leave behind.
func TestRestoreIsDecidedByTheRecord(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		applied  *Applied
		apply    bool
		armTimer bool
	}{
		{"nothing was ever applied", nil, false, false},
		{
			"a confirmed apply comes back on its own",
			&Applied{AppliedAt: now.Add(-24 * time.Hour), ConfirmedAt: now.Add(-23 * time.Hour)},
			true, false,
		},
		{
			"an apply still waiting comes back with the timer",
			&Applied{AppliedAt: now.Add(-time.Minute), Deadline: now.Add(4 * time.Minute)},
			true, true,
		},
		{
			"an apply whose window passed stays gone",
			&Applied{AppliedAt: now.Add(-time.Hour), Deadline: now.Add(-55 * time.Minute)},
			false, false,
		},
		{
			"a record that says the table was taken down is not restored",
			&Applied{AppliedAt: now.Add(-time.Hour), ConfirmedAt: now.Add(-time.Hour), RemovedAt: now.Add(-time.Minute)},
			false, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			apply, armTimer := tc.applied.Restore(now)
			if apply != tc.apply || armTimer != tc.armTimer {
				t.Errorf("Restore = (%v, %v), want (%v, %v)", apply, armTimer, tc.apply, tc.armTimer)
			}
		})
	}
}
