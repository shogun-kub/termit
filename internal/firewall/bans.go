package firewall

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/atomicfile"
)

// Signal is why a source is refused.
type Signal string

const (
	// SignalScan is a port scan the kernel's meter counted.
	SignalScan Signal = "scan"
	// SignalPath404 is a run of failed requests in the web server's access log.
	SignalPath404 Signal = "path404"
	// SignalOperator is an explicit `bans block`. It never expires on its own.
	SignalOperator Signal = "operator"
)

// BansSchemaVersion is the only shape of bans.json this build writes.
const BansSchemaVersion = 1

// Ban is one decision about one source. The fields the ladder needs are stored with it: the kernel's
// sets have a TTL and forget, so this file is the only memory of what was decided and how often.
type Ban struct {
	IP     string `json:"ip"`
	Signal Signal `json:"signal"`
	// Until is when the ban ends. The zero value means permanent, which only an operator can do.
	Until time.Time `json:"until,omitzero"`
	// Count is how many times this signal has come back from this source.
	Count int `json:"count,omitempty"`
	// LastBanSeconds is the length handed out last time, which is how the ladder knows it has run out
	// of rungs. It is seconds rather than a duration so the file stays readable.
	LastBanSeconds int64     `json:"last_ban_seconds,omitempty"`
	Last           time.Time `json:"last,omitzero"`
	// Offender marks a source the ladder could not punish harder: the operator's decision is next.
	Offender bool `json:"offender,omitempty"`
}

// Permanent reports whether the ban has no end.
func (b Ban) Permanent() bool { return b.Until.IsZero() }

// Active reports whether the source is banned now.
func (b Ban) Active(now time.Time) bool { return b.Permanent() || now.Before(b.Until) }

// Remaining is how long the ban has left; zero for a permanent one.
func (b Ban) Remaining(now time.Time) time.Duration {
	if b.Permanent() {
		return 0
	}
	if left := b.Until.Sub(now); left > 0 {
		return left
	}
	return 0
}

// Bans is bans.json: the guard's decisions, kept across restarts and reboots. The commands edit it too,
// and the guard re-reads it every cycle so `bans unban` takes effect on the next pass rather than at
// the next restart.
type Bans struct {
	SchemaVersion int   `json:"schema_version"`
	Bans          []Ban `json:"bans"`
}

