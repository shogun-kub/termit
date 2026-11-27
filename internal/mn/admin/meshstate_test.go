package admin

import (
	"context"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/mesh"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// A peer that was never reached and a peer that is up to date are different answers, and the dashboard has
// to tell them apart: "no news" printed as "fine" is how a split mesh goes unnoticed.
func TestMeshStateReportsEachPeer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service := &Service{Store: db, Master: []byte("0123456789abcdef0123456789abcdef")}
	service.Mesh = config.Mesh{Peers: []config.Peer{
		{Name: "mn2", URL: "https://mn2.example.com/p9x"},
		{Name: "mn3", URL: "https://mn3.example.com/p9x"},
	}}

	answer, apiErr := service.meshState(ctx)
	if apiErr != nil {
		t.Fatalf("meshState: %v", apiErr)
	}
	state, ok := answer.(MeshState)
	if !ok {
		t.Fatalf("meshState answered %T", answer)
	}
	if !state.HasMasterKey {
		t.Error("the service holds a key but says it does not")
	}
	if len(state.Peers) != 2 {
		t.Fatalf("the state names %d peers, want 2", len(state.Peers))
	}
	for _, peer := range state.Peers {
		if peer.Known {
			t.Errorf("peer %s is reported as known before it was ever reached", peer.Name)
		}
	}

	// What the puller leaves behind after one snapshot and one page: the peer's id, where this server got
	// to, and where the peer's journal ends.
	const peerMN = "mn_peer"
	snap := store.Snapshot{
		MNID: peerMN, Seq: 4, SchemaVersion: store.SchemaVersion,
		Nodes: []store.UV{{
			ID: "uv_peer", Domain: "peer.example.com", State: store.StateActive,
			CreatedAt: time.Now().UTC(), CredentialEpoch: 1,
		}},
	}
	if err := db.ApplySnapshot(ctx, snap); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	if err := db.SetSetting(ctx, mesh.PeerIDKey("mn2"), peerMN); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.SetSetting(ctx, mesh.PeerSeqKey("mn2"), "7"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	answer, apiErr = service.meshState(ctx)
	if apiErr != nil {
		t.Fatalf("meshState: %v", apiErr)
	}
	state = answer.(MeshState)
	known := state.Peers[0]
	if !known.Known || known.MNID != peerMN {
		t.Errorf("the reached peer reads %+v, want it known as %s", known, peerMN)
	}
	if known.Cursor != 4 || known.LastSeq != 7 || known.Behind != 3 {
		t.Errorf("cursor/last/behind = %d/%d/%d, want 4/7/3", known.Cursor, known.LastSeq, known.Behind)
	}
	if state.Peers[1].Known {
		t.Error("the second peer became known without being pulled")
	}
}

func TestMeshStateWithoutPeers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	answer, apiErr := (&Service{Store: db}).meshState(ctx)
	if apiErr != nil {
		t.Fatalf("meshState: %v", apiErr)
	}
	state := answer.(MeshState)
	if state.MNID == "" {
		t.Error("a single-server mesh has no identity to report")
	}
	if len(state.Peers) != 0 {
		t.Errorf("a single-server mesh reports %d peers", len(state.Peers))
	}
	if state.HasMasterKey {
		t.Error("a service without a key says it holds one")
	}
}
