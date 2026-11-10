// Package sched is the state machine that decides what a check result means.
//
// The table is pure: it takes a state and an event and returns the next state plus the effects the
// caller must perform. Keeping it that way is what makes "unknown never changes a state" a property the
// tests can prove rather than a rule someone remembers.
package sched

import "fmt"

// State is the lifecycle of a node. It mirrors the store's states without depending on it: this package
// has to be testable without a database.
type State string

const (
	StatePending     State = "pending"
	StateActive      State = "active"
	StateUnavailable State = "unavailable"
	StateBlocked     State = "blocked"
	StateRemoved     State = "removed"
)

// Event is what happened.
type Event string

const (
	// EventCheckOK is a cycle that carried a real request.
	EventCheckOK Event = "check_ok"
	// EventCheckFailed is a cycle where every attempt reached the node and failed.
	EventCheckFailed Event = "check_failed"
	// EventCheckUnknown is a cycle the prober could not complete. It is never evidence against a node.
	EventCheckUnknown Event = "check_unknown"
	// EventApprove admits a pending node.
	EventApprove Event = "approve"
	// EventReject removes a pending node.
	EventReject Event = "reject"
	// EventUnblock is the owner asking, with checks that passed, to be let back in.
	EventUnblock Event = "unblock"
	// EventUnblockFailed is the same request with checks that did not pass.
	EventUnblockFailed Event = "unblock_failed"
	// EventRelocate is the owner presenting the same node at a new host, with checks that passed. The
	// move has already proved the new address works, so it re-admits a blocked or unavailable node;
	// an active one keeps its state and only its endpoint changes.
	EventRelocate Event = "relocate"
	// EventAdminBlock, EventAdminUnblock and EventAdminRemove are operator overrides.
	EventAdminBlock   Event = "admin_block"
	EventAdminUnblock Event = "admin_unblock"
	EventAdminRemove  Event = "admin_remove"
)

// Effect is a side effect the caller performs after a transition. They are values, not callbacks, so a
// test can assert on them.
type Effect string

const (
	// EffectPeersDirty means the node's presence in peer sets changed: every subscriber has to see it.
	EffectPeersDirty Effect = "peers_dirty"
	// EffectCredentialsInvalid means the node's credentials must stop working.
	EffectCredentialsInvalid Effect = "credentials_invalid"
	// EffectAudit means the transition is worth an audit record.
	EffectAudit Effect = "audit"
	// EffectNotice means the owner should be told; today that is a log line and a state reason.
	EffectNotice Effect = "notice"
)

// Outcome is the decision.
type Outcome struct {
	Next    State
	Effects []Effect
	// Reason is a short human explanation stored with the state.
	Reason string
	// Changed is false when the event does not move the machine. The caller must then do nothing at
	// all: no state write, no effect, no audit.
	Changed bool
}

