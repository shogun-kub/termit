package sched_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/mn/sched"
	"github.com/shogun-kub/termit/internal/mn/store"
)

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// scriptedProber answers with the results it was given, in order, and repeats the last one.
type scriptedProber struct {
	results []probe.Result
	calls   int
}

func (p *scriptedProber) Probe(_ context.Context, _ []probe.Target) ([]probe.Result, error) {
	if len(p.results) == 0 {
		return nil, errors.New("no scripted results")
	}
	index := p.calls
	if index >= len(p.results) {
		index = len(p.results) - 1
	}
	p.calls++
	return []probe.Result{p.results[index]}, nil
}

func okResult() probe.Result { return probe.Result{OK: true, Latency: 42 * time.Millisecond} }
func failResult() probe.Result {
	return probe.Result{Class: probe.ClassTimeout, Err: errors.New("timeout")}
}
func unknownResult() probe.Result {
	return probe.Result{Class: probe.ClassWorker, Err: errors.New("the prober is unavailable")}
}

type staticTargeter struct{ ok bool }

func (s staticTargeter) Target(_ context.Context, node store.UV) (probe.Target, bool) {
	return probe.Target{ID: node.ID, Address: node.Domain, Port: 443, ServerName: node.Domain, Path: "/secret/"}, s.ok
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(t.TempDir(), "termit.db"),
		Now:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func enroll(t *testing.T, db *store.Store, id, domain string, state store.State) store.UV {
	t.Helper()
	ctx := context.Background()
	node := store.UV{ID: id, Domain: domain, SelfUUID: "uuid-" + id, ProbeUUID: "probe-" + id,
		State: state, CreatedAt: now, CredentialEpoch: 1}
	if err := db.CreateUV(ctx, node); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}
	return node
}

func runner(db *store.Store, prober sched.Prober, targeter sched.Targeter) *sched.Runner {
	return &sched.Runner{
		Store:    db,
		Prober:   prober,
		Targeter: targeter,
		Policy:   sched.Policy{Interval: 24 * time.Hour, Jitter: 0, Retries: sched.RetryPolicy{Attempts: 3, Gap: 10}, Batch: 10, Workers: 2},
		Now:      func() time.Time { return now },
		Sleep:    func(context.Context, time.Duration) bool { return true },
	}
}

func stateOf(t *testing.T, db *store.Store, id string) store.State {
	t.Helper()
	node, err := db.UVByID(context.Background(), id)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	return node.State
}

func TestOneFailureMakesANodeUnavailable(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	node := enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	cycle, err := runner(db, &scriptedProber{results: []probe.Result{failResult()}}, staticTargeter{ok: true}).
		CheckOne(context.Background(), node)
	if err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	if cycle.Event != sched.EventCheckFailed {
		t.Errorf("event = %s, want failed", cycle.Event)
	}
	if got := stateOf(t, db, "uv_1"); got != store.StateUnavailable {
		t.Errorf("state = %s, want unavailable", got)
	}
	// Three attempts happen inside one cycle, and each is recorded.
	if cycle.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", cycle.Attempts)
	}
	checks, err := db.Checks(context.Background(), "uv_1", 10)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(checks) != 3 {
		t.Errorf("%d check rows, want one per attempt", len(checks))
	}
	if checks[0].ErrorClass == "" {
		t.Error("the check row lost the error class")
	}
}

func TestSecondFailureBlocks(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)
	prober := &scriptedProber{results: []probe.Result{failResult()}}
	r := runner(db, prober, staticTargeter{ok: true})

	for i := 0; i < 2; i++ {
		node, err := db.UVByID(context.Background(), "uv_1")
		if err != nil {
			t.Fatalf("UVByID: %v", err)
		}
		if _, err := r.CheckOne(context.Background(), node); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	if got := stateOf(t, db, "uv_1"); got != store.StateBlocked {
		t.Errorf("state = %s, want blocked", got)
	}
	// The transition is audited, so an owner can be told why they were cut off.
	audit, err := db.AuditCount(context.Background(), "uv_1")
	if err != nil {
		t.Fatalf("AuditCount: %v", err)
	}
	if audit == 0 {
		t.Error("the block was not audited")
	}
}

