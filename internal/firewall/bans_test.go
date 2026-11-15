package firewall

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ladder is the default policy the configuration produces.
func ladder() Ladder {
	return Ladder{
		Window:  24 * time.Hour,
		Scan:    Escalation{First: time.Hour, Repeat: 24 * time.Hour},
		Path404: Escalation{First: 5 * time.Minute, Repeat: time.Hour, Double: true},
	}
}

// TestEscalationBan pins the arithmetic of both ladders, including the cap.
func TestEscalationBan(t *testing.T) {
	t.Parallel()
	scan := ladder().Scan
	for n, want := range map[int]time.Duration{1: time.Hour, 2: 24 * time.Hour, 9: 24 * time.Hour} {
		if got := scan.Ban(n); got != want {
			t.Errorf("scan.Ban(%d) = %s, want %s", n, got, want)
		}
	}
	failed := ladder().Path404
	want := []time.Duration{
		5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute,
		time.Hour, time.Hour, time.Hour,
	}
	for n, expected := range want {
		if got := failed.Ban(n + 1); got != expected {
			t.Errorf("path404.Ban(%d) = %s, want %s", n+1, got, expected)
		}
	}
}

// TestDecideWalksTheLadder is the same walk through Decide, where the window and the offender flag
// live.
func TestDecideWalksTheLadder(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		signal   Signal
		previous Ban
		now      time.Time
		wantLen  time.Duration
		wantSeq  int
		offender bool
	}{
		{
			name: "first scan", signal: SignalScan, now: start,
			wantLen: time.Hour, wantSeq: 1,
		},
		{
			name: "second scan right after the ban", signal: SignalScan,
			previous: Ban{Signal: SignalScan, Count: 1, Last: start, Until: start.Add(time.Hour), LastBanSeconds: 3600},
			now:      start.Add(2 * time.Hour), wantLen: 24 * time.Hour, wantSeq: 2,
		},
		{
			name: "third scan, the ladder is exhausted", signal: SignalScan,
			previous: Ban{Signal: SignalScan, Count: 2, Last: start, Until: start.Add(25 * time.Hour), LastBanSeconds: 86400},
			now:      start.Add(26 * time.Hour), wantLen: 24 * time.Hour, wantSeq: 3, offender: true,
		},
		{
			name: "a scan two days after being unbanned starts over", signal: SignalScan,
			previous: Ban{Signal: SignalScan, Count: 3, Last: start, Until: start.Add(24 * time.Hour), LastBanSeconds: 86400},
			now:      start.Add(24*time.Hour + 48*time.Hour), wantLen: time.Hour, wantSeq: 1,
		},
		{
			name: "first failed run", signal: SignalPath404, now: start,
			wantLen: 5 * time.Minute, wantSeq: 1,
		},
		{
			name: "second failed run doubles", signal: SignalPath404,
			previous: Ban{Signal: SignalPath404, Count: 1, Last: start, Until: start.Add(5 * time.Minute), LastBanSeconds: 300},
			now:      start.Add(10 * time.Minute), wantLen: 10 * time.Minute, wantSeq: 2,
		},
		{
			name: "past the cap, the ladder is exhausted", signal: SignalPath404,
			previous: Ban{Signal: SignalPath404, Count: 5, Last: start, Until: start.Add(time.Hour), LastBanSeconds: 3600},
			now:      start.Add(2 * time.Hour), wantLen: time.Hour, wantSeq: 6, offender: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next, offender := ladder().Decide(tc.signal, tc.previous, tc.now)
			if got := next.Until.Sub(tc.now); got != tc.wantLen {
				t.Errorf("ban length = %s, want %s", got, tc.wantLen)
			}
			if next.Count != tc.wantSeq {
				t.Errorf("count = %d, want %d", next.Count, tc.wantSeq)
			}
			if offender != tc.offender {
				t.Errorf("offender = %v, want %v", offender, tc.offender)
			}
			if next.Signal != tc.signal {
				t.Errorf("signal = %q", next.Signal)
			}
		})
	}
}

// TestLadderRefusesNonsense pins the validation the configuration relies on.
func TestLadderRefusesNonsense(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Ladder){
		"no window":               func(l *Ladder) { l.Window = 0 },
		"no first ban":            func(l *Ladder) { l.Scan.First = 0 },
		"repeat shorter":          func(l *Ladder) { l.Scan.Repeat = time.Minute },
		"path404 repeat shorter":  func(l *Ladder) { l.Path404.Repeat = time.Second },
		"negative path404 window": func(l *Ladder) { l.Path404.First = -time.Minute },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l := ladder()
			mutate(&l)
			if err := l.Validate(); err == nil {
				t.Error("the ladder was accepted")
			}
		})
	}
	if err := ladder().Validate(); err != nil {
		t.Errorf("the default ladder is refused: %v", err)
	}
}

