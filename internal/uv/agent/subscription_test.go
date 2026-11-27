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

func TestRememberServer(t *testing.T) {
	t.Parallel()
	// Both fields an update offer and a `support` line read: the build the server runs and the oldest
	// build it says it supports.
	st := &state.State{}
	answer := proto.SyncResponse{ServerVersion: "v0.7.0", MinAgentVersion: "v0.7.0"}
	if !agent.RememberServer(st, answer) {
		t.Fatal("RememberServer reported no change for an empty state")
	}
	if st.ServerVersion != answer.ServerVersion || st.MinAgentVersion != answer.MinAgentVersion {
		t.Errorf("stored %q / %q, want %q / %q",
			st.ServerVersion, st.MinAgentVersion, answer.ServerVersion, answer.MinAgentVersion)
	}
	if agent.RememberServer(st, answer) {
		t.Error("RememberServer reported a change for the same answer")
	}

	// The floor is the answering server's rule, so an answer without it clears the stored one: a server
	// that declares no minimum says so by sending nothing, and a warning from a floor its operator
	// removed must not outlive the rule.
	cleared := proto.SyncResponse{ServerVersion: "v0.7.1"}
	if !agent.RememberServer(st, cleared) {
		t.Fatal("RememberServer ignored an answer that dropped the floor")
	}
	if st.MinAgentVersion != "" {
		t.Errorf("min_agent_version = %q, a server that declares no floor left one behind", st.MinAgentVersion)
	}
	if st.ServerVersion != "v0.7.1" {
		t.Errorf("server_version = %q, want the version the answer carried", st.ServerVersion)
	}
	if agent.RememberServer(st, cleared) {
		t.Error("RememberServer reported a change for the same answer")
	}
}
