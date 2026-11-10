package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// Policy is how often and how hard nodes are checked.
type Policy struct {
	// Interval is the gap between cycles for one node.
	Interval time.Duration
	// Jitter spreads the cycles, so a mesh that enrolled together does not probe together.
	Jitter time.Duration
	// Retries is the in-cycle retry policy: three attempts, ten minutes apart, by default.
	Retries RetryPolicy
	// Batch is how many due nodes one pass takes.
	Batch int
	// Workers is how many nodes are probed at once.
	Workers int
}

// DefaultPolicy is the documented default: a check a day, at most an hour of spread.
func DefaultPolicy() Policy {
	return Policy{
		Interval: 24 * time.Hour,
		Jitter:   time.Hour,
		Retries:  RetryPolicy{Attempts: 3, Gap: 10},
		Batch:    50,
		Workers:  4,
	}
}

// Prober is the part of the prober the scheduler needs. A test passes a fake.
type Prober interface {
	Probe(ctx context.Context, targets []probe.Target) ([]probe.Result, error)
}

// Targeter builds the probe target for a node. It returns ok=false when the node cannot be probed yet,
// which is unknown, not a failure. It takes a context because a targeter may have to read the node's
// certificate the first time.
type Targeter interface {
	Target(ctx context.Context, node store.UV) (probe.Target, bool)
}

// Cycle is what one node's check produced.
type Cycle struct {
	Node     store.UV
	Result   probe.Result
	Event    Event
	Outcome  Outcome
	Attempts int
	// Errored is set when the cycle could not run at all (no prober, no target).
	Errored error
}

// Cycle outcomes as an observer sees them. They are plain words on purpose: the scheduler does not
// import a metrics package, and a counter label is not worth a dependency edge.
const (
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultUnknown = "unknown"
	ResultError   = "error"
)

// CheckObserver is told about every cycle the runner finishes. It exists so the server can expose
// counters and a latency histogram without the state machine knowing what a metric is.
type CheckObserver interface {
	ObserveCheck(result string, took time.Duration)
}

// Runner checks nodes on a schedule and applies the transition table to what it finds.
type Runner struct {
	Store    *store.Store
	Prober   Prober
	Targeter Targeter
	Policy   Policy
	Now      func() time.Time
	Logger   *slog.Logger
	// Metrics, when set, sees every cycle: its outcome and its duration.
	Metrics CheckObserver
	// Sleep waits between attempts. Tests replace it; production leaves it nil.
	Sleep func(ctx context.Context, d time.Duration) bool
	// PolicyFn, when set, supplies the policy for each pass. It is how an operator changes the
	// interval without restarting the server.
	PolicyFn func(ctx context.Context) Policy
}

// CheckOne runs one cycle for one node: up to Policy.Retries.Attempts probes, the aggregate turned into
// one event, and the node's next check scheduled whatever the result.
func (r *Runner) CheckOne(ctx context.Context, node store.UV) (Cycle, error) {
	r.applyDefaults()
	return r.check(ctx, node, r.Policy.Retries)
}

// CheckNow is one cycle without the in-cycle retries: what an operator asking "check it now" wants, and
// what a diagnostic needs. Waiting ten minutes between attempts is right for the nightly cycle and
// absurd for a command someone is watching.
func (r *Runner) CheckNow(ctx context.Context, node store.UV) (Cycle, error) {
	r.applyDefaults()
	return r.check(ctx, node, RetryPolicy{Attempts: 1, Gap: r.Policy.Retries.Gap})
}

func (r *Runner) check(ctx context.Context, node store.UV, retries RetryPolicy) (Cycle, error) {
	started := r.Now()
	cycle, err := r.runCycle(ctx, node, retries)
	// A cancelled context is a shutdown, not a cycle that failed: counting it would make every
	// restart look like a burst of errors.
	if r.Metrics != nil && ctx.Err() == nil {
		r.Metrics.ObserveCheck(cycleResult(cycle, err), r.Now().Sub(started))
	}
	return cycle, err
}

// cycleResult classifies a finished cycle for the observer.
func cycleResult(cycle Cycle, err error) string {
	switch {
	case err != nil:
		return ResultError
	case cycle.Event == EventCheckOK:
		return ResultOK
	case cycle.Event == EventCheckFailed:
		return ResultFailed
	default:
		return ResultUnknown
	}
}