// TestBansFileSurvivesARoundTrip: the guard writes it, the commands read it and the guard reads it
// back, so the shape has to be stable.
func TestBansFileSurvivesARoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bans.json")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	bans := &Bans{SchemaVersion: BansSchemaVersion, Bans: []Ban{
		{IP: "203.0.113.7", Signal: SignalScan, Count: 2, LastBanSeconds: 3600, Last: now, Until: now.Add(time.Hour)},
		{IP: "203.0.113.8", Signal: SignalOperator, Count: 1, Last: now},
	}}
	if err := SaveBans(path, bans); err != nil {
		t.Fatalf("SaveBans: %v", err)
	}
	back, err := LoadBans(path)
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	ban, found := back.Get("203.0.113.7")
	if !found || ban.Count != 2 || ban.LastBanSeconds != 3600 || !ban.Active(now) {
		t.Errorf("the scan ban came back as %+v", ban)
	}
	permanent, found := back.Get("203.0.113.8")
	if !found || !permanent.Permanent() {
		t.Errorf("the operator's ban came back as %+v", permanent)
	}
	if active := back.Active(now); len(active) != 2 || active[0].IP != "203.0.113.8" {
		t.Errorf("Active = %+v; the permanent ban belongs first", active)
	}
}

// TestLoadBansMissingIsEmpty: a node that never banned anything has no file, and that is not an error.
func TestLoadBansMissingIsEmpty(t *testing.T) {
	t.Parallel()
	bans, err := LoadBans(filepath.Join(t.TempDir(), "bans.json"))
	if err != nil {
		t.Fatalf("LoadBans: %v", err)
	}
	if len(bans.Bans) != 0 {
		t.Errorf("bans = %+v", bans.Bans)
	}
}

// TestLoadBansRefusesANewerSchema: silently reading a file a newer build wrote would lose fields.
func TestLoadBansRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bans.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 99, "bans": []}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadBans(path); err == nil {
		t.Error("a newer schema was accepted")
	}
}

// TestPruneForgetsWhatTheLadderNoLongerNeeds: an ended ban that nobody marked is dropped once the
// window has passed, and the permanent and offender records stay.
func TestPruneForgetsWhatTheLadderNoLongerNeeds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	bans := &Bans{Bans: []Ban{
		{IP: "203.0.113.1", Signal: SignalScan, Last: now.Add(-72 * time.Hour), Until: now.Add(-71 * time.Hour)},
		{IP: "203.0.113.2", Signal: SignalScan, Last: now.Add(-72 * time.Hour), Until: now.Add(-71 * time.Hour), Offender: true},
		{IP: "203.0.113.3", Signal: SignalOperator, Last: now.Add(-72 * time.Hour)},
		{IP: "203.0.113.4", Signal: SignalScan, Last: now, Until: now.Add(time.Hour)},
		{IP: "203.0.113.5", Signal: SignalScan, Last: now.Add(-2 * time.Hour), Until: now.Add(-time.Hour)},
	}}
	bans.Prune(now, 24*time.Hour)
	kept := map[string]bool{}
	for _, ban := range bans.Bans {
		kept[ban.IP] = true
	}
	for _, gone := range []string{"203.0.113.1"} {
		if kept[gone] {
			t.Errorf("%s survived the prune", gone)
		}
	}
	for _, stays := range []string{"203.0.113.2", "203.0.113.3", "203.0.113.4", "203.0.113.5"} {
		if !kept[stays] {
			t.Errorf("%s was pruned", stays)
		}
	}
}

// TestNeverBanCoversTheSelfInflicted: the built-in ranges and whatever the caller adds.
func TestNeverBanCoversTheSelfInflicted(t *testing.T) {
	t.Parallel()
	never := NewNeverBan(netip.MustParsePrefix("203.0.113.0/24"))
	for _, covered := range []string{"127.0.0.1", "10.0.0.1", "172.16.5.4", "192.168.1.1", "169.254.1.1", "::1", "fe80::1", "fd00::1", "203.0.113.7"} {
		if !never.CoversString(covered) {
			t.Errorf("%s is not protected", covered)
		}
	}
	for _, open := range []string{"8.8.8.8", "2001:db8::1", "1.1.1.1"} {
		if never.CoversString(open) {
			t.Errorf("%s is protected, which would make a ban impossible", open)
		}
	}
	if never.CoversString("not-an-address") {
		t.Error("a word is not an address")
	}
}
