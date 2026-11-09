package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// failingProber is a node that answers wrongly, or a prober that cannot answer at all.
type failingProber struct {
	unknown bool
}

func (p failingProber) Probe(context.Context, enroll.Target) enroll.Outcome {
	if p.unknown {
		return enroll.Outcome{Unknown: true}
	}
	return enroll.Outcome{Class: "tunnel_closed", Err: errors.New("the node closed the tunnel")}
}

func (f *fixture) unblock(t *testing.T, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/unblock", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5555"
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

// block leaves the node in the state Scenario B starts from.
func (f *fixture) block(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.SetUVState(ctx, f.nodeID, store.StateBlocked, "two failed cycles", "scheduler", testNow); err != nil {
		t.Fatalf("block: %v", err)
	}
	if err := f.store.SetFailedCycles(ctx, f.nodeID, 2); err != nil {
		t.Fatalf("failed cycles: %v", err)
	}
}

func TestUnblockRestoresABlockedNodeAfterTheChecksPass(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	fx.block(t)

	recorder := fx.unblock(t, fx.agentKey, `{"domain":"one.example.com"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var answer proto.UnblockResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer.Node.State != string(store.StateActive) {
		t.Errorf("answer state = %q", answer.Node.State)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.State != store.StateActive {
		t.Errorf("stored state = %s, want active", node.State)
	}
	if node.FailedCycles != 0 {
		t.Errorf("failed cycles = %d, want 0", node.FailedCycles)
	}
	if count, err := fx.store.AuditCount(context.Background(), fx.nodeID); err != nil {
		t.Fatalf("audit count: %v", err)
	} else if count != 1 {
		t.Errorf("audit records = %d, want one unblock record", count)
	}
}

func TestUnblockAcceptsANewURLForTheSameHost(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	fx.block(t)

	url, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: "one.example.com",
		UUID:   "0f0e0d0c-0b0a-4908-8706-050403020100",
		Path:   "/rebuilt/",
		Label:  "one.example.com",
	})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	body, _ := json.Marshal(proto.UnblockRequest{Domain: "one.example.com", BasicVLESSURL: url})
	recorder := fx.unblock(t, fx.agentKey, string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.SelfUUID != "0f0e0d0c-0b0a-4908-8706-050403020100" {
		t.Errorf("self uuid = %q, want the rebuilt box's", node.SelfUUID)
	}
	if !strings.Contains(node.ProfileJSON, "/rebuilt/") {
		t.Errorf("profile was not replaced: %s", node.ProfileJSON)
	}
}

func TestUnblockKeepsTheNodeBlockedWhenTheChecksFail(t *testing.T) {
	t.Parallel()
	fx := newFixtureWithProber(t, failingProber{})
	fx.block(t)

	recorder := fx.unblock(t, fx.agentKey, `{}`)
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
	if node.State != store.StateBlocked {
		t.Errorf("state = %s, a failed check must not unblock", node.State)
	}
	if count, err := fx.store.AuditCount(context.Background(), fx.nodeID); err != nil {
		t.Fatalf("audit count: %v", err)
	} else if count != 1 {
		t.Errorf("audit records = %d, want one failure record", count)
	}
}

func TestUnblockKeepsTheNodeBlockedWhenTheProberIsUnknown(t *testing.T) {
	t.Parallel()
	fx := newFixtureWithProber(t, failingProber{unknown: true})
	fx.block(t)

	recorder := fx.unblock(t, fx.agentKey, `{}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.State != store.StateBlocked {
		t.Errorf("state = %s, our own failure must not unblock a node", node.State)
	}
}

func TestUnblockRefusesADifferentDomain(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	fx.block(t)

	recorder := fx.unblock(t, fx.agentKey, `{"domain":"two.example.com"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var problem proto.Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if problem.Code != proto.CodeRelocateRequired {
		t.Errorf("code = %q, want %q", problem.Code, proto.CodeRelocateRequired)
	}
	node, err := fx.store.UVByID(context.Background(), fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.State != store.StateBlocked {
		t.Errorf("state = %s, want blocked", node.State)
	}
}

func TestUnblockIsIdempotentForAnActiveNode(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)

	recorder := fx.unblock(t, fx.agentKey, `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var answer proto.UnblockResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer.Notice == "" {
		t.Error("an already-active node should say that nothing changed")
	}
}

func TestUnblockRejectsARemovedNode(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	if _, err := fx.store.SetUVState(context.Background(), fx.nodeID, store.StateRemoved, "removed", "operator", testNow); err != nil {
		t.Fatalf("remove: %v", err)
	}
	recorder := fx.unblock(t, fx.agentKey, `{}`)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestUnblockNeedsTheAgentKey(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	fx.block(t)
	if recorder := fx.unblock(t, "", `{}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if recorder := fx.unblock(t, "not-the-key", `{}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestSyncRecordsTheSelfCheck(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	body := `{"agent_version":"1","check":{"ok":false,"error_class":"xray_stopped","at":"2026-11-09T12:00:00Z"}}`
	if recorder := fx.sync(t, fx.agentKey, body); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	check, err := fx.store.LatestCheck(context.Background(), fx.nodeID, "self")
	if err != nil {
		t.Fatalf("LatestCheck: %v", err)
	}
	if check.Kind != "self" || check.OK || check.ErrorClass != "xray_stopped" {
		t.Errorf("self-check = %+v", check)
	}
}
