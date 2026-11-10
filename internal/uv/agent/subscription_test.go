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
