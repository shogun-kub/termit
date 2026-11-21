package agent

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
)

const (
	// DefaultSyncInterval is the fallback cadence when the server sends no policy: one cycle a day.
	DefaultSyncInterval = 24 * time.Hour
	// DefaultMinSyncDelay is the design's "never earlier than 2 h after enrollment".
	DefaultMinSyncDelay = 2 * time.Hour
	// maxJitter spreads the fleet's cycles. The server already spreads its own schedule; this is the
	// node's own drift, so a group that enrolled together does not stay synchronised for life.
	maxJitter = 10 * time.Minute
	// meshPoll is how often a node that is not due yet asks the server whether the mesh changed. It is
	// one unauthenticated read of /healthz, and it is what makes a join reach the other nodes in
	// minutes instead of at their next scheduled sync.
	meshPoll = 5 * time.Minute
	// notEnrolledPoll is how often an installed but unenrolled node looks for credentials.
	notEnrolledPoll = time.Minute
)

// Backoff is how long the loop waits after a cycle that could not reach the server or apply its
// answer. It doubles from five minutes to an hour: long enough not to hammer a downed server,
// short enough that the mesh converges soon after it returns. Traffic already on the box is never
// touched by a failed cycle.
func Backoff(failures int) time.Duration {
	const base = 5 * time.Minute
	if failures < 1 {
		failures = 1
	}
	if failures > 8 {
		failures = 8
	}
	wait := base << (failures - 1)
	if wait > time.Hour {
		wait = time.Hour
	}
	return wait
}

// NextDue is when the node's next cycle may run. The server's next_sync_after is the authority; the
// policy and the local interval only fill it in when the server sends nothing, and the enrollment
// floor is applied last so an early answer cannot start a cycle the design forbids.
func NextDue(now time.Time, answer proto.SyncResponse, enrolledAt time.Time, seed string) time.Time {
	next := answer.NextSyncAfter
	if next.IsZero() {
		interval := time.Duration(answer.Policy.SyncIntervalSeconds) * time.Second
		if interval <= 0 {
			interval = DefaultSyncInterval
		}
		next = now.Add(interval)
	}
	minDelay := time.Duration(answer.Policy.MinSyncDelaySeconds) * time.Second
	if minDelay <= 0 {
		minDelay = DefaultMinSyncDelay
	}
	if !enrolledAt.IsZero() {
		if floor := enrolledAt.Add(minDelay); next.Before(floor) {
			next = floor
		}
	}
	if next.Before(now) {
		next = now
	}
	return next.Add(jitter(seed))
}

// jitter is derived from the node's identity, so it is stable across restarts and still differs per
// node: a hash, not a random number the node would have to persist.
func jitter(seed string) time.Duration {
	if seed == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	return time.Duration(h.Sum64()%uint64(maxJitter/time.Second)) * time.Second
}

// ErrNotEnrolled means the node has no credentials yet; the loop waits instead of failing.
var ErrNotEnrolled = errors.New("agent: this node is not enrolled")

// CycleReport is what one loop iteration did, for the log and for tests.
type CycleReport struct {
	At     time.Time
	Result string
	Answer proto.SyncResponse
	Check  state.SelfCheckReport
	Apply  Result
}

// Loop is the long-running agent: one cycle at the time the server asked for, with its answer as the
// authority on both the peer set and when the next cycle is due. Between cycles it wakes on the mesh
// poll and reconciles early when the digest says the mesh it applied is no longer the mesh the server
// holds — that is what makes a join land in minutes instead of at the next scheduled sync.
type Loop struct {
	Remote    Remote
	Logger    *slog.Logger
	StatePath string
	// CertPath is the leaf certificate the node serves. The loop reports its fingerprint on every
	// sync so the server's probe pin follows a renewal. Empty disables the report.
	CertPath string
	// Now and Sleep are seams for tests; production leaves them nil.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) bool
	// Check runs the local self-check. A nil Check means the report is omitted, which an operator
	// command wants and the service does not.
	Check func(ctx context.Context, st *state.State) state.SelfCheckReport
	// Apply reconciles the core with the answer. Required.
	Apply func(ctx context.Context, st *state.State, answer proto.SyncResponse) (Result, error)
	// AfterSync is the last act of a cycle that applied an answer, and it runs after the state was
	// saved: the owner's page uses it to list the mesh that was just reconciled. A failure is logged
	// and never fails the cycle — the core is already correct, and a page is a convenience.
	AfterSync func(ctx context.Context) error
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now().UTC()
	}
	return time.Now().UTC()
}

