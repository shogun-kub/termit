package server_test

import (
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
	"github.com/shogun-kub/termit/internal/mn/mesh"
	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/mn/store"
)

var peerKey = []byte("0123456789abcdef0123456789abcdef")

// testLogger keeps the puller's warnings out of the test output.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.DiscardHandler)
}

// peerFixture is a server with nothing wired but what replication needs: the registry, the key both
// servers share, and the API prefix a peer's url has to carry.
type peerFixture struct {
	handler http.Handler
	db      *store.Store
	prefix  string
}

func newPeerFixture(t *testing.T) *peerFixture {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := enroll.New(enroll.Options{
		Store: db, Mode: config.ModeInvite, Resolvers: []enroll.Resolver{staticResolver{}},
		Prober: okProber{}, Certs: okCerts{}, Master: peerKey,
	})
	if err != nil {
		t.Fatalf("enroll.New: %v", err)
	}
	handler, err := server.New(server.Options{
		Store: db, Enrollment: service, Domain: "mn.example.com", APIPrefix: "/p9x", MasterKey: peerKey,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return &peerFixture{handler: handler, db: db, prefix: "/p9x"}
}

// node records a node the way enrollment would, so the journal has something to carry.
func (f *peerFixture) node(t *testing.T, id, domain string) {
	t.Helper()
	err := f.db.CreateUV(context.Background(), store.UV{
		ID: id, Label: "peer node", Domain: domain, SelfUUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
		ProfileJSON: `{"network":"xhttp"}`, State: store.StatePending, CreatedAt: time.Now().UTC(),
		AgentKeyHash: "argon2id$fake", CredentialEpoch: 1,
	})
	if err != nil {
		t.Fatalf("CreateUV(%s): %v", id, err)
	}
}

func (f *peerFixture) get(t *testing.T, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, f.prefix+path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

func TestThePeerEndpointsNeedTheMeshCredential(t *testing.T) {
	t.Parallel()
	fx := newPeerFixture(t)
	for _, path := range []string{"/peer/snapshot", "/peer/events"} {
		for _, token := range []string{"", "not-the-token", mesh.PullToken([]byte("a different master key 0123456"))} {
			if code := fx.get(t, path, token).Code; code != http.StatusUnauthorized {
				t.Errorf("GET %s with token %q = %d, want 401", path, token, code)
			}
		}
	}
	for _, path := range []string{"/peer/snapshot", "/peer/events"} {
		if code := fx.get(t, path, mesh.PullToken(peerKey)).Code; code != http.StatusOK {
			t.Errorf("GET %s with the mesh token = %d, want 200", path, code)
		}
	}
}

func TestThePeerSnapshotIsTheRegistry(t *testing.T) {
	t.Parallel()
	fx := newPeerFixture(t)
	fx.node(t, "uv_one", "one.example.com")
	fx.node(t, "uv_two", "two.example.com")

	recorder := fx.get(t, "/peer/snapshot", mesh.PullToken(peerKey))
	var snap store.Snapshot
	if err := json.NewDecoder(recorder.Body).Decode(&snap); err != nil {
		t.Fatalf("decode the snapshot: %v", err)
	}
	if len(snap.Nodes) != 2 {
		t.Fatalf("the snapshot carries %d nodes, want 2", len(snap.Nodes))
	}
	mnID, err := fx.db.MeshID(context.Background())
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if snap.MNID != mnID {
		t.Errorf("snapshot mn_id = %q, want %q", snap.MNID, mnID)
	}
}

func TestThePeerEventsPageByCursor(t *testing.T) {
	t.Parallel()
	fx := newPeerFixture(t)
	fx.node(t, "uv_one", "one.example.com")
	fx.node(t, "uv_two", "two.example.com")

	first := fx.get(t, "/peer/events?after=0&limit=1", mesh.PullToken(peerKey))
	var page mesh.Events
	if err := json.NewDecoder(first.Body).Decode(&page); err != nil {
		t.Fatalf("decode the journal: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("after=0&limit=1 returned %d events, want 1", len(page.Events))
	}
	if page.Events[0].Seq != 1 {
		t.Errorf("the first event has seq %d, want 1", page.Events[0].Seq)
	}
	if page.LastSeq != 2 {
		t.Errorf("last_seq = %d, want 2", page.LastSeq)
	}

	second := fx.get(t, "/peer/events?after=1", mesh.PullToken(peerKey))
	page = mesh.Events{}
	if err := json.NewDecoder(second.Body).Decode(&page); err != nil {
		t.Fatalf("decode the second page: %v", err)
	}
	if len(page.Events) == 0 || page.Events[0].Seq != 2 {
		t.Fatalf("after=1 starts at %+v, want seq 2", page.Events)
	}

	for _, query := range []string{"?after=yesterday", "?after=-1", "?limit=0", "?limit=many"} {
		if code := fx.get(t, "/peer/events"+query, mesh.PullToken(peerKey)).Code; code != http.StatusBadRequest {
			t.Errorf("GET /peer/events%s = %d, want 400", query, code)
		}
	}
}

// TestThePullerCopiesTheRegistry is the seam: a real handler on one side, a real puller on the other, two
// stores in one process. The first pull has no cursor and must take a snapshot; every later one must move
// only what changed.
func TestThePullerCopiesTheRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := newPeerFixture(t)
	home.node(t, "uv_one", "one.example.com")
	home.node(t, "uv_two", "two.example.com")

	peer := httptest.NewServer(home.handler)
	defer peer.Close()

	replica, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/replica.db"})
	if err != nil {
		t.Fatalf("Open the replica: %v", err)
	}
	defer func() { _ = replica.Close() }()

	puller := &mesh.Puller{
		Store: replica, MasterKey: peerKey, Client: peer.Client(),
		Mesh:   config.Mesh{Peers: []config.Peer{{Name: "home", URL: peer.URL + home.prefix}}},
		Logger: testLogger(t),
	}
	p := config.Peer{Name: "home", URL: peer.URL + home.prefix}

	applied, err := puller.Pull(ctx, p)
	if err != nil {
		t.Fatalf("the first pull: %v", err)
	}
	if applied != 2 {
		t.Errorf("the first pull applied %d nodes, want 2", applied)
	}
	mnID, err := home.db.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	for _, id := range []string{"uv_one", "uv_two"} {
		got, err := replica.UVByID(ctx, id)
		if err != nil {
			t.Fatalf("the replica has no %s: %v", id, err)
		}
		if got.HomeMN != mnID {
			t.Errorf("%s has home %q on the replica, want %q", id, got.HomeMN, mnID)
		}
	}

	// A change on the home travels as one journal entry, and a second pull moves exactly that.
	home.node(t, "uv_three", "three.example.com")
	if applied, err = puller.Pull(ctx, p); err != nil {
		t.Fatalf("the second pull: %v", err)
	}
	if applied != 1 {
		t.Errorf("the second pull applied %d events, want 1", applied)
	}
	if _, err := replica.UVByID(ctx, "uv_three"); err != nil {
		t.Errorf("the change did not reach the replica: %v", err)
	}
	if applied, err = puller.Pull(ctx, p); err != nil || applied != 0 {
		t.Errorf("an idle pull = %d, %v; want 0, nil", applied, err)
	}

	// A write on the replica to a row the home owns is refused, not silently overwritten later.
	if _, err := replica.SetUVState(ctx, "uv_one", store.StateActive, "peer test", "test", time.Now()); err == nil {
		t.Error("the replica accepted a state change for a node the home owns")
	} else if !strings.Contains(err.Error(), "homed by") {
		t.Errorf("the refusal reads %q, want it to name the home", err)
	}
}

// TestThePullerRefusesTheWrongKey keeps the failure an operator will hit most (two installs that do not
// share a master key) from looking like a network error.
func TestThePullerRefusesTheWrongKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := newPeerFixture(t)
	peer := httptest.NewServer(home.handler)
	defer peer.Close()

	replica, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/replica.db"})
	if err != nil {
		t.Fatalf("Open the replica: %v", err)
	}
	defer func() { _ = replica.Close() }()

	puller := &mesh.Puller{
		Store: replica, MasterKey: []byte("another master key 0123456789abcdef"), Client: peer.Client(),
		Logger: testLogger(t),
	}
	_, err = puller.Pull(ctx, config.Peer{Name: "home", URL: peer.URL + home.prefix})
	if err == nil {
		t.Fatal("the pull succeeded with a different master key")
	}
	if !strings.Contains(err.Error(), "do not share a master key") {
		t.Errorf("the error reads %q, want it to name the key", err)
	}
}
