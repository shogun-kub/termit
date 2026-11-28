package sched_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/mn/sched"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	return probe.Target{ID: node.ID, Address: node.Domain, Port: 443, ServerName: node.Domain,
		Path: "/secret/", Transport: proto.KindVLESS}, s.ok
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

// TestGraceChecksDeferTheBlock: block_after_failed_cycles = 4 leaves the node unavailable for three
// failed cycles and blocks it on the fourth. The three attempts inside a cycle are one count, not three.
func TestGraceChecksDeferTheBlock(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)
	r := runner(db, &scriptedProber{results: []probe.Result{failResult()}}, staticTargeter{ok: true})
	r.Policy.BlockAfter = 4

	for cycle := 1; cycle <= 3; cycle++ {
		if _, err := r.CheckOne(context.Background(), mustNode(t, db, "uv_1")); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if got := stateOf(t, db, "uv_1"); got != store.StateUnavailable {
			t.Fatalf("cycle %d: state = %s, want unavailable (still in the grace period)", cycle, got)
		}
		if got := mustNode(t, db, "uv_1").FailedCycles; got != cycle {
			t.Fatalf("cycle %d: failed cycles = %d, want %d (attempts do not count)", cycle, got, cycle)
		}
	}
	if _, err := r.CheckOne(context.Background(), mustNode(t, db, "uv_1")); err != nil {
		t.Fatalf("fourth cycle: %v", err)
	}
	if got := stateOf(t, db, "uv_1"); got != store.StateBlocked {
		t.Errorf("state = %s, want blocked after the fourth failed cycle", got)
	}
}

// switchProber answers every attempt with whatever verdict the test last set: a cycle verdict, rather
// than the attempt-by-attempt script scriptedProber plays.
type switchProber struct{ result probe.Result }

func (p *switchProber) Probe(context.Context, []probe.Target) ([]probe.Result, error) {
	return []probe.Result{p.result}, nil
}

// TestACheckThatCouldNotRunKeepsTheGrace: an unknown cycle is neither progress nor forgiveness. If it
// reset the streak, three failures spread around prober outages would never block the node.
func TestACheckThatCouldNotRunKeepsTheGrace(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)
	prober := &switchProber{result: failResult()}
	r := runner(db, prober, staticTargeter{ok: true})
	r.Policy.BlockAfter = 3
	check := func() {
		t.Helper()
		if _, err := r.CheckOne(context.Background(), mustNode(t, db, "uv_1")); err != nil {
			t.Fatalf("CheckOne: %v", err)
		}
	}

	check() // failure 1
	check() // failure 2
	prober.result = unknownResult()
	check() // not counted
	if got := mustNode(t, db, "uv_1").FailedCycles; got != 2 {
		t.Fatalf("failed cycles = %d after an unknown cycle, want 2", got)
	}
	prober.result = failResult()
	check() // failure 3 — blocks, because the unknown cycle did not reset the streak
	if got := stateOf(t, db, "uv_1"); got != store.StateBlocked {
		t.Errorf("state = %s, want blocked: an unknown cycle must not reset the streak", got)
	}
}

