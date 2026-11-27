package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/mesh"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

const replicaAgentKey = "agent-key-the-node-uses"

// nodeOn creates a node whose agent key is replicaAgentKey, so a sync to either server authenticates.
func nodeOn(t *testing.T, db *store.Store, id, domain string) {
	t.Helper()
	if err := db.CreateUV(context.Background(), store.UV{
		ID: id, Label: "replicated node", Domain: domain,
		SelfUUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5", ProfileJSON: `{"network":"xhttp"}`,
		State: store.StateActive, CreatedAt: time.Now().UTC(), AgentKeyHash: secret.Digest(replicaAgentKey),
		CredentialEpoch: 1,
	}); err != nil {
		t.Fatalf("CreateUV(%s): %v", id, err)
	}
}

// syncing sends what a node sends: its version, what it serves and how its own check went.
func syncing(t *testing.T, handler http.Handler, prefix, agentKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, prefix+"/agent/sync", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+agentKey)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestAReplicaServesTheLastStateAndQueuesTheReport is decision 3 of §5.2.1 end to end: a node whose home is
// away gets this server's copy read-only, its report waits here, and the puller hands that report to the
// home, which applies it exactly as if the node had synced to it.
func TestAReplicaServesTheLastStateAndQueuesTheReport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := newPeerFixture(t)
	nodeOn(t, home.db, "uv_node", "node.example.com")
	homeMN, err := home.db.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}

	peer := httptest.NewServer(home.handler)
	defer peer.Close()
	peerConfig := config.Peer{Name: "home", URL: peer.URL + home.prefix}

	replicaDB, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/replica.db"})
	if err != nil {
		t.Fatalf("Open the replica: %v", err)
	}
	defer func() { _ = replicaDB.Close() }()
	puller := &mesh.Puller{
		Store: replicaDB, MasterKey: peerKey, Client: peer.Client(), Logger: testLogger(t),
		Mesh: config.Mesh{Peers: []config.Peer{peerConfig}},
	}
	if _, err := puller.Pull(ctx, peerConfig); err != nil {
		t.Fatalf("the replica did not take the snapshot: %v", err)
	}

	body := `{"agent_version":"v0.7.1","transports":[{"kind":"hysteria2"}],"check":{"ok":true}}`
	answer := syncing(t, newHandler(t, replicaDB), home.prefix, replicaAgentKey, body)
	if answer.Code != http.StatusOK {
		t.Fatalf("the replica answered %d: %s", answer.Code, answer.Body)
	}
	var response proto.SyncResponse
	if err := json.NewDecoder(answer.Body).Decode(&response); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if !response.ReadOnly {
		t.Error("the replica answered as if it were the home")
	}
	if response.Home != homeMN {
		t.Errorf("the answer names home %q, want %q", response.Home, homeMN)
	}

	// Nothing on the replica moved: it copies the row and never writes it.
	row, err := replicaDB.UVByID(ctx, "uv_node")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if row.LastSeenAt != nil {
		t.Errorf("the replica recorded a sighting (%s) of a row it does not home", row.LastSeenAt)
	}
	if row.State != store.StateActive {
		t.Errorf("the replica's copy is %s, want the home's active state", row.State)
	}

	pending, err := replicaDB.PendingReports(ctx)
	if err != nil {
		t.Fatalf("PendingReports: %v", err)
	}
	if len(pending) != 1 || pending[0].UV != "uv_node" || pending[0].Home != homeMN {
		t.Fatalf("the queued reports read %+v, want one for uv_node homed by %s", pending, homeMN)
	}

	// The puller hands the report back; the home applies it as its own.
	puller.DeliverReports(ctx)
	if pending, err = replicaDB.PendingReports(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("the queue after delivery = %+v, %v; want empty", pending, err)
	}
	applied, err := home.db.UVByID(ctx, "uv_node")
	if err != nil {
		t.Fatalf("UVByID on the home: %v", err)
	}
	if applied.LastSeenAt == nil {
		t.Fatal("the home did not apply the report")
	}
	if applied.AgentVersion != "v0.7.1" {
		t.Errorf("the home recorded version %q, want v0.7.1", applied.AgentVersion)
	}
	transports, err := home.db.UVTransports(ctx)
	if err != nil {
		t.Fatalf("UVTransports: %v", err)
	}
	// The same rows a direct sync would have written: the report says the node serves QUIC, so that is what
	// the home records beside the TLS profile it renders for everyone.
	if len(transports["uv_node"]) != 1 || transports["uv_node"][0] != proto.KindHysteria2 {
		t.Errorf("the home records transports %v, want [%s]", transports["uv_node"], proto.KindHysteria2)
	}
}

func TestAReportForAnotherServersNodeIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := newPeerFixture(t)
	snap := store.Snapshot{
		MNID: "mn_other", Seq: 1, SchemaVersion: store.SchemaVersion,
		Nodes: []store.UV{{
			ID: "uv_other", Domain: "other.example.com", State: store.StateActive,
			CreatedAt: time.Now().UTC(), CredentialEpoch: 1,
		}},
	}
	if err := home.db.ApplySnapshot(ctx, snap); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}

	report := proto.Report{UV: "uv_other", At: time.Now().UTC(), Sync: proto.SyncRequest{AgentVersion: "v1"}}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	recorder := sendReport(t, home.handler, home.prefix, mesh.PullToken(peerKey), body)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("a report for a row another server homes = %d, want 409", recorder.Code)
	}
	row, err := home.db.UVByID(ctx, "uv_other")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if row.LastSeenAt != nil {
		t.Error("the refusal still touched the row")
	}
}

func TestAnOlderReportDoesNotRewindTheRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := newPeerFixture(t)
	nodeOn(t, fx.db, "uv_node", "node.example.com")
	now := time.Now().UTC()

	fresh := proto.Report{UV: "uv_node", At: now, Sync: proto.SyncRequest{AgentVersion: "v0.7.2"}}
	body, err := json.Marshal(fresh)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if recorder := sendReport(t, fx.handler, fx.prefix, mesh.PullToken(peerKey), body); recorder.Code != http.StatusOK {
		t.Fatalf("the fresh report = %d: %s", recorder.Code, recorder.Body)
	}

	older := proto.Report{UV: "uv_node", At: now.Add(-time.Hour), Sync: proto.SyncRequest{AgentVersion: "v0.7.0"}}
	body, err = json.Marshal(older)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	recorder := sendReport(t, fx.handler, fx.prefix, mesh.PullToken(peerKey), body)
	var answer struct {
		Applied bool `json:"applied"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if recorder.Code != http.StatusOK || answer.Applied {
		t.Fatalf("the stale report = %d %+v, want 200 applied=false", recorder.Code, answer)
	}
	row, err := fx.db.UVByID(ctx, "uv_node")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if row.AgentVersion != "v0.7.2" {
		t.Errorf("the row went back to %q", row.AgentVersion)
	}
	if row.LastSeenAt == nil || row.LastSeenAt.Before(now.Add(-time.Minute)) {
		t.Errorf("last seen = %v, want the fresh report's time", row.LastSeenAt)
	}
}

func sendReport(t *testing.T, handler http.Handler, prefix, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, prefix+"/peer/report", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