// Transition is the table. Every state × event pair is answered here; an unlisted pair is a change.
//
// The rules it encodes, from DESCRIPTION §7.2:
//   - active → unavailable on the first failed cycle;
//   - unavailable → blocked on the next one;
//   - blocked → active only through an explicit, successful unblock;
//   - unknown never moves anything, in any state.
func Transition(state State, event Event) Outcome {
	if event == EventCheckUnknown {
		// Invariant 2: we could not check, so we do not know. Say so and change nothing.
		return Outcome{Next: state, Reason: "the check could not be completed"}
	}

	switch state {
	case StatePending:
		switch event {
		case EventCheckOK:
			// A pending node that answers is still pending: admission is a decision, not a probe.
			return Outcome{Next: state, Reason: "checks passed; waiting for approval"}
		case EventApprove:
			return Outcome{Next: StateActive, Changed: true, Reason: "approved",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		case EventReject, EventAdminRemove:
			return Outcome{Next: StateRemoved, Changed: true, Reason: "rejected",
				Effects: []Effect{EffectCredentialsInvalid, EffectPeersDirty, EffectAudit}}
		case EventAdminBlock:
			return Outcome{Next: StateBlocked, Changed: true, Reason: "blocked by an operator",
				Effects: []Effect{EffectPeersDirty, EffectAudit}}
		case EventRelocate:
			// Admission is a decision: a node that has not been approved cannot move itself in.
			return Outcome{Next: state, Reason: "a pending node cannot move before it is approved"}
		}

	case StateActive:
		switch event {
		case EventCheckOK:
			return Outcome{Next: state, Reason: "checks passed"}
		case EventRelocate:
			return Outcome{Next: state, Reason: "the endpoint moved; the state does not change"}
		case EventCheckFailed:
			return Outcome{Next: StateUnavailable, Changed: true, Reason: "checks failed",
				Effects: []Effect{EffectPeersDirty, EffectNotice}}
		case EventAdminBlock:
			return Outcome{Next: StateBlocked, Changed: true, Reason: "blocked by an operator",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		case EventAdminRemove:
			return Outcome{Next: StateRemoved, Changed: true, Reason: "removed by an operator",
				Effects: []Effect{EffectCredentialsInvalid, EffectPeersDirty, EffectAudit}}
		}

	case StateUnavailable:
		switch event {
		case EventCheckOK:
			return Outcome{Next: StateActive, Changed: true, Reason: "checks passed again",
				Effects: []Effect{EffectPeersDirty, EffectNotice}}
		case EventCheckFailed:
			return Outcome{Next: StateBlocked, Changed: true, Reason: "checks failed twice in a row",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		case EventAdminBlock:
			return Outcome{Next: StateBlocked, Changed: true, Reason: "blocked by an operator",
				Effects: []Effect{EffectPeersDirty, EffectAudit}}
		case EventAdminUnblock:
			return Outcome{Next: StateActive, Changed: true, Reason: "unblocked by an operator",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		case EventAdminRemove:
			return Outcome{Next: StateRemoved, Changed: true, Reason: "removed by an operator",
				Effects: []Effect{EffectCredentialsInvalid, EffectPeersDirty, EffectAudit}}
		case EventRelocate:
			return Outcome{Next: StateActive, Changed: true, Reason: "relocated after a successful check",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		}

	case StateBlocked:
		switch event {
		case EventCheckOK:
			// A blocked node that answers stays blocked: no automatic un-blocking, to avoid flapping.
			return Outcome{Next: state, Reason: "still blocked; an unblock request is required"}
		case EventCheckFailed:
			return Outcome{Next: state, Reason: "still failing"}
		case EventUnblock:
			return Outcome{Next: StateActive, Changed: true, Reason: "unblocked after a successful check",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		case EventUnblockFailed:
			return Outcome{Next: state, Reason: "the unblock request failed its checks"}
		case EventRelocate:
			return Outcome{Next: StateActive, Changed: true, Reason: "relocated after a successful check",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		case EventAdminUnblock:
			return Outcome{Next: StateActive, Changed: true, Reason: "unblocked by an operator",
				Effects: []Effect{EffectPeersDirty, EffectAudit, EffectNotice}}
		case EventAdminRemove:
			return Outcome{Next: StateRemoved, Changed: true, Reason: "removed by an operator",
				Effects: []Effect{EffectCredentialsInvalid, EffectPeersDirty, EffectAudit}}
		}

	case StateRemoved:
		// A removed node is the end of the line. Only a fresh enrollment creates a new record.
		return Outcome{Next: state, Reason: "this node was removed"}

	default:
		return Outcome{Next: state, Reason: fmt.Sprintf("unknown state %q", state)}
	}

	return Outcome{Next: state, Reason: fmt.Sprintf("event %q does not apply in state %q", event, state)}
}

// RetryPolicy decides when the next attempt inside one cycle happens.
type RetryPolicy struct {
	// Attempts is how many tries one cycle makes. Defaults to 3.
	Attempts int
	// Gap is the pause between attempts. Defaults to 10 minutes.
	Gap int
}

// NextAttempt returns the wait before the next attempt and whether there is one.
func (p RetryPolicy) NextAttempt(attempt int) (int, bool) {
	attempts, gap := p.Attempts, p.Gap
	if attempts <= 0 {
		attempts = 3
	}
	if gap <= 0 {
		gap = 10
	}
	if attempt >= attempts {
		return 0, false
	}
	return gap, true
}