// TestASuccessResetsTheGrace: a node that comes back starts a new streak, so a flapping path cannot
// accumulate failures across recoveries.
func TestASuccessResetsTheGrace(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)
	prober := &switchProber{result: failResult()}
	r := runner(db, prober, staticTargeter{ok: true})
	r.Policy.BlockAfter = 3
	check := func() {
		t.Helper()
		if _, err := r.CheckOne(context.Background(), mustNode(t, db, "uv_1")); err != nil {
			t.Fatalf("CheckOne: %v", err)
		}
	}

	check() // failure 1
	check() // failure 2
	prober.result = okResult()
	check() // back to active, streak reset
	prober.result = failResult()
	check() // failure 1 of a new streak

	if got := stateOf(t, db, "uv_1"); got != store.StateUnavailable {
		t.Errorf("state = %s, want unavailable: the recovery reset the streak", got)
	}
	if got := mustNode(t, db, "uv_1").FailedCycles; got != 1 {
		t.Errorf("failed cycles = %d, want 1 after the reset", got)
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

// TestACheckSaysWhichTransportItProbed: a node may declare two transports while the server probes one,
// so a check row that does not name its leg lets a green "ok" stand for a transport nobody tested.
func TestACheckSaysWhichTransportItProbed(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	node := enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	if _, err := runner(db, &scriptedProber{results: []probe.Result{okResult()}}, staticTargeter{ok: true}).
		CheckOne(context.Background(), node); err != nil {
		t.Fatalf("CheckOne: %v", err)
	}
	checks, err := db.Checks(context.Background(), "uv_1", 10)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if len(checks) == 0 {
		t.Fatal("no check rows")
	}
	if !strings.Contains(checks[0].DetailJSON, `"transport":"vless"`) {
		t.Errorf("the row does not say which transport it probed: %q", checks[0].DetailJSON)
	}
}

// A vantage answers for a node this server cannot reach (docs/IMPLEMENTATION_PLAN.md §5.2.4). The rule is the
// one the plan decided: an `ok` from another network overrules the local failure, a `failed` only agrees with
// what was already seen, and a vantage that cannot answer leaves the node alone.
func TestASecondVantageRescuesANodeThisServerCannotReach(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		answer    proto.ProbeAnswer
		wantEvent sched.Event
	}{
		{"the peer reaches it", proto.ProbeAnswer{OK: true}, sched.EventCheckOK},
		{"the peer agrees it is down", proto.ProbeAnswer{OK: false, Class: "tunnel_rejected"}, sched.EventCheckFailed},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			db := newStore(t)
			enroll(t, db, "uv_1", "one.example.com", store.StateActive)
			test.answer.At = now

			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/prefix"+proto.PeerProbePath {
					t.Errorf("the vantage was called at %s", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("credential = %q", got)
				}
				_ = json.NewEncoder(w).Encode(test.answer)
			}))
			defer peer.Close()

			r := runner(db, &rejectingProber{}, staticTargeter{ok: true})
			r.Vantage = func(store.UV) (*probe.Vantage, bool) {
				return &probe.Vantage{
					Name:    "mn2",
					BaseURL: peer.URL + "/prefix",
					Token:   "test-token",
					HTTP:    peer.Client(),
					Now:     func() time.Time { return now },
				}, true
			}
			cycles, err := r.Pass(context.Background())
			if err != nil {
				t.Fatalf("Pass: %v", err)
			}
			if len(cycles) != 1 || cycles[0].Event != test.wantEvent {
				t.Fatalf("cycles = %v, want %v", ids(cycles), test.wantEvent)
			}
		})
	}
}

// A vantage that does not answer is not a verdict: the node's state is what the local probe left it as.
func TestAVantageThatCannotAnswerChangesNothing(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer peer.Close()

	r := runner(db, &rejectingProber{}, staticTargeter{ok: true})
	r.Vantage = func(store.UV) (*probe.Vantage, bool) {
		return &probe.Vantage{Name: "mn2", BaseURL: peer.URL, Token: "t", HTTP: peer.Client(),
			Now: func() time.Time { return now }}, true
	}
	cycles, err := r.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(cycles) != 1 || cycles[0].Event != sched.EventCheckFailed {
		t.Fatalf("cycles = %v", ids(cycles))
	}
}

// A stale observation is not a verdict about now.
func TestAStaleVantageAnswerIsNotAVerdict(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(proto.ProbeAnswer{OK: true, At: now.Add(-time.Hour)})
	}))
	defer peer.Close()

	r := runner(db, &rejectingProber{}, staticTargeter{ok: true})
	r.Vantage = func(store.UV) (*probe.Vantage, bool) {
		return &probe.Vantage{Name: "mn2", BaseURL: peer.URL, Token: "t", HTTP: peer.Client(),
			Now: func() time.Time { return now }}, true
	}
	cycles, err := r.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(cycles) != 1 || cycles[0].Event != sched.EventCheckFailed {
		t.Fatalf("a stale answer was taken as a verdict: %v", ids(cycles))
	}
}

// A prober that reaches the node ends the cycle before any vantage is consulted: the peer is for the case
// this server could not conclude, not a second opinion on every check.
func TestTheVantageIsNotAskedWhenTheLocalProbeSucceeded(t *testing.T) {
	t.Parallel()
	db := newStore(t)
	enroll(t, db, "uv_1", "one.example.com", store.StateActive)

	asked := false
	r := runner(db, &acceptingProber{}, staticTargeter{ok: true})
	r.Vantage = func(store.UV) (*probe.Vantage, bool) {
		asked = true
		return nil, false
	}
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if asked {
		t.Error("the vantage was consulted after a successful local probe")
	}
}

type rejectingProber struct{}

func (rejectingProber) Probe(_ context.Context, targets []probe.Target) ([]probe.Result, error) {
	out := make([]probe.Result, 0, len(targets))
	for _, target := range targets {
		out = append(out, probe.Result{ID: target.ID, Class: probe.ClassRejected, Err: errors.New("blocked")})
	}
	return out, nil
}

type acceptingProber struct{}

func (acceptingProber) Probe(_ context.Context, targets []probe.Target) ([]probe.Result, error) {
	out := make([]probe.Result, 0, len(targets))
	for _, target := range targets {
		out = append(out, probe.Result{ID: target.ID, OK: true})
	}
	return out, nil
}
