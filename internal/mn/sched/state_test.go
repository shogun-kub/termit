package sched_test

import (
	"testing"

	"github.com/shogun-kub/termit/internal/mn/sched"
)

var allStates = []sched.State{
	sched.StatePending, sched.StateActive, sched.StateUnavailable, sched.StateBlocked, sched.StateRemoved,
}

var allEvents = []sched.Event{
	sched.EventCheckOK, sched.EventCheckFailed, sched.EventCheckUnknown,
	sched.EventApprove, sched.EventReject,
	sched.EventUnblock, sched.EventUnblockFailed, sched.EventRelocate,
	sched.EventAdminBlock, sched.EventAdminUnblock, sched.EventAdminRemove,
}

// TestUnknownNeverChangesAnything is invariant 2 stated as a test over every state: if the prober could
// not do its job, the machine must not move.
func TestUnknownNeverChangesAnything(t *testing.T) {
	t.Parallel()
	for _, state := range allStates {
		outcome := sched.Transition(state, sched.EventCheckUnknown, sched.Tally{})
		if outcome.Changed {
			t.Errorf("%s moved to %s on an unknown check", state, outcome.Next)
		}
		if outcome.Next != state {
			t.Errorf("%s became %s on an unknown check", state, outcome.Next)
		}
		if len(outcome.Effects) != 0 {
			t.Errorf("%s produced effects %v on an unknown check", state, outcome.Effects)
		}
	}
}

func TestEveryPairAnswers(t *testing.T) {
	t.Parallel()
	for _, state := range allStates {
		for _, event := range allEvents {
			outcome := sched.Transition(state, event, sched.Tally{Streak: 2})
			if outcome.Next == "" {
				t.Errorf("%s + %s produced no next state", state, event)
			}
			if outcome.Reason == "" {
				t.Errorf("%s + %s produced no reason", state, event)
			}
			// A change without a next state, or a next state without a change, is a bug in the table.
			if !outcome.Changed && outcome.Next != state {
				t.Errorf("%s + %s moved to %s without reporting a change", state, event, outcome.Next)
			}
			if outcome.Changed && outcome.Next == state {
				t.Errorf("%s + %s reported a change to the same state", state, event)
			}
		}
	}
}

func TestLifecycle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state sched.State
		event sched.Event
		tally sched.Tally
		want  sched.State
	}{
		{"a fresh failure makes a node unavailable", sched.StateActive, sched.EventCheckFailed, sched.Tally{Streak: 1}, sched.StateUnavailable},
		{"a second failure blocks it", sched.StateUnavailable, sched.EventCheckFailed, sched.Tally{Streak: 2}, sched.StateBlocked},
		{"a success restores it", sched.StateUnavailable, sched.EventCheckOK, sched.Tally{}, sched.StateActive},
		{"a blocked node stays blocked on success", sched.StateBlocked, sched.EventCheckOK, sched.Tally{}, sched.StateBlocked},
		{"an unblock request with passing checks restores it", sched.StateBlocked, sched.EventUnblock, sched.Tally{}, sched.StateActive},
		{"a failed unblock request changes nothing", sched.StateBlocked, sched.EventUnblockFailed, sched.Tally{}, sched.StateBlocked},
		{"a successful relocation re-admits a blocked node", sched.StateBlocked, sched.EventRelocate, sched.Tally{}, sched.StateActive},
		{"a successful relocation re-admits an unavailable node", sched.StateUnavailable, sched.EventRelocate, sched.Tally{}, sched.StateActive},
		{"an active node keeps its state when it moves", sched.StateActive, sched.EventRelocate, sched.Tally{}, sched.StateActive},
		{"a pending node cannot move itself in", sched.StatePending, sched.EventRelocate, sched.Tally{}, sched.StatePending},
		{"approval admits a pending node", sched.StatePending, sched.EventApprove, sched.Tally{}, sched.StateActive},
		{"a pending node that answers is still pending", sched.StatePending, sched.EventCheckOK, sched.Tally{}, sched.StatePending},
		{"rejection removes it", sched.StatePending, sched.EventReject, sched.Tally{}, sched.StateRemoved},
		{"an operator can block an active node", sched.StateActive, sched.EventAdminBlock, sched.Tally{}, sched.StateBlocked},
		{"an operator can unblock", sched.StateBlocked, sched.EventAdminUnblock, sched.Tally{}, sched.StateActive},
		{"removal is final", sched.StateRemoved, sched.EventApprove, sched.Tally{}, sched.StateRemoved},
		{"removal wins from blocked", sched.StateBlocked, sched.EventAdminRemove, sched.Tally{}, sched.StateRemoved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			outcome := sched.Transition(tc.state, tc.event, tc.tally)
			if outcome.Next != tc.want {
				t.Errorf("%s + %s = %s, want %s", tc.state, tc.event, outcome.Next, tc.want)
			}
		})
	}
}