// LoadBans reads the file. An absent one is an empty list: a node that never banned anything has
// nothing to say.
func LoadBans(path string) (*Bans, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path comes from the component's configuration.
	if errors.Is(err, os.ErrNotExist) {
		return &Bans{SchemaVersion: BansSchemaVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("firewall: read %s: %w", path, err)
	}
	bans := &Bans{}
	if err := json.Unmarshal(data, bans); err != nil {
		return nil, fmt.Errorf("firewall: %s: %w", path, err)
	}
	if bans.SchemaVersion > BansSchemaVersion {
		return nil, fmt.Errorf("firewall: %s was written by a newer build (schema %d)", path, bans.SchemaVersion)
	}
	bans.SchemaVersion = BansSchemaVersion
	return bans, nil
}

// SaveBans writes the file atomically, in a stable order so a diff means something.
func SaveBans(path string, bans *Bans) error {
	bans.SchemaVersion = BansSchemaVersion
	slices.SortFunc(bans.Bans, func(a, b Ban) int { return strings.Compare(a.IP, b.IP) })
	data, err := json.MarshalIndent(bans, "", "  ")
	if err != nil {
		return fmt.Errorf("firewall: encode the bans: %w", err)
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("firewall: write %s: %w", path, err)
	}
	return nil
}

// Get returns the record for one source.
func (b *Bans) Get(ip string) (Ban, bool) {
	for _, ban := range b.Bans {
		if ban.IP == ip {
			return ban, true
		}
	}
	return Ban{}, false
}

// Put stores the record, replacing the one for the same source.
func (b *Bans) Put(ban Ban) {
	for i := range b.Bans {
		if b.Bans[i].IP == ban.IP {
			b.Bans[i] = ban
			return
		}
	}
	b.Bans = append(b.Bans, ban)
}

// Delete removes the record and reports whether there was one.
func (b *Bans) Delete(ip string) bool {
	for i := range b.Bans {
		if b.Bans[i].IP == ip {
			b.Bans = append(b.Bans[:i], b.Bans[i+1:]...)
			return true
		}
	}
	return false
}

// Active lists the bans in force now, longest first, so a status line leads with what matters.
func (b *Bans) Active(now time.Time) []Ban {
	out := make([]Ban, 0, len(b.Bans))
	for _, ban := range b.Bans {
		if ban.Active(now) {
			out = append(out, ban)
		}
	}
	slices.SortFunc(out, func(a, c Ban) int {
		switch {
		case a.Permanent() != c.Permanent():
			if a.Permanent() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.IP, c.IP)
	})
	return out
}

// Offenders lists the sources the ladder gave up on: they kept coming back after the longest ban it
// has, and only the operator can decide what to do next.
func (b *Bans) Offenders(now time.Time) []Ban {
	out := make([]Ban, 0, len(b.Bans))
	for _, ban := range b.Bans {
		if ban.Offender {
			out = append(out, ban)
		}
	}
	slices.SortFunc(out, func(a, c Ban) int { return strings.Compare(a.IP, c.IP) })
	return out
}

// Prune forgets records the ladder no longer needs: a ban that ended long ago and that nobody marked
// an offender would otherwise keep the file growing for the life of the node.
func (b *Bans) Prune(now time.Time, window time.Duration) {
	kept := b.Bans[:0]
	for _, ban := range b.Bans {
		switch {
		case ban.Active(now), ban.Offender, ban.Signal == SignalOperator:
			kept = append(kept, ban)
		case window > 0 && now.Sub(ban.Until) > window:
			// The next attempt starts the ladder over, which is the point.
		default:
			kept = append(kept, ban)
		}
	}
	b.Bans = kept
}

// Escalation is one signal's ladder. The first offence earns First; a repeat inside the ladder window
// earns Repeat, either directly (a scan) or by doubling from First up to it (a run of failed requests).
type Escalation struct {
	First  time.Duration
	Repeat time.Duration
	Double bool
}

// Ban is the n-th ban for the same signal, n starting at 1.
func (e Escalation) Ban(n int) time.Duration {
	if n <= 1 {
		return e.First
	}
	if !e.Double {
		return e.Repeat
	}
	length := e.First
	for i := 1; i < n && length < e.Repeat; i++ {
		length *= 2
	}
	if length > e.Repeat {
		length = e.Repeat
	}
	return length
}

// Ladder is the whole policy: which signal earns what, and how long a repetition is remembered.
type Ladder struct {
	// Window is how long a repeat still counts as one. Past it, the next offence starts over.
	Window  time.Duration
	Scan    Escalation
	Path404 Escalation
}

// Escalation returns the ladder for a signal, and false for a signal that has none (the operator's).
func (l Ladder) Escalation(signal Signal) (Escalation, bool) {
	switch signal {
	case SignalScan:
		return l.Scan, true
	case SignalPath404:
		return l.Path404, true
	}
	return Escalation{}, false
}

// Decide is the ladder as a pure function: the record so far and the clock in, the next ban out.
//
// A repeat only escalates when the source comes back inside the window — and the window is measured
// from the end of the previous ban, not from its start. Otherwise a ban as long as the window would
// make every repeat a first offence: the source is let back in exactly when the memory expires, and a
// scanner that never stops would never be recognised as one. A source that comes back after the window
// has passed since it was unbanned is a first offence again.
//
// A repeat that earns no longer ban than the last one means the ladder is exhausted — that is what
// marks an offender, instead of a hard-coded "third time" that would drift from the configuration.
func (l Ladder) Decide(signal Signal, previous Ban, now time.Time) (Ban, bool) {
	escalation, ok := l.Escalation(signal)
	if !ok {
		return Ban{Signal: signal, Until: now, Count: 1}, false
	}
	count := previous.Count
	if count == 0 || previous.Last.IsZero() || now.Sub(releasedAt(previous)) > l.Window {
		count = 0
	}
	count++
	length := escalation.Ban(count)
	offender := count > 1 && length <= time.Duration(previous.LastBanSeconds)*time.Second
	return Ban{
		Signal:         signal,
		Until:          now.Add(length),
		Count:          count,
		LastBanSeconds: int64(length / time.Second),
		Last:           now,
		Offender:       offender,
	}, offender
}

// releasedAt is when the previous ban stopped applying: its end, or when it was decided for a ban that
// is already over.
func releasedAt(previous Ban) time.Time {
	if !previous.Until.IsZero() && previous.Until.After(previous.Last) {
		return previous.Until
	}
	return previous.Last
}

// Validate reports a ladder that cannot mean anything: a ban of zero, a repeat shorter than the first
// offence, a window that is not positive.
func (l Ladder) Validate() error {
	var problems []error
	if l.Window <= 0 {
		problems = append(problems, errors.New("ladder_window must be positive"))
	}
	for name, escalation := range map[string]Escalation{"scan": l.Scan, "path404": l.Path404} {
		if escalation.First <= 0 {
			problems = append(problems, fmt.Errorf("%s.ban must be positive", name))
		}
		if escalation.Repeat < escalation.First {
			problems = append(problems, fmt.Errorf("%s: the repeat (%s) is shorter than the first ban (%s)",
				name, escalation.Repeat, escalation.First))
		}
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	return nil
}

// NeverBan is the set a ban may not touch. loopback and the private ranges are there because banning
// them is banning yourself; link-local is there because it is not a source that can scan you, and the
// caller adds the management server and the operator's own networks.
type NeverBan struct {
	prefixes []netip.Prefix
}

// neverPrefixes are the built-in protected ranges.
var neverPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fc00::/7"),
}

// NewNeverBan builds the set with the caller's additions (the management server, ssh_from).
func NewNeverBan(extra ...netip.Prefix) NeverBan {
	prefixes := make([]netip.Prefix, 0, len(neverPrefixes)+len(extra))
	prefixes = append(prefixes, neverPrefixes...)
	for _, prefix := range extra {
		if prefix.IsValid() {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	return NeverBan{prefixes: prefixes}
}

// Covers reports whether a ban on addr would be self-inflicted.
func (n NeverBan) Covers(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range n.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// CoversString is Covers for an address as the tools see it in a set or on a command line.
func (n NeverBan) CoversString(ip string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	return n.Covers(addr)
}
