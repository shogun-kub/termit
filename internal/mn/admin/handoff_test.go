package admin_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/mesh"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// fakePeer is the other half of a handoff: it reports its own identity and progress and takes the node, or
// refuses. It knows nothing of the asking server's id, so it reports no cursor — which a handoff reads as
// "I have applied none of you", the worst case.
type fakePeer struct {
	refuse bool
	// took records the node ids it was asked for, so a test can tell "refused" from "never asked".
	took []string
}

func (f *fakePeer) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /p9x/peer/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mesh.PullToken(testMaster) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"mn_id": "mn_peer", "applied": map[string]int64{}})
	})
	mux.HandleFunc("POST /p9x/peer/handoff", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UV   string `json:"uv"`
			From string `json:"from"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.took = append(f.took, body.UV)
		if f.refuse {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"conflict"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"term": 1, "agent_key": "the-new-agent-key"})
	})
	return mux
}

// handoffService is a running admin service with one peer configured, plus the store behind it.
func handoffService(t *testing.T, peerURL string, lagMax int) (admin.Client, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateAdmin(context.Background(), store.Admin{
		ID: "admin_1", Name: "operator", TokenHash: secret.Digest(testToken), CreatedAt: testNow,
	}); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	service, err := admin.New(db)
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	service.Now = func() time.Time { return testNow }
	service.Master = testMaster
	service.Mesh = config.Mesh{
		Peers:  []config.Peer{{Name: "mn2", URL: peerURL + "/p9x"}},
		LagMax: lagMax,
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = admin.Serve(ctx, listener, service, slog.New(slog.DiscardHandler)) }()
	t.Cleanup(func() { _ = listener.Close() })
	return admin.Client{Network: "tcp", Address: listener.Addr().String(), Token: testToken}, db
}

// A target that has not caught up cannot take a node over — it does not have the row — unless the operator
// says so, and the refusal names the distance so the next step is obvious.
func TestHandoffRefusesAFarBehindPeer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, db := handoffService(t, srv.URL, 1)
	for _, node := range []store.UV{testNode("uv_one", "one.example.com"), testNode("uv_two", "two.example.com")} {
		if err := db.CreateUV(ctx, node); err != nil {
			t.Fatalf("CreateUV: %v", err)
		}
	}

	var outcome admin.HandoffOutcome
	err := client.Call(ctx, "home.handoff", map[string]any{"uv": "uv_one", "peer": "mn2"}, &outcome)
	if err == nil || !strings.Contains(err.Error(), "behind") {
		t.Fatalf("a far-behind peer = %v, want a refusal naming the distance", err)
	}
	if len(peer.took) != 0 {
		t.Errorf("the peer was asked to take the node anyway: %v", peer.took)
	}
	row, err := db.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if row.HomeMN != "" || row.HomeTerm != 0 {
		t.Errorf("the refused handoff touched the row: home %q term %d", row.HomeMN, row.HomeTerm)
	}
}

func TestHandoffWithForceMovesTheRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, db := handoffService(t, srv.URL, 1)
	for _, node := range []store.UV{testNode("uv_one", "one.example.com"), testNode("uv_two", "two.example.com")} {
		if err := db.CreateUV(ctx, node); err != nil {
			t.Fatalf("CreateUV: %v", err)
		}
	}

	var outcome admin.HandoffOutcome
	if err := client.Call(ctx, "home.handoff",
		map[string]any{"uv": "uv_one", "peer": "mn2", "force": true}, &outcome); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if outcome.Peer != "mn2" || outcome.Home != "mn_peer" || outcome.Term != 1 {
		t.Errorf("the outcome reads %+v, want mn2/mn_peer/term 1", outcome)
	}
	if outcome.AgentKey != "the-new-agent-key" {
		t.Errorf("the outcome prints key %q, want the one the peer minted", outcome.AgentKey)
	}
	row, err := db.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if row.HomeMN != "mn_peer" || row.HomeTerm != 1 {
		t.Errorf("the row reads home %q term %d, want mn_peer term 1", row.HomeMN, row.HomeTerm)
	}
	if row.AgentKeyHash != secret.Digest("the-new-agent-key") {
		t.Error("the row does not authenticate with the key the peer minted")
	}
	if row.AgentKeySealed == "" {
		t.Error("the new key was not kept sealed beside the row")
	}
}

func TestHandoffReportsAPeerThatRefuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{refuse: true}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, db := handoffService(t, srv.URL, 10)
	if err := db.CreateUV(ctx, testNode("uv_one", "one.example.com")); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	var outcome admin.HandoffOutcome
	err := client.Call(ctx, "home.handoff", map[string]any{"uv": "uv_one", "peer": "mn2"}, &outcome)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refusing peer = %v, want it named", err)
	}
	if row, err := db.UVByID(ctx, "uv_one"); err != nil || row.HomeMN != "" {
		t.Errorf("the row moved although the peer refused: %+v, %v", row.HomeMN, err)
	}
}

func TestHandoffNamesThePeersItKnows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, db := handoffService(t, srv.URL, 10)
	if err := db.CreateUV(ctx, testNode("uv_one", "one.example.com")); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	var outcome admin.HandoffOutcome
	err := client.Call(ctx, "home.handoff", map[string]any{"uv": "uv_one", "peer": "mn9"}, &outcome)
	if err == nil || !strings.Contains(err.Error(), "mn2") {
		t.Fatalf("an unknown peer = %v, want the configured names listed", err)
	}
}

// Adoption is the answer to a home that never comes back: the replica holds the rows and the sealed agent
// keys, so the nodes keep working. It must never happen while the old home still answers — that is a split
// brain, and a handoff is the procedure there.
func TestAdoptionRefusesAHomeThatAnswers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, db := handoffService(t, srv.URL, 10)
	// The peer's id is what its rows are homed by; it was learned when this server pulled from it.
	if err := db.SetSetting(ctx, mesh.PeerIDKey("mn2"), "mn_gone"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.ApplySnapshot(ctx, store.Snapshot{
		MNID: "mn_gone", Seq: 1, SchemaVersion: store.SchemaVersion,
		Nodes: []store.UV{testNode("uv_one", "one.example.com")},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}

	var outcome admin.AdoptOutcome
	err := client.Call(ctx, "home.adopt", map[string]any{"peer": "mn2"}, &outcome)
	if err == nil || !strings.Contains(err.Error(), "not gone") {
		t.Fatalf("adopting from a live peer = %v, want a refusal pointing at handoff", err)
	}
}

func TestAdoptionTakesTheRowsOfAGonePeer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	url := srv.URL
	srv.Close() // the machine is gone: nothing listens at that address any more

	client, db := handoffService(t, url, 10)
	if err := db.SetSetting(ctx, mesh.PeerIDKey("mn2"), "mn_gone"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.ApplySnapshot(ctx, store.Snapshot{
		MNID: "mn_gone", Seq: 4, SchemaVersion: store.SchemaVersion,
		Nodes: []store.UV{testNode("uv_one", "one.example.com"), testNode("uv_two", "two.example.com")},
	}); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	keys := map[string]string{}
	for _, id := range []string{"uv_one", "uv_two"} {
		row, err := db.UVByID(ctx, id)
		if err != nil {
			t.Fatalf("UVByID: %v", err)
		}
		keys[id] = row.AgentKeyHash
	}

	var outcome admin.AdoptOutcome
	if err := client.Call(ctx, "home.adopt", map[string]any{"peer": "mn2"}, &outcome); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if outcome.Adopted != 2 || outcome.Peer != "mn2" || outcome.Cursor != 4 {
		t.Errorf("the outcome reads %+v, want two nodes from mn2 at cursor 4", outcome)
	}
	own, err := db.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if outcome.Home != own {
		t.Errorf("the outcome names home %q, want this server %q", outcome.Home, own)
	}
	for _, id := range []string{"uv_one", "uv_two"} {
		row, err := db.UVByID(ctx, id)
		if err != nil {
			t.Fatalf("UVByID: %v", err)
		}
		if row.HomeMN != own || row.HomeTerm != 1 {
			t.Errorf("%s reads home %q term %d, want %q term 1", id, row.HomeMN, row.HomeTerm, own)
		}
		if row.AgentKeyHash != keys[id] {
			t.Errorf("%s lost its agent key: the node would have to be visited", id)
		}
	}
}

// A peer this server never pulled from homed nothing here, so there is nothing to adopt — and a name nobody
// configured is a typo, not a rescue.
func TestAdoptionNeedsAPeerItKnows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, _ := handoffService(t, srv.URL, 10)

	var outcome admin.AdoptOutcome
	if err := client.Call(ctx, "home.adopt", map[string]any{"peer": "mn9"}, &outcome); err == nil {
		t.Error("an unconfigured peer was adopted from")
	}
	if err := client.Call(ctx, "home.adopt", map[string]any{"peer": "mn2"}, &outcome); err == nil ||
		!strings.Contains(err.Error(), "never been pulled") {
		t.Errorf("adopting from a never-pulled peer = %v, want it named", err)
	}
}

func testNode(id, domain string) store.UV {
	return store.UV{
		ID: id, Label: "test node", Domain: domain, SelfUUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
		ProfileJSON: `{"network":"xhttp"}`, State: store.StateActive, CreatedAt: testNow,
		AgentKeyHash: secret.Digest("an-old-key"), CredentialEpoch: 1,
	}
}
