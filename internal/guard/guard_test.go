package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// fakeNft answers the two kernel reads the guard makes and records every write, so a test can assert
// both the decision and the set it landed in.
type fakeNft struct {
	table   bool
	sets    map[string][]string // set name -> element values
	writes  []string
	now     *time.Time
	failAdd bool
}

func (f *fakeNft) Run(_ context.Context, name string, args []string, stdin string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	// Records the nft arguments, not the binary: the assertions below read as nft syntax.
	call := strings.Join(args, " ")
	switch {
	case strings.Contains(line, "-j list tables"):
		if !f.table {
			return `{"nftables": [{"metainfo": {"version": "1.1.3"}}]}`, nil
		}
		return `{"nftables": [{"table": {"family": "inet", "name": "termit_uv"}}]}`, nil
	case strings.Contains(line, "-j list set"):
		set := args[len(args)-1]
		payload := map[string]any{"nftables": []any{}}
		elements := make([]any, 0, len(f.sets[set]))
		for _, value := range f.sets[set] {
			elements = append(elements, map[string]any{"elem": map[string]any{"val": value, "expires": 600}})
		}
		payload["nftables"] = []any{map[string]any{"set": map[string]any{"name": set, "elem": elements}}}
		data, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(data), nil
	case strings.Contains(line, "add element"):
		if f.failAdd {
			return "", fmt.Errorf("nft: no such table")
		}
		f.writes = append(f.writes, call)
		return "", nil
	case strings.Contains(line, "delete element"):
		f.writes = append(f.writes, call)
		return "", nil
	}
	return "", nil
}

// fixture is one guard wired to a fake kernel, a fake clock and a temporary access log.
type fixture struct {
	guard    *Guard
	kernel   *fakeNft
	bansPath string
	logPath  string
	now      *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	f := &fixture{
		kernel:   &fakeNft{table: true, sets: map[string][]string{}, now: &clock},
		bansPath: filepath.Join(dir, "bans.json"),
		logPath:  filepath.Join(dir, "access.log"),
		now:      &clock,
	}
	engine, err := New(Settings{
		Table:     firewall.NodeTable,
		BansPath:  f.bansPath,
		AccessLog: f.logPath,
		Ladder: firewall.Ladder{
			Window:  24 * time.Hour,
			Scan:    firewall.Escalation{First: time.Hour, Repeat: 24 * time.Hour},
			Path404: firewall.Escalation{First: 5 * time.Minute, Repeat: time.Hour, Double: true},
		},
		Failures: Failures{Attempts: 10, Window: 5 * time.Minute},
		Now:      func() time.Time { return clock },
	}, firewall.Applier{Runner: f.kernel})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.guard = engine
	return f
}

// cycle runs one pass and fails the test when it errors.
func (f *fixture) cycle(t *testing.T) {
	t.Helper()
	if err := f.guard.Cycle(context.Background()); err != nil {
		t.Fatalf("Cycle: %v", err)
	}
}

// ban reads the record the guard wrote.
func (f *fixture) ban(t *testing.T, ip string) (firewall.Ban, bool) {
	t.Helper()
	bans, err := firewall.LoadBans(f.bansPath)
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	return bans.Get(ip)
}

// advance moves the fake clock.
func (f *fixture) advance(d time.Duration) { *f.now = f.now.Add(d) }

// TestScanBanAndEscalation walks the ladder the way the kernel would: the same source appears in the
// scan set, serves its ban, comes back, and comes back again.
func TestScanBanAndEscalation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.kernel.sets["scan4"] = []string{"203.0.113.7"}
	f.cycle(t)

	ban, found := f.ban(t, "203.0.113.7")
	if !found {
		t.Fatal("the scan was not recorded")
	}
	if ban.Signal != firewall.SignalScan || ban.Count != 1 {
		t.Errorf("ban = %+v", ban)
	}
	if got := ban.Remaining(*f.now); got < 59*time.Minute || got > 61*time.Minute {
		t.Errorf("the first ban is %s, want about an hour", got)
	}
	if !containsWrite(f.kernel.writes, "add element inet termit_uv banned4 { 203.0.113.7 timeout 3600s }") {
		t.Errorf("the kernel was not told: %v", f.kernel.writes)
	}

	// The element stays in the kernel for its whole TTL. Each pass must not escalate on it again.
	f.cycle(t)
	f.cycle(t)
	if ban, _ := f.ban(t, "203.0.113.7"); ban.Count != 1 {
		t.Errorf("one scan was counted %d times", ban.Count)
	}

	// The ban expires and the source scans again: the second rung is the repeat, 24 hours.
	f.advance(90 * time.Minute)
	f.cycle(t)
	ban, _ = f.ban(t, "203.0.113.7")
	if ban.Count != 2 || ban.Offender {
		t.Fatalf("after the second scan: %+v", ban)
	}
	if got := ban.Remaining(*f.now); got < 23*time.Hour || got > 25*time.Hour {
		t.Errorf("the repeat ban is %s, want about 24h", got)
	}

	// A third offence earns no longer ban, so the ladder is exhausted and the operator is told.
	f.advance(25 * time.Hour)
	f.cycle(t)
	ban, _ = f.ban(t, "203.0.113.7")
	if !ban.Offender {
		t.Errorf("a third scan was not marked an offender: %+v", ban)
	}
	if got := ban.Remaining(*f.now); got < 23*time.Hour || got > 25*time.Hour {
		t.Errorf("the third ban is %s, want the 24h repeat", got)
	}
}

