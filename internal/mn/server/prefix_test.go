package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// prefixHandler builds a handler with the given api_prefix and nothing else wired: these tests ask
// where a path lives, not what the handler does with it.
func prefixHandler(t *testing.T, prefix string) http.Handler {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := enroll.New(enroll.Options{
		Store: db, Mode: config.ModeInvite, Resolvers: []enroll.Resolver{staticResolver{}},
		Prober: okProber{}, Certs: okCerts{}, Master: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("enroll.New: %v", err)
	}
	handler, err := server.New(server.Options{
		Store: db, Enrollment: service, Domain: "mn.example.com", APIPrefix: prefix,
		MasterKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler
}

func statusOf(handler http.Handler, method, path string) int {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Code
}

// The point of the prefix: a probe that tries the documented names finds nothing. A 404 is the answer
// that carries no information, and it is what every old path must return.
func TestPrefixHidesTheOldPaths(t *testing.T) {
	t.Parallel()
	handler := prefixHandler(t, "/k3f9x2q7m1zp")
	for _, path := range []string{"/api/v1/enroll", "/api/v1/agent/sync", "/healthz"} {
		if got := statusOf(handler, http.MethodPost, path); got != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, got)
		}
	}
	// And the prefix itself answers: the enrollment handler is reached, and refuses a request without an
	// invite rather than vanishing.
	if got := statusOf(handler, http.MethodPost, "/k3f9x2q7m1zp/enroll"); got == http.StatusNotFound {
		t.Errorf("the prefixed enroll path is not routed")
	}
	if got := statusOf(handler, http.MethodGet, "/k3f9x2q7m1zp/healthz"); got != http.StatusOK {
		t.Errorf("the prefixed health path = %d", got)
	}
}

// A configuration without an api_prefix keeps the path it has always had: an upgrade must not move the
// API under the operators who did not ask for it.
func TestPrefixDefaultsToTheHistoricalPath(t *testing.T) {
	t.Parallel()
	handler := prefixHandler(t, "")
	if got := statusOf(handler, http.MethodGet, config.DefaultAPIPrefix+"/healthz"); got != http.StatusOK {
		t.Errorf("%s/healthz = %d", config.DefaultAPIPrefix, got)
	}
}

// Metrics and subscriptions are not behind the prefix, for reasons the doc states: a scrape
// configuration and a URL already in a client's hands.
func TestPrefixLeavesMetricsAndSubscriptionsWhereTheyWere(t *testing.T) {
	t.Parallel()
	handler := prefixHandler(t, "/k3f9x2q7m1zp")
	// Off loopback, metrics refuse rather than 404: the path is not a secret, the access is.
	if got := statusOf(handler, http.MethodGet, "/metrics"); got != http.StatusForbidden {
		t.Errorf("/metrics = %d, want 403 off loopback", got)
	}
	// A subscription path with a token the store does not know answers with the subscription problem,
	// not with the plain 404 of an unrouted path: the endpoint is reachable, the token is not.
	req := httptest.NewRequest(http.MethodGet, "/sub/unknown-token", nil)
	req.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if body := recorder.Body.String(); !strings.Contains(body, "subscription_not_found") {
		t.Errorf("/sub/{token} answered %d %s, want the subscription problem", recorder.Code, body)
	}
}
