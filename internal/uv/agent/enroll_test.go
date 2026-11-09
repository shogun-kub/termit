package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
)

func enrollmentAnswer() proto.EnrollResponse {
	response := proto.EnrollResponse{
		APIVersion:      proto.APIVersion,
		AgentKey:        "agent-key-from-server",
		SubToken:        "sub-token-from-server",
		ProbeUUID:       "11111111-2222-4333-8444-555555555555",
		SubscriptionURL: "/sub/sub-token-from-server",
	}
	response.Node = proto.Node{ID: "uv_new", State: "active", Domain: "node.example.com"}
	return response
}

func TestEnrollSendsWhatTheServerNeeds(t *testing.T) {
	t.Parallel()
	var got proto.EnrollRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/enroll" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(enrollmentAnswer())
	}))
	defer server.Close()

	answer, err := agent.Remote{BaseURL: server.URL}.Enroll(context.Background(), agent.EnrollRequest{
		InviteCode:    "  CODE-1234  ",
		Domain:        "node.example.com",
		BasicVLESSURL: "vless://uuid@node.example.com:443?type=xhttp",
		AgentVersion:  "termit-uv/test",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if got.InviteCode != "CODE-1234" {
		t.Errorf("invite code = %q, want it trimmed", got.InviteCode)
	}
	if got.Domain != "node.example.com" || got.Profile != proto.ProfileV1 {
		t.Errorf("request = %+v", got)
	}
	if answer.AgentKey != "agent-key-from-server" || answer.Node.ID != "uv_new" {
		t.Errorf("answer = %+v", answer)
	}
}

func TestEnrollReportsTheServersRefusal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(proto.NewProblem(http.StatusConflict, proto.CodeDNSMismatch, "Conflict",
			"node.example.com does not resolve to 203.0.113.5"))
	}))
	defer server.Close()

	_, err := agent.Remote{BaseURL: server.URL}.Enroll(context.Background(), agent.EnrollRequest{
		Domain: "node.example.com", BasicVLESSURL: "vless://x@node.example.com:443",
	})
	var refusal *agent.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a refusal", err)
	}
	if refusal.Problem.Code != proto.CodeDNSMismatch {
		t.Errorf("code = %q", refusal.Problem.Code)
	}
	if !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("the refusal lost the server's detail: %v", err)
	}
}

func TestEnrollRefusesAnAnswerWithoutCredentials(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(proto.EnrollResponse{})
	}))
	defer server.Close()

	_, err := agent.Remote{BaseURL: server.URL}.Enroll(context.Background(), agent.EnrollRequest{
		Domain: "node.example.com", BasicVLESSURL: "vless://x@node.example.com:443",
	})
	if err == nil {
		t.Fatal("Enroll accepted a 201 with no credentials")
	}
}

func TestEnrollNeedsAServerAndTheNodesOwnURL(t *testing.T) {
	t.Parallel()
	if _, err := (agent.Remote{}).Enroll(context.Background(), agent.EnrollRequest{Domain: "d", BasicVLESSURL: "u"}); err == nil {
		t.Fatal("Enroll ran without a server")
	}
	if _, err := (agent.Remote{BaseURL: "https://mn.example.com"}).Enroll(context.Background(), agent.EnrollRequest{Domain: "d"}); err == nil {
		t.Fatal("Enroll ran without the node's own URL")
	}
}
