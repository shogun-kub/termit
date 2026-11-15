// Package guard is the ban engine: a long-running loop that reads the kernel's scan set and the web
// server's access log, decides bans with the ladder, applies them to the firewall table and keeps
// every decision in bans.json.
//
// It owns no firewall shape of its own. The table is rendered and applied by `termit-uv firewall on`,
// and the guard only ever adds or removes elements inside it — the invariant from
// docs/DESCRIPTION.md §11: a firewall changes when an operator says so, never because a timer fired.
// If the table is absent the guard reads nothing and does nothing.
package guard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

const (
	// DefaultInterval is how often the loop looks. The kernel remembers a scan for minutes, so half a
	// minute is prompt without being busy; a failed request is counted from the log on the same pass.
	DefaultInterval = 30 * time.Second
	// maxLinesPerCycle bounds one pass over the access log. A log that grew larger than this between
	// two cycles is read in the next one instead of holding the cycle for minutes.
	maxLinesPerCycle = 20000
	// failureSources bounds the in-memory counter map, so a log full of distinct sources cannot grow it
	// without limit. Shortcut: past this many sources the oldest counters are dropped, which can only
	// make a ban late; upgrade to a bounded LRU if a node ever sees this much noise.
	failureSources = 50000
)

// Failures is the failed-request detector: how many 4xx answers from one source inside a window make a
// ban.
type Failures struct {
	Attempts int
	Window   time.Duration
}

// Settings is what the loop needs. It carries no Termit configuration type on purpose: the node and
// the server hand it the same facts under different paths.
type Settings struct {
	// Table is the firewall table to read and to ban in.
	Table string
	// BansPath is bans.json.
	BansPath string
	// AccessLog is the web server's log; empty turns the 4xx signal off.
	AccessLog string
	Ladder    firewall.Ladder
	Failures  Failures
	// Never returns the networks a ban may not touch beyond the built-in private ranges — the
	// management server's address, the operator's ssh_from. It is called once per cycle, because the
	// server's address can change.
	Never func(context.Context) []netip.Prefix
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Log is the logger; nil means slog.Default.
	Log *slog.Logger
	// Interval is the pause between cycles; zero means DefaultInterval.
	Interval time.Duration
}

// Guard is the loop. One is built per process and run until the context is cancelled.
type Guard struct {
	settings Settings
	applier  firewall.Applier

	// failures counts 4xx answers per source inside the window.
	failures map[string][]time.Time
	// logOffset is how far the access log has been read. A file shorter than it was truncated or
	// rotated, and reading starts over.
	logOffset int64
}

// New wires a guard to an applier. Both must be given; there is no meaningful default that could ban
// anything.
func New(settings Settings, applier firewall.Applier) (*Guard, error) {
	if settings.Table == "" {
		return nil, errors.New("guard: no table to guard")
	}
	if settings.BansPath == "" {
		return nil, errors.New("guard: no bans file")
	}
	if settings.Ladder.Window <= 0 {
		return nil, errors.New("guard: the ladder needs a window")
	}
	if applier.Runner == nil {
		return nil, errors.New("guard: no firewall runner")
	}
	if settings.Log == nil {
		settings.Log = slog.Default()
	}
	if settings.Now == nil {
		settings.Now = time.Now
	}
	if settings.Interval <= 0 {
		settings.Interval = DefaultInterval
	}
	return &Guard{settings: settings, applier: applier, failures: make(map[string][]time.Time)}, nil
}