// TestScanOutsideTheWindowStartsOver: a source that comes back after the ladder window is a first
// offence again, not a repeat.
func TestScanOutsideTheWindowStartsOver(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.kernel.sets["scan4"] = []string{"203.0.113.7"}
	f.cycle(t)
	f.advance(48 * time.Hour)
	f.cycle(t)
	ban, _ := f.ban(t, "203.0.113.7")
	if ban.Count != 1 {
		t.Errorf("a scan two days later counted as offence %d", ban.Count)
	}
	if got := ban.Remaining(*f.now); got > 2*time.Hour {
		t.Errorf("a scan two days later earned %s, want the first rung again", got)
	}
}

// TestNeverBanSourcesAreIgnored: loopback and the private ranges are never banned, whatever the kernel
// reports.
func TestNeverBanSourcesAreIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.kernel.sets["scan4"] = []string{"127.0.0.1", "192.168.1.5", "10.1.2.3", "203.0.113.7"}
	f.cycle(t)
	for _, ip := range []string{"127.0.0.1", "192.168.1.5", "10.1.2.3"} {
		if _, found := f.ban(t, ip); found {
			t.Errorf("%s was banned, which would be self-inflicted", ip)
		}
	}
	if _, found := f.ban(t, "203.0.113.7"); !found {
		t.Error("the public address was not banned")
	}
}

// TestFailedRequestsWalkTheLadder: ten 4xx answers in the window earn five minutes, and each repeat
// doubles up to the cap.
func TestFailedRequestsWalkTheLadder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const source = "198.51.100.9"
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour}
	for i, expected := range want {
		f.appendFailures(t, source, 10, 404)
		f.cycle(t)
		ban, found := f.ban(t, source)
		if !found {
			t.Fatalf("round %d: no ban", i+1)
		}
		if got := ban.Remaining(*f.now); got != expected {
			t.Fatalf("round %d: ban = %s, want %s", i+1, got, expected)
		}
		if ban.Signal != firewall.SignalPath404 {
			t.Fatalf("round %d: signal = %s", i+1, ban.Signal)
		}
		// Serve the ban, then come back.
		f.advance(expected + time.Minute)
	}
	// One more round: the cap again, and now the ladder is exhausted.
	f.appendFailures(t, source, 10, 404)
	f.cycle(t)
	ban, _ := f.ban(t, source)
	if got := ban.Remaining(*f.now); got != time.Hour {
		t.Errorf("past the cap the ban is %s, want one hour", got)
	}
	if !ban.Offender {
		t.Errorf("a source past the cap is not marked an offender: %+v", ban)
	}
}

// TestSuccessDoesNotCount: only 4xx answers move the counter.
func TestSuccessDoesNotCount(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.appendFailures(t, "198.51.100.9", 30, 200)
	f.cycle(t)
	if _, found := f.ban(t, "198.51.100.9"); found {
		t.Error("successful requests earned a ban")
	}
}

// TestFailuresOutsideTheWindowDoNotAddUp: the counter is a window, not a lifetime total.
func TestFailuresOutsideTheWindowDoNotAddUp(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.appendFailures(t, "198.51.100.9", 9, 404)
	f.cycle(t)
	f.advance(10 * time.Minute)
	f.appendFailures(t, "198.51.100.9", 9, 404)
	f.cycle(t)
	if _, found := f.ban(t, "198.51.100.9"); found {
		t.Error("nine failures, a pause, then nine more earned a ban; the window was not applied")
	}
}