func TestEffectsFollowTheTransition(t *testing.T) {
	t.Parallel()
	// Losing a node changes every subscriber's peer set.
	if out := sched.Transition(sched.StateActive, sched.EventCheckFailed, sched.Tally{Streak: 1}); !hasEffect(out.Effects, sched.EffectPeersDirty) {
		t.Errorf("a node going unavailable did not dirty the peer sets: %v", out.Effects)
	}
	// Removal kills credentials, not just presence.
	if out := sched.Transition(sched.StateActive, sched.EventAdminRemove, sched.Tally{}); !hasEffect(out.Effects, sched.EffectCredentialsInvalid) {
		t.Errorf("removal did not invalidate credentials: %v", out.Effects)
	}
	// A no-op must have no effects at all.
	if out := sched.Transition(sched.StateBlocked, sched.EventCheckOK, sched.Tally{}); len(out.Effects) != 0 {
		t.Errorf("a no-op produced effects: %v", out.Effects)
	}
	// Every state change is worth an audit record unless it is a machine-driven availability flip;
	// those are recorded as check results instead.
	if out := sched.Transition(sched.StateUnavailable, sched.EventCheckOK, sched.Tally{}); !hasEffect(out.Effects, sched.EffectPeersDirty) {
		t.Errorf("a node coming back did not dirty the peer sets: %v", out.Effects)
	}
	// A failure that is still inside the grace period is not a peer-set change: the node stays listed.
	if out := sched.Transition(sched.StateUnavailable, sched.EventCheckFailed, sched.Tally{Streak: 2, BlockAfter: 4}); len(out.Effects) != 0 || out.Changed {
		t.Errorf("a grace failure produced effects %v (changed=%v)", out.Effects, out.Changed)
	}
}

// TestGraceDefersTheBlock pins the configurable grace: a node blocks only when the streak reaches the
// threshold, and a threshold of 1 blocks on the first failure inside unavailable (the old behaviour).
func TestGraceDefersTheBlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		streak  int
		blockAt int
		want    sched.State
	}{
		{"one grace check, second failure blocks", 2, 2, sched.StateBlocked},
		{"no grace, first failure blocks", 1, 1, sched.StateBlocked},
		{"grace of three, third failure still unavailable", 3, 4, sched.StateUnavailable},
		{"grace of three, fourth failure blocks", 4, 4, sched.StateBlocked},
		{"unset threshold means two strikes", 2, 0, sched.StateBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := sched.Transition(sched.StateUnavailable, sched.EventCheckFailed,
				sched.Tally{Streak: tc.streak, BlockAfter: tc.blockAt})
			if out.Next != tc.want {
				t.Errorf("streak %d of %d = %s, want %s", tc.streak, tc.blockAt, out.Next, tc.want)
			}
		})
	}
}

func TestRetryPolicy(t *testing.T) {
	t.Parallel()
	policy := sched.RetryPolicy{Attempts: 3, Gap: 10}
	for attempt := 0; attempt < 3; attempt++ {
		gap, ok := policy.NextAttempt(attempt)
		if !ok || gap != 10 {
			t.Errorf("attempt %d: gap=%d ok=%v", attempt, gap, ok)
		}
	}
	if _, ok := policy.NextAttempt(3); ok {
		t.Error("a fourth attempt was allowed")
	}

	// Defaults: three attempts, ten minutes apart.
	var defaults sched.RetryPolicy
	if gap, ok := defaults.NextAttempt(2); !ok || gap != 10 {
		t.Errorf("defaults: gap=%d ok=%v", gap, ok)
	}
	if _, ok := defaults.NextAttempt(3); ok {
		t.Error("the default policy allowed a fourth attempt")
	}
}

func hasEffect(effects []sched.Effect, want sched.Effect) bool {
	for _, effect := range effects {
		if effect == want {
			return true
		}
	}
	return false
}
