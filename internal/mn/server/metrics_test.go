package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// vlessURL builds the profile URL an enrolling node sends, in the one shape the server accepts.
func vlessURL(t *testing.T, domain, uuid, path string) string {
	t.Helper()
	url, err := proto.BuildVLESSUrl(proto.Profile{Domain: domain, UUID: uuid, Path: path, Label: domain})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	return url
}

// enrollService is the enrollment service the fixture uses, rebuilt here so a test can attach its own
// logger or prober.
func enrollService(t *testing.T, db *store.Store) *enroll.Service {
	t.Helper()
	service, err := enroll.New(enroll.Options{
		Store: db, Mode: config.ModeInvite, Resolvers: []enroll.Resolver{staticResolver{}},
		Prober: okProber{}, Certs: okCerts{}, Master: masterKey,
	})
	if err != nil {
		t.Fatalf("enroll.New: %v", err)
	}
	return service
}

// scrape fetches /metrics from a loopback peer, which is the only peer allowed to.
func (f *fixture) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics = %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func TestMetricsReportTheMeshWithoutNamingIt(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)

	if rec := fx.sync(t, fx.agentKey, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("sync = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := fx.sync(t, "not-the-key", `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sync with a bad key = %d, want 401", rec.Code)
	}

	body := fx.scrape(t)
	for _, want := range []string{
		`termit_mn_syncs_total{result="ok"} 1`,
		`termit_mn_syncs_total{result="refused"} 1`,
		`termit_mn_nodes{state="active"} 1`,
		`termit_mn_nodes{state="unavailable"} 1`,
		`termit_mn_prober_up 1`,
		`termit_mn_up 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%q is missing from:\n%s", want, body)
		}
	}

	// The endpoint is scraped by a monitoring agent and is not an inventory: a domain or an id in a
	// label would put the mesh's topology into whatever stores the metrics.
	for _, forbidden := range []string{"one.example.com", "two.example.com", "uv_one", "uv_two", fx.agentKey} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics leak %q:\n%s", forbidden, body)
		}
	}
}

func TestMetricsRefuseANonLoopbackPeer(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	fx.handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "termit_mn_") {
		t.Error("a refused scrape still returned samples")
	}
}

func TestEnrollmentOutcomesAreCounted(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()

	// A refusal: no such invite.
	body, err := json.Marshal(proto.EnrollRequest{
		InviteCode:    "WRONG-CODE",
		Domain:        "three.example.com",
		BasicVLESSURL: vlessURL(t, "three.example.com", "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", "/three/"),
		AgentVersion:  "termit-uv/test",
		Profile:       "v1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enroll", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	fx.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("enroll with a bad invite = %d: %s", recorder.Code, recorder.Body.String())
	}

	// An admission: a real invite, and a node the resolver places on the caller's address.
	invite := "GOOD-CODE-1234"
	if err := fx.store.CreateInvite(ctx, store.Invite{
		ID: "inv_1", CodeHash: secret.Digest(invite), CreatedAt: testNow, MaxUses: 1,
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	body, err = json.Marshal(proto.EnrollRequest{
		InviteCode:    invite,
		Domain:        "three.example.com",
		BasicVLESSURL: vlessURL(t, "three.example.com", "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", "/three/"),
		AgentVersion:  "termit-uv/test",
		Profile:       "v1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/enroll", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5555"
	recorder = httptest.NewRecorder()
	fx.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enroll = %d: %s", recorder.Code, recorder.Body.String())
	}

	bodyAfter := fx.scrape(t)
	for _, want := range []string{
		`termit_mn_enrollments_total{result="refused"} 1`,
		`termit_mn_enrollments_total{result="ok"} 1`,
		`termit_mn_enrollments_total{result="pending"} 0`,
	} {
		if !strings.Contains(bodyAfter, want) {
			t.Errorf("%q is missing from:\n%s", want, bodyAfter)
		}
	}
}

func TestLogsCarryIdentifiersAndNotSecrets(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service := enrollService(t, db)
	handler, err := server.New(server.Options{
		Store: db, Enrollment: service, Domain: "mn.example.com", Logger: logger,
		MasterKey: masterKey, CheckInterval: 24 * time.Hour, SyncInterval: 24 * time.Hour,
		MinSyncDelay: 2 * time.Hour, Now: func() time.Time { return testNow }, ProberUp: true,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	invite := "SECRET-INVITE-CODE"
	if err := db.CreateInvite(context.Background(), store.Invite{
		ID: "inv_1", CodeHash: secret.Digest(invite), CreatedAt: testNow, MaxUses: 1,
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	const selfUUID = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	body, err := json.Marshal(proto.EnrollRequest{
		InviteCode:    invite,
		Domain:        "node.example.com",
		BasicVLESSURL: vlessURL(t, "node.example.com", selfUUID, "/node/"),
		AgentVersion:  "termit-uv/test",
		Profile:       "v1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enroll", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enroll = %d: %s", recorder.Code, recorder.Body.String())
	}

	var answer proto.EnrollResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the enrollment: %v", err)
	}

	syncReq := httptest.NewRequest(http.MethodPost, "/api/v1/agent/sync", strings.NewReader(`{}`))
	syncReq.RemoteAddr = "203.0.113.10:5555"
	syncReq.Header.Set("Authorization", "Bearer "+answer.AgentKey)
	handler.ServeHTTP(httptest.NewRecorder(), syncReq)

	output := logs.String()
	if output == "" {
		t.Fatal("nothing was logged, so this test proves nothing")
	}
	// The identifiers an operator needs are there.
	for _, want := range []string{"node enrolled", answer.Node.ID, "sync served"} {
		if !strings.Contains(output, want) {
			t.Errorf("logs are missing %q:\n%s", want, output)
		}
	}
	// The credentials printed once in the response are not.
	for _, secret := range []string{answer.AgentKey, answer.SubToken, answer.ProbeUUID, invite, selfUUID} {
		if strings.Contains(output, secret) {
			t.Errorf("logs leak %q:\n%s", secret, output)
		}
	}
}