// TestLogRotationStartsOver: a log that got smaller was rotated, and the guard reads the new one from
// the beginning instead of believing its old offset.
func TestLogRotationStartsOver(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.appendFailures(t, "198.51.100.9", 9, 404)
	f.cycle(t)
	// Rotation: the new file is shorter than what was read before.
	if err := os.WriteFile(f.logPath, []byte(""), 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.appendFailures(t, "198.51.100.9", 10, 404)
	f.cycle(t)
	if _, found := f.ban(t, "198.51.100.9"); !found {
		t.Error("the guard did not read the rotated log")
	}
}

// TestRebootRebuildsTheKernelFromTheFile is the persistence promise: the kernel comes back empty, the
// file does not, and one pass puts the bans back.
func TestRebootRebuildsTheKernelFromTheFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	bans := &firewall.Bans{SchemaVersion: firewall.BansSchemaVersion, Bans: []firewall.Ban{
		{IP: "203.0.113.7", Signal: firewall.SignalScan, Count: 1, Last: *f.now, Until: f.now.Add(30 * time.Minute)},
		{IP: "2001:db8::7", Signal: firewall.SignalScan, Count: 1, Last: *f.now, Until: f.now.Add(30 * time.Minute)},
		{IP: "203.0.113.8", Signal: firewall.SignalOperator, Count: 1, Last: *f.now},
	}}
	if err := firewall.SaveBans(f.bansPath, bans); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}
	f.cycle(t)
	for _, want := range []string{
		"add element inet termit_uv banned4 { 203.0.113.7 timeout 1800s }",
		"add element inet termit_uv banned6 { 2001:db8::7 timeout 1800s }",
		"add element inet termit_uv banned4 { 203.0.113.8 }",
	} {
		if !containsWrite(f.kernel.writes, want) {
			t.Errorf("missing %q in %v", want, f.kernel.writes)
		}
	}
}

// TestExpiredBansLeaveTheKernel: an element the file no longer considers active is removed, so the
// kernel and the record cannot drift apart.
func TestExpiredBansLeaveTheKernel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.kernel.sets["banned4"] = []string{"203.0.113.99"}
	f.cycle(t)
	if !containsWrite(f.kernel.writes, "delete element inet termit_uv banned4 { 203.0.113.99 }") {
		t.Errorf("an element with no record stayed: %v", f.kernel.writes)
	}
}

// TestNoTableIsNoWork: with no table applied the guard reads nothing, writes nothing and creates no
// record. This is what makes it safe to ship enabled on a node whose operator never ran `firewall on`.
func TestNoTableIsNoWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.kernel.table = false
	f.kernel.sets["scan4"] = []string{"203.0.113.7"}
	f.cycle(t)
	if len(f.kernel.writes) != 0 {
		t.Errorf("the guard wrote to the kernel with no table: %v", f.kernel.writes)
	}
	if _, err := os.Stat(f.bansPath); !os.IsNotExist(err) {
		t.Errorf("the guard created a record with no table: %v", err)
	}
}

// TestOperatorBanIsNotEscalated: a permanent ban is the operator's, and an automatic signal does not
// replace or shorten it.
func TestOperatorBanIsNotEscalated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	bans := &firewall.Bans{SchemaVersion: firewall.BansSchemaVersion, Bans: []firewall.Ban{
		{IP: "203.0.113.7", Signal: firewall.SignalOperator, Count: 1, Last: *f.now},
	}}
	if err := firewall.SaveBans(f.bansPath, bans); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}
	f.kernel.sets["scan4"] = []string{"203.0.113.7"}
	f.cycle(t)
	ban, _ := f.ban(t, "203.0.113.7")
	if !ban.Permanent() || ban.Signal != firewall.SignalOperator {
		t.Errorf("the operator's ban was replaced: %+v", ban)
	}
}

// TestParseAccessLine reads nginx's combined format, including a user agent full of spaces.
func TestParseAccessLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line   string
		source string
		status int
		ok     bool
	}{
		{`203.0.113.7 - - [15/Nov/2026:12:00:00 +0000] "GET /a HTTP/1.1" 404 153 "-" "curl/8"`, "203.0.113.7", 404, true},
		{`2001:db8::7 - - [15/Nov/2026:12:00:00 +0000] "GET / HTTP/2.0" 200 5 "-" "Mozilla/5.0 (X11; Linux x86_64)"`, "2001:db8::7", 200, true},
		{`198.51.100.9 - user [15/Nov/2026:12:00:00 +0000] "POST /x?a=1 HTTP/1.1" 403 0 "https://example.com/a b" "ua with spaces"`, "198.51.100.9", 403, true},
		{"garbage", "", 0, false},
		{`203.0.113.7 - - [15/Nov/2026:12:00:00 +0000] "GET /a HTTP/1.1"`, "", 0, false},
	}
	for _, tc := range cases {
		source, status, ok := parseAccessLine(tc.line)
		if ok != tc.ok || source != tc.source || status != tc.status {
			t.Errorf("parseAccessLine(%q) = %q, %d, %v; want %q, %d, %v",
				tc.line, source, status, ok, tc.source, tc.status, tc.ok)
		}
	}
}

// appendFailures writes lines to the access log.
func (f *fixture) appendFailures(t *testing.T, source string, count, status int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&b, `%s - - [15/Nov/2026:12:00:00 +0000] "GET /probe%d HTTP/1.1" %d 10 "-" "test"`+"\n",
			source, i, status)
	}
	file, err := os.OpenFile(f.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open the log: %v", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(b.String()); err != nil {
		t.Fatalf("write the log: %v", err)
	}
}

// containsWrite reports whether one recorded nft call matches.
func containsWrite(writes []string, want string) bool {
	for _, write := range writes {
		if write == want {
			return true
		}
	}
	return false
}
