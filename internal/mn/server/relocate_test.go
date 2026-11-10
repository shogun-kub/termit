package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

func (f *fixture) relocate(t *testing.T, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/relocate", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5555"
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

// relocateBody is the request a box at domain sends after it moved: its own endpoint as it now
// serves it.
func relocateBody(t *testing.T, domain, uuid, path string) string {
	t.Helper()
	url, err := proto.BuildVLESSUrl(proto.Profile{Domain: domain, UUID: uuid, Path: path, Label: domain})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	body, err := json.Marshal(proto.RelocateRequest{Domain: domain, BasicVLESSURL: url})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

func TestRelocateMovesAnActiveNodeToANewDomain(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	const newUUID = "11111111-2222-4333-8444-555555555555"

	recorder := fx.relocate(t, fx.agentKey, relocateBody(t, "three.example.com", newUUID, "/moved/"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var answer proto.RelocateResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer.Node.Domain != "three.example.com" || answer.Node.State != string(store.StateActive) {
		t.Errorf("answer = %+v", answer.Node)
	}
	if answer.Notice == "" {
		t.Error("a successful move should say where the node went")
	}

	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.Domain != "three.example.com" {
		t.Errorf("domain = %q, want the new one", node.Domain)
	}
	if node.SelfUUID != newUUID {
		t.Errorf("self uuid = %q, want the moved box's", node.SelfUUID)
	}
	if !strings.Contains(node.ProfileJSON, "/moved/") {
		t.Errorf("profile was not replaced: %s", node.ProfileJSON)
	}
	if node.CertSHA256 != strings.Repeat("a", 64) {
		t.Errorf("cert pin = %q, want the one the checks observed", node.CertSHA256)
	}
	// The node proved it works, so the state table leaves an active node alone.
	if node.State != store.StateActive || node.FailedCycles != 0 {
		t.Errorf("state = %s/%d, want active with no failed cycles", node.State, node.FailedCycles)
	}
	if count, err := fx.store.AuditCount(context.Background(), fx.nodeID); err != nil {
		t.Fatalf("audit count: %v", err)
	} else if count != 1 {
		t.Errorf("audit records = %d, want one relocate record", count)
	}
}

func TestRelocateReadmitsABlockedNode(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	fx.block(t)

	recorder := fx.relocate(t, fx.agentKey,
		relocateBody(t, "three.example.com", "11111111-2222-4333-8444-555555555555", "/moved/"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.State != store.StateActive {
		t.Errorf("state = %s, a verified move should re-admit the node", node.State)
	}
	if node.FailedCycles != 0 {
		t.Errorf("failed cycles = %d, want 0", node.FailedCycles)
	}
}

func TestRelocateKeepsTheOldRecordWhenTheChecksFail(t *testing.T) {
	t.Parallel()
	fx := newFixtureWithProber(t, failingProber{})

	recorder := fx.relocate(t, fx.agentKey,
		relocateBody(t, "three.example.com", "11111111-2222-4333-8444-555555555555", "/moved/"))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var problem proto.Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem.Code != proto.CodeCheckFailed {
		t.Errorf("code = %q, want %q", problem.Code, proto.CodeCheckFailed)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.Domain != "one.example.com" || node.SelfUUID != "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5" {
		t.Errorf("a failed check moved the record: %+v", node)
	}
	if count, err := fx.store.AuditCount(context.Background(), fx.nodeID); err != nil {
		t.Fatalf("audit count: %v", err)
	} else if count != 1 {
		t.Errorf("audit records = %d, want one failure record", count)
	}
}

func TestRelocateRefusesADomainOwnedByAnotherNode(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)

	// two.example.com belongs to uv_two; its self UUID is uv_two's, so the URL is consistent.
	recorder := fx.relocate(t, fx.agentKey,
		relocateBody(t, "two.example.com", "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", "/other/"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.Domain != "one.example.com" {
		t.Errorf("domain = %q, a taken domain must not move the node", node.Domain)
	}
}

func TestRelocateRefusesTheSameDomain(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)

	recorder := fx.relocate(t, fx.agentKey,
		relocateBody(t, "one.example.com", "11111111-2222-4333-8444-555555555555", "/moved/"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "unblock") {
		t.Errorf("the refusal should point at unblock: %s", recorder.Body)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.SelfUUID != "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5" {
		t.Error("a refused move changed the identity")
	}
}

func TestRelocateRefusesAPendingNode(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	if _, err := fx.store.SetUVState(context.Background(), fx.nodeID, store.StatePending, "waiting", "test", testNow); err != nil {
		t.Fatalf("pending: %v", err)
	}

	recorder := fx.relocate(t, fx.agentKey,
		relocateBody(t, "three.example.com", "11111111-2222-4333-8444-555555555555", "/moved/"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.Domain != "one.example.com" {
		t.Errorf("domain = %q, a pending node must not move", node.Domain)
	}
}

func TestRelocateRequiresTheURLToNameTheDomain(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)

	body := relocateBody(t, "one.example.com", "11111111-2222-4333-8444-555555555555", "/moved/")
	body = strings.Replace(body, `"domain":"one.example.com"`, `"domain":"three.example.com"`, 1)
	if recorder := fx.relocate(t, fx.agentKey, body); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if recorder := fx.relocate(t, fx.agentKey, `{"domain":"three.example.com"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, a missing URL must be refused", recorder.Code)
	}
}

func TestRelocateRejectsARemovedNode(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	if _, err := fx.store.SetUVState(context.Background(), fx.nodeID, store.StateRemoved, "removed", "operator", testNow); err != nil {
		t.Fatalf("remove: %v", err)
	}
	recorder := fx.relocate(t, fx.agentKey,
		relocateBody(t, "three.example.com", "11111111-2222-4333-8444-555555555555", "/moved/"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestRelocateNeedsTheAgentKey(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	body := relocateBody(t, "three.example.com", "11111111-2222-4333-8444-555555555555", "/moved/")
	if recorder := fx.relocate(t, "", body); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if recorder := fx.relocate(t, "not-the-key", body); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}