func (r *Runner) runCycle(ctx context.Context, node store.UV, retries RetryPolicy) (Cycle, error) {
	cycle := Cycle{Node: node}

	target, ok := r.target(ctx, node)
	if !ok {
		// Nothing to probe: unknown, and no state change. This is the "missing prober" branch, not a
		// verdict about the node.
		return r.finish(ctx, cycle, EventCheckUnknown, probe.Result{Class: probe.ClassWorker})
	}

	var (
		last     probe.Result
		okSeen   bool
		definite bool
	)
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return cycle, ctx.Err()
		}
		result, err := r.probeOne(ctx, target)
		cycle.Attempts = attempt + 1
		if err != nil {
			// The prober itself failed: unknown for this attempt, but another attempt may still land.
			last = probe.Result{Class: probe.ClassWorker}
		} else {
			last = result
			if result.Unknown() {
				// The prober could not do its job; this attempt is not evidence either way.
			} else {
				definite = true
				if result.OK {
					okSeen = true
				}
			}
		}
		if err := r.record(ctx, node.ID, last); err != nil {
			return cycle, err
		}
		if okSeen {
			break
		}
		if _, more := retries.NextAttempt(attempt + 1); !more {
			break
		}
		if !r.sleep(ctx, time.Duration(retries.Gap)*time.Minute) {
			return cycle, ctx.Err()
		}
	}

	event := EventCheckUnknown
	switch {
	case okSeen:
		event = EventCheckOK
	case definite:
		// Every attempt that reached a conclusion concluded the same thing: this is evidence against
		// the node, so it may move it.
		event = EventCheckFailed
	}
	return r.finish(ctx, cycle, event, last)
}

// finish applies the table, stores the outcome, schedules the next check and returns the cycle.
func (r *Runner) finish(ctx context.Context, cycle Cycle, event Event, result probe.Result) (Cycle, error) {
	cycle.Result = result
	cycle.Event = event

	outcome := Transition(State(cycle.Node.State), event)
	cycle.Outcome = outcome
	now := r.Now().UTC()

	if outcome.Changed && r.Store != nil {
		if err := r.setNodeState(ctx, cycle.Node, outcome, now); err != nil {
			return cycle, err
		}
	}
	if r.Store != nil {
		failed := 0
		if event == EventCheckFailed {
			failed = 1
		}
		if err := r.Store.SetFailedCycles(ctx, cycle.Node.ID, failed); err != nil {
			return cycle, err
		}
		if err := r.Store.ScheduleCheck(ctx, cycle.Node.ID, r.nextDue(now, cycle.Node.ID), now); err != nil {
			return cycle, err
		}
	}
	return cycle, nil
}

