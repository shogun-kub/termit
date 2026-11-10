package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/cred"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/proto"
)

// TestSubscriptionAllowsAClientsRefreshes pins the subscription limit apart from the enrollment one.
// A subscription client refreshes at launch and on demand; the token is unguessable, so the counter
// exists to bound load, not to defend a secret. The assertion is deliberately at the boundary: the
// 60th request passes and the 61st is refused, so a future change to the constant is a test update,
// not a silent tightening that an owner would discover as a "429" in their client.
func TestSubscriptionAllowsAClientsRefreshes(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	token := "subscription-token-for-tests"
	sealed, err := secret.Seal(masterKey, token)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := fx.store.SetSubTokenHash(context.Background(), fx.nodeID, secret.Digest(token), sealed); err != nil {
		t.Fatalf("SetSubTokenHash: %v", err)
	}

	fetch := func() int {
		request := httptest.NewRequest(http.MethodGet, "/sub/"+token, nil)
		request.RemoteAddr = "203.0.113.10:5555"
		recorder := httptest.NewRecorder()
		fx.handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	for i := 1; i <= 60; i++ {
		if code := fetch(); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 (a client's refreshes must not be throttled)", i, code)
		}
	}
	if code := fetch(); code != http.StatusTooManyRequests {
		t.Errorf("request 61 = %d, want 429", code)
	}
}

// subscriptionUUIDs fetches the JSON rendering and maps each peer's address to the credential the
// owner's devices would use.
func (f *fixture) subscriptionUUIDs(t *testing.T, token string) (map[string]string, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/sub/"+token+"/json", nil)
	request.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("subscription = %d: %s", recorder.Code, recorder.Body)
	}
	var document struct {
		Outbounds []struct {
			Type   string `json:"type"`
			Server string `json:"server"`
			UUID   string `json:"uuid"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode the subscription: %v", err)
	}
	uuids := make(map[string]string, len(document.Outbounds))
	for _, outbound := range document.Outbounds {
		if outbound.Type == "vless" {
			uuids[outbound.Server] = outbound.UUID
		}
	}
	return uuids, recorder.Body.String()
}

// TestSubscriptionUsesPairCredentials pins the credential model: the owner's own node appears with
// its self credential, a peer with the pair credential the owner shares with it (DESCRIPTION §6.4).
// Handing out the peer's self credential would give every owner's device the peer's owner access and
// would break the promise that a leaked subscription exposes only the pairs it names (§6.5).
func TestSubscriptionUsesPairCredentials(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	token := "subscription-token-for-tests"
	sealed, err := secret.Seal(masterKey, token)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := fx.store.SetSubTokenHash(context.Background(), fx.nodeID, secret.Digest(token), sealed); err != nil {
		t.Fatalf("SetSubTokenHash: %v", err)
	}

	pair, err := cred.Pair(masterKey, fx.nodeID, fx.peerID, 1)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	uuids, _ := fx.subscriptionUUIDs(t, token)
	if got := uuids["two.example.com"]; got != pair {
		t.Error("the peer does not appear with the pair credential")
	}
	if got := uuids["two.example.com"]; got == "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d" {
		t.Error("the subscription exposed the peer's own credential")
	}
	if got := uuids["one.example.com"]; got != "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5" {
		t.Error("the owner's own node must appear with its self credential")
	}
}

// TestSubscriptionKeepsThePairCredentialAcrossARebuild is the Scenario C promise at the subscription
// level: a rebuilt node changes its host and path in every list, but the credential the peers use
// stays the one derived from the node ids.
func TestSubscriptionKeepsThePairCredentialAcrossARebuild(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	token := "subscription-token-for-tests"
	sealed, err := secret.Seal(masterKey, token)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := fx.store.SetSubTokenHash(context.Background(), fx.nodeID, secret.Digest(token), sealed); err != nil {
		t.Fatalf("SetSubTokenHash: %v", err)
	}
	before, _ := fx.subscriptionUUIDs(t, token)

	rebuilt, err := json.Marshal(proto.Profile{
		Domain: "two.example.com",
		UUID:   "11111111-2222-4333-8444-555555555555",
		Path:   "/moved/",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := fx.store.SetUVEndpoint(context.Background(), fx.peerID,
		"11111111-2222-4333-8444-555555555555", string(rebuilt), strings.Repeat("b", 64)); err != nil {
		t.Fatalf("SetUVEndpoint: %v", err)
	}

	after, body := fx.subscriptionUUIDs(t, token)
	if after["two.example.com"] != before["two.example.com"] {
		t.Error("the pair credential changed although only the endpoint did")
	}
	if !strings.Contains(body, "/moved/") {
		t.Error("the subscription did not pick up the rebuilt path")
	}
}
