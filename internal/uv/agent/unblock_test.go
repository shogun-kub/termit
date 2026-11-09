package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
)

func TestUnblockSendsTheAgentKeyAndTheNewURL(t *testing.T) {
	t.Parallel()
	var (
		gotKey  string
		gotBody proto.UnblockRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(proto.UnblockResponse{
			APIVersion: proto.APIVersion,
			Node:       proto.Node{ID: "uv_one", State: "active", Domain: "one.example.com"},
		})
	}))
	defer server.Close()

	answer, err := agent.Remote{BaseURL: server.URL}.Unblock(context.Background(), "agent-key", proto.UnblockRequest{
		Domain:        "one.example.com",
		BasicVLESSURL: "vless://example",
	})
	if err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	if gotKey != "Bearer agent-key" {
		t.Errorf("authorization = %q", gotKey)
	}
	if gotBody.BasicVLESSURL != "vless://example" {
		t.Errorf("body = %+v", gotBody)
	}
	if answer.Node.State != "active" {
		t.Errorf("answer = %+v", answer)
	}
}

func TestUnblockReportsARefusal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(proto.NewProblem(http.StatusConflict,
			proto.CodeRelocateRequired, "Conflict", "use uv migrate"))
	}))
	defer server.Close()

	_, err := agent.Remote{BaseURL: server.URL}.Unblock(context.Background(), "key", proto.UnblockRequest{})
	if err == nil {
		t.Fatal("Unblock accepted a refusal")
	}
	var refusal *agent.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a refusal", err)
	}
	if refusal.Problem.Code != proto.CodeRelocateRequired {
		t.Errorf("code = %q", refusal.Problem.Code)
	}
}

func TestUnblockRefusesToRunWithoutCredentials(t *testing.T) {
	t.Parallel()
	if _, err := (agent.Remote{BaseURL: "https://mn.example.com"}).Unblock(context.Background(), "", proto.UnblockRequest{}); err == nil {
		t.Fatal("Unblock ran without an agent key")
	}
}