func (l *Loop) logger() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Run cycles until the context is cancelled. A failed cycle is logged and retried with backoff; it
// never stops the loop and never changes the core. Between the cycles the loop wakes on the mesh poll
// and reconciles early when the server reports a mesh it has not applied yet.
func (l *Loop) Run(ctx context.Context) error {
	for {
		if !l.sleep(ctx, l.untilNext(ctx)) {
			return ctx.Err()
		}
		if !l.due() && !l.meshChanged(ctx) {
			continue
		}
		// The cycle owns the lock, not the sleep: a manual `termit-uv sync` must not wait on a node that is
		// merely waiting for tomorrow.
		report, err := l.onceLocked(ctx)
		if err != nil && ctx.Err() == nil {
			l.logger().Warn("agent cycle failed", "result", report.Result, "err", err)
		}
	}
}

// due reports whether the schedule the server handed out says the node may sync now.
func (l *Loop) due() bool {
	st, err := state.LoadInstalled(l.StatePath)
	if err != nil || st.Credentials == nil || st.Credentials.AgentKey == "" {
		return false
	}
	next := st.Schedule.NextSyncAt
	return next.IsZero() || !l.now().Before(next)
}

// meshChanged polls the server's mesh digest and reports whether it differs from the one this node
// last applied. A digest it cannot read changes nothing: the scheduled cycle will report the outage
// with the backoff it deserves.
func (l *Loop) meshChanged(ctx context.Context) bool {
	st, err := state.LoadInstalled(l.StatePath)
	if err != nil || st.Credentials == nil || st.Credentials.AgentKey == "" {
		return false
	}
	generation, err := l.Remote.Mesh(ctx)
	if err != nil {
		l.logger().Debug("agent: mesh poll", "err", err)
		return false
	}
	if generation == "" || generation == st.MeshGeneration {
		return false
	}
	// Remembered before the sync: a sync that fails is retried by the schedule, not by every poll.
	st.MeshGeneration = generation
	if err := st.Save(l.StatePath); err != nil {
		l.logger().Warn("agent: remember the mesh generation", "err", err)
		return false
	}
	l.logger().Info("mesh changed", "generation", generation)
	return true
}

// untilNext reads the persisted schedule. It never sleeps past the poll interval, so a join, an
// enrollment or an operator action is noticed without waiting for the whole interval.
func (l *Loop) untilNext(ctx context.Context) time.Duration {
	st, err := state.LoadInstalled(l.StatePath)
	if err != nil {
		// Polling silently here would hide a state file the agent cannot read: the loop would look
		// healthy and never sync.
		l.logger().Warn("agent: cannot read the state, polling", "err", err)
		return notEnrolledPoll
	}
	if st.Credentials == nil || st.Credentials.AgentKey == "" {
		return notEnrolledPoll
	}
	now := l.now()
	next := st.Schedule.NextSyncAt
	if next.IsZero() {
		// A state written before this build has no schedule. Start a normal interval from now.
		return 0
	}
	wait := next.Sub(now)
	if wait <= 0 {
		return 0
	}
	if wait > meshPoll {
		wait = meshPoll
	}
	return wait
}

// Once runs one cycle without waiting. It takes the reconciliation lock and is what a test drives.
func (l *Loop) Once(ctx context.Context) (CycleReport, error) {
	unlock, err := state.Lock(l.StatePath + ".lock")
	if err != nil {
		return CycleReport{At: l.now(), Result: "skipped"}, err
	}
	defer unlock()
	return l.cycle(ctx)
}

func (l *Loop) onceLocked(ctx context.Context) (CycleReport, error) {
	unlock, err := state.Lock(l.StatePath + ".lock")
	if errors.Is(err, state.ErrLocked) {
		// Another process is syncing; it will have written the schedule when it is done.
		return CycleReport{At: l.now(), Result: "skipped"}, nil
	}
	if err != nil {
		return CycleReport{At: l.now(), Result: "failed"}, err
	}
	defer unlock()
	return l.cycle(ctx)
}

