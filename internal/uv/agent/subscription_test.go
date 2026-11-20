package agent_test

import (
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/state"
)

func TestRememberSubscription(t *testing.T) {
	t.Parallel()
	// A node enrolled by an older build has credentials but no URL: the next sync must fill it in.
	st := &state.State{Credentials: &state.Credentials{AgentKey: "key"}}
	answer := proto.SyncResponse{SubscriptionURL: "https://mn.example/sub/token"}
	if !agent.RememberSubscription(st, answer) {
		t.Fatal("RememberSubscription reported no change for an empty URL")
	}
	if st.Credentials.SubscriptionURL != answer.SubscriptionURL {
		t.Errorf("stored %q, want %q", st.Credentials.SubscriptionURL, answer.SubscriptionURL)
	}
	if agent.RememberSubscription(st, answer) {
		t.Error("RememberSubscription reported a change for the same URL")
	}
	if agent.RememberSubscription(st, proto.SyncResponse{}) {
		t.Error("RememberSubscription cleared the URL on an answer without one")
	}

	// No credentials means a node that is not enrolled: there is nothing to remember against.
	if agent.RememberSubscription(&state.State{}, answer) {
		t.Error("RememberSubscription changed a state without credentials")
	}
}

func TestRememberProbe(t *testing.T) {
	t.Parallel()
	// A node rebuilt with --relocate has an agent key but no probe credential: the next sync must
	// restore it, or the next config rewrite drops the server's way in.
	st := &state.State{Credentials: &state.Credentials{AgentKey: "key"}}
	answer := proto.SyncResponse{}
	answer.Self.ProbeUUID = "11111111-2222-4333-8444-555555555555"
	if !agent.RememberProbe(st, answer) {
		t.Fatal("RememberProbe reported no change for an empty probe uuid")
	}
	if st.Credentials.ProbeUUID != answer.Self.ProbeUUID {
		t.Errorf("stored %q, want %q", st.Credentials.ProbeUUID, answer.Self.ProbeUUID)
	}
	if agent.RememberProbe(st, answer) {
		t.Error("RememberProbe reported a change for the same probe uuid")
	}
	if agent.RememberProbe(st, proto.SyncResponse{}) {
		t.Error("RememberProbe cleared the probe uuid on an answer without one")
	}

	// No credentials means a node that is not enrolled: there is nothing to remember against.
	if agent.RememberProbe(&state.State{}, answer) {
		t.Error("RememberProbe changed a state without credentials")
	}
}
