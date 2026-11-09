package server_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/cred"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

var (
	testNow   = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	masterKey = []byte("0123456789abcdef0123456789abcdef")
)

// fixture is a running API with two enrolled nodes.
type fixture struct {
	handler  http.Handler
	store    *store.Store
	agentKey string
	nodeID   string
	peerID   string
}

type staticResolver struct{}

func (staticResolver) Lookup(ctx context.Context, domain string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("203.0.113.10")}, nil
}

type okProber struct{}

func (okProber) Probe(ctx context.Context, target enroll.Target) enroll.Outcome {
	return enroll.Outcome{}
}

type okCerts struct{}

func (okCerts) Check(ctx context.Context, domain string) (enroll.Pin, error) {
	return enroll.Pin{Fingerprint: strings.Repeat("a", 64), NotAfter: testNow.Add(60 * 24 * time.Hour)}, nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	agentKey := "agent-key-for-tests"
	profile, err := json.Marshal(proto.Profile{Domain: "one.example.com", UUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5", Path: "/secret/"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	peerProfile, err := json.Marshal(proto.Profile{Domain: "two.example.com", UUID: "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", Path: "/other/"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, uv := range []store.UV{
		{
			ID: "uv_one", Domain: "one.example.com", SelfUUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
			ProfileJSON: string(profile), State: store.StateActive, CreatedAt: testNow,
			AgentKeyHash: secret.Digest(agentKey), ProbeUUID: "probe-one", CredentialEpoch: 1,
		},
		{
			ID: "uv_two", Domain: "two.example.com", SelfUUID: "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d",
			ProfileJSON: string(peerProfile), State: store.StateUnavailable, CreatedAt: testNow,
			ProbeUUID: "probe-two", CredentialEpoch: 1,
		},
	} {
		if err := db.CreateUV(context.Background(), uv); err != nil {
			t.Fatalf("CreateUV: %v", err)
		}
	}

	service, err := enroll.New(enroll.Options{
		Store: db, Mode: config.ModeInvite, Resolvers: []enroll.Resolver{staticResolver{}},
		Prober: okProber{}, Certs: okCerts{},
	})
	if err != nil {
		t.Fatalf("enroll.New: %v", err)
	}
	handler, err := server.New(server.Options{
		Store: db, Enrollment: service, Domain: "mn.example.com",
		MasterKey: masterKey, CheckInterval: 24 * time.Hour, SyncInterval: 24 * time.Hour,
		MinSyncDelay: 2 * time.Hour, Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return &fixture{handler: handler, store: db, agentKey: agentKey, nodeID: "uv_one", peerID: "uv_two"}
}

func (f *fixture) sync(t *testing.T, key string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/sync", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5555"
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

func TestSyncReturnsThePeerSetWithPairCredentials(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	response := fx.sync(t, fx.agentKey, `{"agent_version":"termit-uv/test"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("sync = %d: %s", response.Code, response.Body.String())
	}

	var body proto.SyncResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Self.ID != fx.nodeID || body.Self.ProbeUUID != "probe-one" {
		t.Errorf("self = %+v", body.Self)
	}
	if len(body.Peers) != 1 {
		t.Fatalf("the response has %d peers, want 1", len(body.Peers))
	}
	peer := body.Peers[0]
	if peer.ID != fx.peerID || peer.Domain != "two.example.com" {
		t.Errorf("peer = %+v", peer)
	}
	if !peer.Degraded || peer.State != string(store.StateUnavailable) {
		t.Errorf("an unavailable peer is not marked degraded: %+v", peer)
	}
	// The credential is the pair credential, not the peer's self credential.
	double, err := cred.Pair(masterKey, fx.nodeID, fx.peerID, 1)
	if err != nil {
		t.Fatalf("cred.Pair: %v", err)
	}
	if peer.UUID != double {
		t.Errorf("peer credential = %s, want the pair credential %s", peer.UUID, double)
	}
	if peer.UUID == "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d" {
		t.Error("the response leaked the peer's self credential")
	}

	// The timing hints are present, and the next sync is in the future.
	if body.Policy.CheckIntervalSeconds != int((24 * time.Hour).Seconds()) {
		t.Errorf("policy = %+v", body.Policy)
	}
	if !body.NextSyncAfter.After(testNow) {
		t.Errorf("next_sync_after = %v, want a future time", body.NextSyncAfter)
	}
	if body.APIVersion != proto.APIVersion {
		t.Errorf("api_version = %q", body.APIVersion)
	}
}

func TestSyncIsStableAndOrdered(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	first := fx.sync(t, fx.agentKey, `{}`).Body.String()
	second := fx.sync(t, fx.agentKey, `{}`).Body.String()
	if first != second {
		t.Error("two syncs with the same registry produced different bodies")
	}
}

func TestSyncRefusesAnUnknownAgent(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	for _, tc := range []struct {
		name string
		key  string
		want int
	}{
		{"no key", "", http.StatusUnauthorized},
		{"wrong key", "not-the-key", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := fx.sync(t, tc.key, `{}`)
			if response.Code != tc.want {
				t.Fatalf("sync = %d, want %d", response.Code, tc.want)
			}
			var problem proto.Problem
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if problem.Code != proto.CodeUnauthorized {
				t.Errorf("code = %s, want %s", problem.Code, proto.CodeUnauthorized)
			}
		})
	}
}

func TestSyncRefusesARemovedNode(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	if _, err := fx.store.SetUVState(context.Background(), fx.nodeID, store.StateRemoved, "test", "test", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	response := fx.sync(t, fx.agentKey, `{}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("sync = %d, want 403 for a removed node", response.Code)
	}
}

func TestSyncRejectsAnUnknownField(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	response := fx.sync(t, fx.agentKey, `{"agent_version":"x","nonsense":1}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("sync = %d, want 400 for an unknown field", response.Code)
	}
}