func TestSuccessRecoversTheNode(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateUnavailable)

	if _, err := runner(db, &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: true}).
		CheckOne(context.Background(), mustNode(t, db, "uv_1")); err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	if got := stateOf(t, db, "uv_1"); got != store.StateActive {
		t.Errorf("state = %s, want active", got)
	}
}

func TestTheSecondAttemptCanSaveACycle(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)
	prober := &scriptedProber{results: []probe.Result{failResult(), okResult()}}

	cycle, err := runner(db, prober, staticTargeter{ok: true}).CheckOne(context.Background(), mustNode(t, db, "uv_1"))
	if err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	if cycle.Event != sched.EventCheckOK || cycle.Attempts != 2 {
		t.Errorf("event=%s attempts=%d, want ok after 2", cycle.Event, cycle.Attempts)
	}
	if got := stateOf(t, db, "uv_1"); got != store.StateActive {
		t.Errorf("state = %s, want active", got)
	}
}

func TestAProberFailureNeverMovesANode(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateUnavailable)

	cycle, err := runner(db, &scriptedProber{results: []probe.Result{unknownResult()}}, staticTargeter{ok: true}).
		CheckOne(context.Background(), mustNode(t, db, "uv_1"))
	if err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	if cycle.Event != sched.EventCheckUnknown {
		t.Errorf("event = %s, want unknown", cycle.Event)
	}
	if got := stateOf(t, db, "uv_1"); got != store.StateUnavailable {
		t.Errorf("state = %s: an unknown check changed it", got)
	}
	// The retries still happened: an unavailable prober is worth another look.
	if cycle.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", cycle.Attempts)
	}
}

func TestANodeThatCannotBeTargetedIsUnknownNotFailed(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	cycle, err := runner(db, &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: false}).
		CheckOne(context.Background(), mustNode(t, db, "uv_1"))
	if err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	if cycle.Event != sched.EventCheckUnknown {
		t.Errorf("event = %s, want unknown", cycle.Event)
	}
	if got := stateOf(t, db, "uv_1"); got != store.StateActive {
		t.Errorf("state = %s: a missing target moved the node", got)
	}
}

func TestTheNextCheckIsScheduledWithinTheJitter(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)
	r := runner(db, &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: true})
	r.Policy.Jitter = 2 * time.Hour

	if _, err := r.CheckOne(context.Background(), mustNode(t, db, "uv_1")); err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	node, err := db.UVByID(context.Background(), "uv_1")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.NextCheckAt == nil {
		t.Fatal("no next check was scheduled")
	}
	earliest := now.Add(r.Policy.Interval - r.Policy.Jitter)
	latest := now.Add(r.Policy.Interval)
	if node.NextCheckAt.Before(earliest) || node.NextCheckAt.After(latest) {
		t.Errorf("next check %s is outside [%s, %s]", node.NextCheckAt, earliest, latest)
	}
	// Scheduled and not due, so a pass right after a check finds nothing.
	cycles, err := r.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(cycles) != 0 {
		t.Errorf("a pass rechecked a node that was just checked: %d cycles", len(cycles))
	}
}

func TestEveryCycleIsObservedOnce(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	observer := &recordingObserver{}
	r := runner(db, &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: true})
	r.Metrics = observer
	node, err := db.UVByID(context.Background(), "uv_1")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if _, err := r.CheckOne(context.Background(), node); err != nil {
		t.Fatalf("CheckOne: %v", err)
	}

	// One cycle, one observation: three attempts are one verdict, and a counter that counted attempts
	// would report a failing node three times as loudly as a healthy one.
	if len(observer.seen) != 1 {
		t.Fatalf("%d observations, want 1", len(observer.seen))
	}
	if observer.seen[0] != sched.ResultOK {
		t.Errorf("result = %q, want ok", observer.seen[0])
	}
}

func TestTheObserverSeesTheOtherOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		prober   sched.Prober
		targeter sched.Targeter
		want     string
	}{
		{"a failing node", &scriptedProber{results: []probe.Result{failResult()}}, staticTargeter{ok: true}, sched.ResultFailed},
		{"a prober that cannot do its job", &scriptedProber{results: []probe.Result{unknownResult()}}, staticTargeter{ok: true}, sched.ResultUnknown},
		{"a node with no target", &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: false}, sched.ResultUnknown},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := newStore(t)
			enroll(t, db, "uv_1", "one.example.com", store.StateActive)
			observer := &recordingObserver{}
			r := runner(db, tc.prober, tc.targeter)
			r.Metrics = observer
			node, err := db.UVByID(context.Background(), "uv_1")
			if err != nil {
				t.Fatalf("UVByID: %v", err)
			}
			if _, err := r.CheckOne(context.Background(), node); err != nil {
				t.Fatalf("CheckOne: %v", err)
			}
			if len(observer.seen) != 1 || observer.seen[0] != tc.want {
				t.Errorf("results = %v, want [%s]", observer.seen, tc.want)
			}
		})
	}
}

// recordingObserver keeps what the runner reported.
type recordingObserver struct {
	seen []string
}

func (o *recordingObserver) ObserveCheck(result string, _ time.Duration) {
	o.seen = append(o.seen, result)
}

func TestARequeueIsNotAWholeCycle(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	node := enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	observer := &recordingObserver{}
	// A cancelled context stops the cycle before the verdict; nothing is added to the counters,
	// because "we shut down" is not a fact about the node.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := runner(db, &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: true})
	r.Metrics = observer
	if _, err := r.CheckOne(ctx, node); err == nil {
		t.Fatal("a cancelled cycle returned no error")
	}
	if len(observer.seen) != 0 {
		t.Errorf("a cancelled cycle was observed as %v", observer.seen)
	}
}

func TestPassTakesOnlyDueNodes(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_due", "due.example.com", store.StateActive)
	enroll(t, db, "uv_later", "later.example.com", store.StateActive)
	enroll(t, db, "uv_gone", "gone.example.com", store.StateRemoved)
	if err := db.ScheduleCheck(context.Background(), "uv_later", now.Add(time.Hour), now); err != nil {
		t.Fatalf("ScheduleCheck: %v", err)
	}

	cycles, err := runner(db, &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: true}).
		Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(cycles) != 1 || cycles[0].Node.ID != "uv_due" {
		t.Errorf("pass checked %v, want only uv_due", ids(cycles))
	}
}

func TestAProberThatCannotStartIsUnknownNotAFailedNode(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	cycles, err := runner(db, &failingProber{}, staticTargeter{ok: true}).Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(cycles) != 1 || cycles[0].Event != sched.EventCheckUnknown {
		t.Fatalf("cycles = %v, want one unknown cycle", ids(cycles))
	}
	// A prober that cannot start says nothing about the node.
	if got := stateOf(t, db, "uv_1"); got != store.StateActive {
		t.Errorf("state = %s: a broken prober moved the node", got)
	}
	// And it is rescheduled, so a broken prober does not leave the node due forever.
	node := mustNode(t, db, "uv_1")
	if node.NextCheckAt == nil {
		t.Error("the node was left due forever")
	}
}

type failingProber struct{}

func (failingProber) Probe(context.Context, []probe.Target) ([]probe.Result, error) {
	return nil, errors.New("the core did not start")
}

func mustNode(t *testing.T, db *store.Store, id string) store.UV {
	t.Helper()
	node, err := db.UVByID(context.Background(), id)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	return node
}

func ids(cycles []sched.Cycle) []string {
	out := make([]string, 0, len(cycles))
	for _, cycle := range cycles {
		out = append(out, cycle.Node.ID)
	}
	return out
}
