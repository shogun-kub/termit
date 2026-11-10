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
	"github.com/shogun-kub/termit/internal/mn/metrics"
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
	registry *metrics.Registry
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
	return newFixtureWithProber(t, okProber{})
}

// newFixtureWithProber is the same fixture with a prober whose verdict the test controls: the
// unblock tests need a node that fails its checks and one that cannot be checked at all.
func newFixtureWithProber(t *testing.T, prober enroll.Prober) *fixture {
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
		Prober: prober, Certs: okCerts{}, Master: masterKey,
	})
	if err != nil {
		t.Fatalf("enroll.New: %v", err)
	}
	registry := metrics.New(testNow)
	handler, err := server.New(server.Options{
		Store: db, Enrollment: service, Domain: "mn.example.com",
		MasterKey: masterKey, CheckInterval: 24 * time.Hour, SyncInterval: 24 * time.Hour,
		MinSyncDelay: 2 * time.Hour, Now: func() time.Time { return testNow },
		Metrics: registry, ProberUp: true,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return &fixture{handler: handler, store: db, registry: registry, agentKey: agentKey,
		nodeID: "uv_one", peerID: "uv_two"}
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

// syncWithPin is sync with the node's certificate report, the header a real agent sets on every call.
func (f *fixture) syncWithPin(t *testing.T, key, pin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/sync", strings.NewReader("{}"))
	req.RemoteAddr = "203.0.113.10:5555"
	req.Header.Set("Authorization", "Bearer "+key)
	if pin != "" {
		req.Header.Set(proto.HeaderCertSHA256, pin)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

func TestSyncFollowsTheCertificateTheNodeReports(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()

	// A node that renewed its certificate reports the new leaf; the server must pin that leaf, or
	// every later check would close the tunnel on a mismatch.
	renewed := strings.Repeat("b", 64)
	if rec := fx.syncWithPin(t, fx.agentKey, renewed); rec.Code != http.StatusOK {
		t.Fatalf("sync = %d: %s", rec.Code, rec.Body.String())
	}
	node, err := fx.store.UVByID(ctx, fx.nodeID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.CertSHA256 != renewed {
		t.Errorf("pin = %q, want the reported certificate", node.CertSHA256)
	}

	// A malformed report is ignored: a bad header must not be able to break a node's checks.
	for _, bad := range []string{"not-a-fingerprint", strings.Repeat("z", 64), strings.Repeat("b", 63)} {
		if rec := fx.syncWithPin(t, fx.agentKey, bad); rec.Code != http.StatusOK {
			t.Fatalf("sync with %q = %d: %s", bad, rec.Code, rec.Body.String())
		}
		node, err = fx.store.UVByID(ctx, fx.nodeID)
		if err != nil {
			t.Fatalf("UVByID: %v", err)
		}
		if node.CertSHA256 != renewed {
			t.Errorf("a malformed pin %q replaced the good one with %q", bad, node.CertSHA256)
		}
	}
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

func TestSyncReturnsAUsableSubscriptionURL(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	// A node gets its token at enrollment; the sealed copy is what lets the server print the URL again.
	token := "sub-token-for-tests"
	sealed, err := secret.Seal(masterKey, token)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := fx.store.SetSubTokenHash(context.Background(), fx.nodeID, secret.Digest(token), sealed); err != nil {
		t.Fatalf("SetSubTokenHash: %v", err)
	}

	var body proto.SyncResponse
	response := fx.sync(t, fx.agentKey, `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("sync = %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := "https://mn.example.com/sub/" + token
	if body.SubscriptionURL != want {
		t.Errorf("subscription_url = %q, want %q", body.SubscriptionURL, want)
	}
}

func TestSyncLeavesTheURLOutWithoutASealedToken(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	var body proto.SyncResponse
	response := fx.sync(t, fx.agentKey, `{}`)
	if response.Code != http.StatusOK {
		t.Fatalf("sync = %d", response.Code)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SubscriptionURL != "" {
		t.Errorf("subscription_url = %q for a node whose token was never stored sealed", body.SubscriptionURL)
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