func (r *Runner) setNodeState(ctx context.Context, node store.UV, outcome Outcome, now time.Time) error {
	to := store.State(outcome.Next)
	if !to.Valid() {
		return fmt.Errorf("sched: the table produced %q, which is not a state", outcome.Next)
	}
	event, err := r.Store.SetUVState(ctx, node.ID, to, outcome.Reason, "scheduler", now)
	if err != nil {
		return err
	}
	for _, effect := range outcome.Effects {
		// Only the audit effect needs the scheduler to act: peer sets and subscriptions are derived from
		// the state, so they change by themselves once the state does.
		if effect == EffectAudit {
			if err := r.Store.AppendAudit(ctx, store.AuditRecord{
				At: now, Actor: "scheduler", Action: "uv.transition", Target: node.ID,
				DetailJSON: fmt.Sprintf(`{"from":%q,"to":%q,"reason":%q}`, event.FromState, event.ToState, outcome.Reason),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) probeOne(ctx context.Context, target probe.Target) (probe.Result, error) {
	if r.Prober == nil {
		return probe.Result{}, errors.New("sched: no prober is configured")
	}
	results, err := r.Prober.Probe(ctx, []probe.Target{target})
	if err != nil {
		return probe.Result{}, err
	}
	if len(results) == 0 {
		return probe.Result{}, errors.New("sched: the prober returned no result")
	}
	return results[0], nil
}

func (r *Runner) record(ctx context.Context, uvID string, result probe.Result) error {
	if r.Store == nil {
		return nil
	}
	detail := ""
	if result.Err != nil {
		// The message, not the error: a check row is data, and a wrapped error would drag a stack
		// around. It must not contain a credential, which the prober guarantees by construction.
		detail = result.Err.Error()
	}
	return r.Store.AppendCheck(ctx, store.Check{
		UVID:       uvID,
		At:         r.Now().UTC(),
		Kind:       "probe",
		OK:         result.OK,
		LatencyMS:  int(result.Latency / time.Millisecond),
		ErrorClass: string(result.Class),
		DetailJSON: detail,
	})
}

func (r *Runner) target(ctx context.Context, node store.UV) (probe.Target, bool) {
	if r.Targeter == nil {
		return probe.Target{}, false
	}
	return r.Targeter.Target(ctx, node)
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) bool {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
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

// nextDue is the node's next check: the interval, minus up to the jitter, offset by a per-node amount so
// that a whole mesh does not come due in the same minute. The offset is derived from the id, so it is
// stable across restarts.
func (r *Runner) nextDue(now time.Time, id string) time.Time {
	spread := r.Policy.Jitter
	if spread < 0 {
		spread = 0
	}
	offset := time.Duration(0)
	if spread > 0 {
		var sum int64
		for i := 0; i < len(id); i++ {
			sum = sum*31 + int64(id[i])
		}
		if sum < 0 {
			sum = -sum
		}
		offset = time.Duration(sum%int64(spread/time.Second)) * time.Second
	}
	return now.Add(r.Policy.Interval - offset)
}

func (r *Runner) applyDefaults() {
	if r.Now == nil {
		r.Now = func() time.Time { return time.Now().UTC() }
	}
	if r.Policy.Interval <= 0 {
		r.Policy.Interval = DefaultPolicy().Interval
	}
	if r.Policy.Batch <= 0 {
		r.Policy.Batch = DefaultPolicy().Batch
	}
	if r.Policy.Workers <= 0 {
		r.Policy.Workers = 1
	}
	if r.Policy.Retries.Attempts <= 0 {
		r.Policy.Retries = DefaultPolicy().Retries
	}
}

// Pass checks every node that is due, once. It is what Run loops over, and what a test drives directly.
func (r *Runner) Pass(ctx context.Context) ([]Cycle, error) {
	if r.PolicyFn != nil {
		if updated := r.PolicyFn(ctx); updated.Interval > 0 {
			r.Policy = updated
		}
	}
	r.applyDefaults()
	if r.Store == nil {
		return nil, errors.New("sched: a store is required")
	}
	now := r.Now().UTC()
	nodes, err := r.Store.DueUVs(ctx, now, r.Policy.Batch)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	// One node per worker, and never the same node twice: the queue gives each id once per pass.
	work := make(chan store.UV)
	results := make(chan Cycle, len(nodes))
	errs := make(chan error, len(nodes))
	workers := r.Policy.Workers
	if workers > len(nodes) {
		workers = len(nodes)
	}
	var group sync.WaitGroup
	group.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer group.Done()
			for node := range work {
				cycle, err := r.CheckOne(ctx, node)
				if err != nil {
					errs <- fmt.Errorf("%s: %w", node.ID, err)
					continue
				}
				results <- cycle
			}
		}()
	}
	dispatched := true
	for _, node := range nodes {
		select {
		case work <- node:
		case <-ctx.Done():
			dispatched = false
		}
		if !dispatched {
			break
		}
	}
	close(work)
	group.Wait()
	close(results)
	close(errs)
	if !dispatched {
		return nil, ctx.Err()
	}

	var cycles []Cycle
	for cycle := range results {
		cycles = append(cycles, cycle)
	}
	var firstErr error
	for err := range errs {
		if firstErr == nil {
			firstErr = err
		}
	}
	if r.Logger != nil {
		for _, cycle := range cycles {
			r.Logger.Debug("check", "node", cycle.Node.Domain, "event", cycle.Event,
				"attempts", cycle.Attempts, "state", cycle.Outcome.Next)
		}
	}
	return cycles, firstErr
}

// Run checks the due nodes until the context is cancelled, sleeping between passes.
func (r *Runner) Run(ctx context.Context) error {
	r.applyDefaults()
	tick := r.Policy.Interval / 12
	if tick < time.Minute {
		tick = time.Minute
	}
	if tick > time.Hour {
		tick = time.Hour
	}
	for {
		if _, err := r.Pass(ctx); err != nil && ctx.Err() == nil {
			if r.Logger != nil {
				r.Logger.Warn("check pass failed", "err", err)
			}
		}
		if !r.sleep(ctx, tick) {
			return ctx.Err()
		}
	}
}
