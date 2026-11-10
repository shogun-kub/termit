package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/secret"
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