// Run is the unit's command: a cycle every Interval until the context is cancelled. A cycle that fails
// is logged and retried on the next tick — the guard must survive a transient nft or log problem
// without losing its counters.
func (g *Guard) Run(ctx context.Context) error {
	ticker := time.NewTicker(g.settings.Interval)
	defer ticker.Stop()
	for {
		if err := g.Cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			g.settings.Log.Error("guard cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Cycle is one pass: read the signals, decide, write the record, reconcile the kernel. It is exported
// because `guard run --once` and the tests drive exactly this.
func (g *Guard) Cycle(ctx context.Context) error {
	now := g.settings.Now()
	bans, err := firewall.LoadBans(g.settings.BansPath)
	if err != nil {
		return err
	}
	present, err := g.applier.Present(ctx, g.settings.Table)
	if err != nil {
		return err
	}
	if !present {
		// No table, no signals: the operator has not applied one, and the guard does not invent it.
		return nil
	}
	never := firewall.NewNeverBan(g.neverPrefixes(ctx)...)

	if err := g.decideScans(ctx, bans, never, now); err != nil {
		return err
	}
	if err := g.decideFailures(ctx, bans, never, now); err != nil {
		return err
	}
	bans.Prune(now, g.settings.Ladder.Window)
	if err := g.reconcile(ctx, bans, now); err != nil {
		return err
	}
	return firewall.SaveBans(g.settings.BansPath, bans)
}

// neverPrefixes asks the settings for the extra protected networks.
func (g *Guard) neverPrefixes(ctx context.Context) []netip.Prefix {
	if g.settings.Never == nil {
		return nil
	}
	return g.settings.Never(ctx)
}

// decideScans turns the kernel's remembered scanners into bans.
func (g *Guard) decideScans(ctx context.Context, bans *firewall.Bans, never firewall.NeverBan, now time.Time) error {
	for _, set := range []string{"scan4", "scan6"} {
		elements, err := g.applier.Elements(ctx, g.settings.Table, set)
		if err != nil {
			return err
		}
		for _, element := range elements {
			addr, err := netip.ParseAddr(element.Value)
			if err != nil || addr.Is4In6() {
				continue
			}
			if never.Covers(addr) {
				continue
			}
			g.decide(bans, firewall.SignalScan, addr.String(), now)
		}
	}
	return nil
}

// decideFailures counts 4xx answers from the log and bans a source that keeps failing. The count lives
// in memory and survives a cycle, not a restart: a restarted guard asks for another window of failures,
// which is the safe direction.
func (g *Guard) decideFailures(ctx context.Context, bans *firewall.Bans, never firewall.NeverBan, now time.Time) error {
	if g.settings.AccessLog == "" || g.settings.Failures.Attempts < 2 {
		return nil
	}
	lines, err := g.newLogLines()
	if err != nil {
		return err
	}
	window := g.settings.Failures.Window
	for _, line := range lines {
		source, status, ok := parseAccessLine(line)
		if !ok || status < 400 || status > 499 {
			continue
		}
		addr, err := netip.ParseAddr(source)
		if err != nil || never.Covers(addr) {
			continue
		}
		key := addr.String()
		kept := make([]time.Time, 0, len(g.failures[key])+1)
		for _, at := range g.failures[key] {
			if now.Sub(at) <= window {
				kept = append(kept, at)
			}
		}
		kept = append(kept, now)
		if len(kept) >= g.settings.Failures.Attempts {
			g.decide(bans, firewall.SignalPath404, key, now)
			delete(g.failures, key)
			continue
		}
		g.failures[key] = kept
		if len(g.failures) > failureSources {
			g.dropOldestFailure()
		}
	}
	return nil
}

// dropOldestFailure forgets the counter whose last hit is oldest, which bounds the map without a
// per-source timer.
func (g *Guard) dropOldestFailure() {
	var oldestKey string
	var oldest time.Time
	for key, hits := range g.failures {
		if len(hits) == 0 {
			delete(g.failures, key)
			continue
		}
		if last := hits[len(hits)-1]; oldest.IsZero() || last.Before(oldest) {
			oldest, oldestKey = last, key
		}
	}
	if oldestKey != "" {
		delete(g.failures, oldestKey)
	}
}

// decide applies the ladder and records the result. A source already banned for this signal is left
// alone: the kernel's scan element stays for its whole TTL, and escalating on every cycle would read
// one scan as a dozen offences.
func (g *Guard) decide(bans *firewall.Bans, signal firewall.Signal, ip string, now time.Time) {
	previous, seen := bans.Get(ip)
	if seen && previous.Permanent() && previous.Active(now) {
		return
	}
	if seen && previous.Signal == signal && previous.Active(now) {
		return
	}
	next, offender := g.settings.Ladder.Decide(signal, previous, now)
	next.IP = ip
	bans.Put(next)
	g.settings.Log.Warn("banned a source",
		"ip", ip, "signal", string(signal), "count", next.Count,
		"until", next.Until.Format(time.RFC3339), "offender", offender)
}

// reconcile makes the kernel match the file: every active ban is an element with its remaining time, and
// an element the file does not know about is removed. This is what makes a reboot harmless — the kernel
// comes back empty and the file refills it.
func (g *Guard) reconcile(ctx context.Context, bans *firewall.Bans, now time.Time) error {
	wanted := map[string]time.Duration{}
	for _, ban := range bans.Active(now) {
		addr, err := netip.ParseAddr(ban.IP)
		if err != nil {
			continue
		}
		wanted[addr.String()] = ban.Remaining(now)
	}
	var problems []error
	for _, set := range []string{"banned4", "banned6"} {
		elements, err := g.applier.Elements(ctx, g.settings.Table, set)
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(elements))
		for _, element := range elements {
			present[element.Value] = true
			if _, ok := wanted[element.Value]; !ok {
				if err := g.applier.DeleteElement(ctx, g.settings.Table, set, element.Value); err != nil {
					problems = append(problems, err)
				}
			}
		}
		for ip, remaining := range wanted {
			addr, err := netip.ParseAddr(ip)
			if err != nil || banSet(addr) != set || present[ip] {
				continue
			}
			if err := g.applier.AddElement(ctx, g.settings.Table, set, ip, remaining); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return errors.Join(problems...)
}

// banSet is the set an address of this family belongs in; the firewall package renders the same split.
func banSet(addr netip.Addr) string {
	if addr.Is4() || addr.Is4In6() {
		return "banned4"
	}
	return "banned6"
}

// newLogLines returns the lines written since the last call. A file that got shorter was rotated or
// truncated, so reading starts from the beginning; a half-written last line is left for the next pass.
func (g *Guard) newLogLines() ([]string, error) {
	file, err := os.Open(g.settings.AccessLog) // #nosec G304 -- a path from the component's configuration.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("guard: open the access log: %w", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("guard: stat the access log: %w", err)
	}
	if info.Size() < g.logOffset {
		g.logOffset = 0
	}
	if _, err := file.Seek(g.logOffset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("guard: seek the access log: %w", err)
	}
	reader := bufio.NewReader(file)
	lines := make([]string, 0, 64)
	for len(lines) < maxLinesPerCycle {
		line, err := reader.ReadString('\n')
		if len(line) > 0 && strings.HasSuffix(line, "\n") {
			lines = append(lines, line)
			g.logOffset += int64(len(line))
		}
		if err != nil {
			break
		}
	}
	return lines, nil
}

// parseAccessLine reads nginx's combined format:
//
//	203.0.113.7 - - [15/Nov/2026:12:00:00 +0000] "GET /a HTTP/1.1" 404 153 "-" "curl/8"
//
// The request and the user agent are quoted, so the status is the first token after the request's
// closing quote: splitting on quotes survives a user agent full of spaces.
func parseAccessLine(line string) (source string, status int, ok bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", 0, false
	}
	source = fields[0]
	open := strings.Index(line, "\"")
	if open < 0 {
		return "", 0, false
	}
	shut := strings.Index(line[open+1:], "\"")
	if shut < 0 {
		return "", 0, false
	}
	rest := strings.Fields(line[open+1+shut+1:])
	if len(rest) == 0 {
		return "", 0, false
	}
	for _, r := range rest[0] {
		if r < '0' || r > '9' {
			return "", 0, false
		}
		status = status*10 + int(r-'0')
	}
	return source, status, true
}