func (l *Loop) cycle(ctx context.Context) (CycleReport, error) {
	now := l.now()
	report := CycleReport{At: now, Result: "failed"}

	if l.Apply == nil {
		return report, errors.New("agent: the loop has no applier")
	}
	st, err := state.LoadInstalled(l.StatePath)
	if err != nil {
		if errors.Is(err, state.ErrNotInstalled) {
			return report, ErrNotEnrolled
		}
		return report, err
	}
	if st.Credentials == nil || st.Credentials.AgentKey == "" {
		return report, ErrNotEnrolled
	}

	if l.Check != nil {
		report.Check = l.Check(ctx, st)
		st.LastSelfCheck = &report.Check
	}

	request := proto.SyncRequest{AgentVersion: version.Agent("termit-uv")}
	if l.Check != nil {
		request.Check = checkReport(report.Check)
	}
	// The pin is read afresh every cycle: the renewal timer may have replaced the certificate since
	// the last one, and a server still pinning the previous leaf would fail the next check.
	remote := l.Remote
	remote.CertPin = CertFingerprint(l.CertPath)
	answer, err := remote.Fetch(ctx, st.Credentials.AgentKey, request)
	if err != nil {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		// The core is left exactly as it was: a server we cannot reach is not a reason to change
		// the peer set, and the traffic already on the box keeps flowing.
		st.Schedule.Failures++
		st.Schedule.LastSyncAt = now
		st.Schedule.LastResult = "failed"
		st.Schedule.NextSyncAt = now.Add(Backoff(st.Schedule.Failures))
		var refusal *Refusal
		if errors.As(err, &refusal) {
			// The server answered; it just said no. Retrying every five minutes would be noise.
			st.Schedule.Failures = 0
			st.Schedule.LastResult = "refused"
			st.Schedule.NextSyncAt = now.Add(DefaultSyncInterval + jitter(st.Identity.SelfUUID))
		}
		if saveErr := st.Save(l.StatePath); saveErr != nil {
			return report, errors.Join(err, saveErr)
		}
		return report, err
	}

	report.Answer = answer
	applied, err := l.Apply(ctx, st, answer)
	if err != nil {
		st.Schedule.Failures++
		st.Schedule.LastSyncAt = now
		st.Schedule.LastResult = "failed"
		st.Schedule.NextSyncAt = now.Add(Backoff(st.Schedule.Failures))
		if saveErr := st.Save(l.StatePath); saveErr != nil {
			return report, errors.Join(err, saveErr)
		}
		return report, err
	}
	report.Apply = applied
	report.Result = "ok"

	st.Peers = PeersFrom(answer)
	if answer.MeshGeneration != "" {
		// The answer names the mesh it described; remembering it is what keeps the poll from syncing
		// again for a change this cycle already applied.
		st.MeshGeneration = answer.MeshGeneration
	}
	RememberSubscription(st, answer)
	RememberProbe(st, answer)
	st.Schedule.Failures = 0
	st.Schedule.LastSyncAt = now
	st.Schedule.LastResult = "ok"
	enrolledAt := time.Time{}
	if st.Credentials != nil {
		enrolledAt = st.Credentials.EnrolledAt
	}
	st.Schedule.NextSyncAt = NextDue(now, answer, enrolledAt, st.Identity.SelfUUID)
	st.Touch()
	if err := st.Save(l.StatePath); err != nil {
		return report, err
	}
	changed := !applied.Unchanged || len(applied.Added) > 0 || len(applied.Removed) > 0
	l.logger().Info("sync complete",
		"peers", len(answer.Peers), "clients", applied.Clients, "changed", changed,
		"next", st.Schedule.NextSyncAt.Format(time.RFC3339))
	if l.AfterSync != nil {
		if err := l.AfterSync(ctx); err != nil {
			l.logger().Warn("agent: after the sync", "err", err)
		}
	}
	return report, nil
}

// sleep waits, with the context as the only way out. The seam exists so tests never sleep.
func (l *Loop) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	if l.Sleep != nil {
		return l.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// checkReport is the wire projection of the local self-check. The report stays a boolean plus one
// class on purpose: an old server must accept the body, and the detail belongs in `termit-uv status`.
func checkReport(report state.SelfCheckReport) *proto.CheckReport {
	return &proto.CheckReport{
		OK:         report.OK,
		ErrorClass: report.ErrorClass(),
		LatencyMS:  report.LatencyMS,
		At:         report.At,
	}
}

// PeersFrom stores what the server said, so `termit-uv status` can report the peer set without a network
// call and the loop can persist it with the schedule.
func PeersFrom(answer proto.SyncResponse) []state.Peer {
	out := make([]state.Peer, 0, len(answer.Peers))
	for _, peer := range answer.Peers {
		out = append(out, state.Peer{
			ID:       peer.ID,
			Domain:   peer.Domain,
			Label:    peer.Label,
			UUID:     peer.UUID,
			Path:     peer.Path,
			State:    peer.State,
			Degraded: peer.Degraded,
		})
	}
	return out
}

// ScheduleNote renders the persisted schedule for a log line or a status field, without a zero time.
func ScheduleNote(s state.Schedule) string {
	if s.NextSyncAt.IsZero() {
		return "not planned"
	}
	if s.Failures > 0 {
		return fmt.Sprintf("%s after %d failed cycle(s)", s.NextSyncAt.Format(time.RFC3339), s.Failures)
	}
	return s.NextSyncAt.Format(time.RFC3339)
}
