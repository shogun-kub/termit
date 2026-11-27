package admin_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/admin"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// Rotation is the one operation that re-keys the whole mesh, so it is gated on the mesh being whole: a peer
// that cannot answer stops it with nothing changed.
func TestRotationNeedsTheWholeMesh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{epoch: 1}
	srv := httptest.NewServer(peer.handler(t))
	url := srv.URL
	srv.Close() // the peer is away
	client, db := handoffService(t, url, 10)
	if err := db.CreateUV(ctx, testNode("uv_one", "one.example.com")); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	var outcome admin.EpochOutcome
	err := client.Call(ctx, "epoch.rotate", nil, &outcome)
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a rotation with a peer away = %v, want a refusal", err)
	}
	if epoch, err := db.Epoch(ctx); err != nil || epoch != 1 {
		t.Errorf("the epoch moved to %d despite the refusal: %v", epoch, err)
	}
}

// A peer ahead of this server means the mesh has already rotated somewhere else; rotating from here would
// move half of it backwards.
func TestRotationRefusesAPeerThatIsAhead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{epoch: 2}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, db := handoffService(t, srv.URL, 10)
	if err := db.CreateUV(ctx, testNode("uv_one", "one.example.com")); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	var outcome admin.EpochOutcome
	err := client.Call(ctx, "epoch.rotate", nil, &outcome)
	if err == nil || !strings.Contains(err.Error(), "at epoch 2") {
		t.Fatalf("a rotation against a peer ahead = %v, want it named", err)
	}
	if epoch, err := db.Epoch(ctx); err != nil || epoch != 1 {
		t.Errorf("the epoch moved to %d despite the refusal: %v", epoch, err)
	}
}

func TestRotationMovesEveryRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{epoch: 1}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, db := handoffService(t, srv.URL, 10)
	for _, node := range []store.UV{testNode("uv_one", "one.example.com"), testNode("uv_two", "two.example.com")} {
		if err := db.CreateUV(ctx, node); err != nil {
			t.Fatalf("CreateUV: %v", err)
		}
	}

	var outcome admin.EpochOutcome
	if err := client.Call(ctx, "epoch.rotate", nil, &outcome); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if outcome.From != 1 || outcome.To != 2 || outcome.Nodes != 2 {
		t.Errorf("the outcome reads %+v, want 1 → 2 over two nodes", outcome)
	}
	if len(outcome.Peers) != 1 || outcome.Peers[0] != "mn2" {
		t.Errorf("the outcome reports peers %v, want the one that answered", outcome.Peers)
	}
	if outcome.Journal < 1 {
		t.Errorf("the rotation entry is %d, want the journal position it landed at", outcome.Journal)
	}
	if epoch, err := db.Epoch(ctx); err != nil || epoch != 2 {
		t.Errorf("the mesh is at epoch %d, %v; want 2", epoch, err)
	}
}

// Nothing enrolled means nothing to re-key: a rotation of an empty registry would bump a number no credential
// uses and make the next enrollment start at 2 for no reason.
func TestRotationOfAnEmptyRegistryIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	peer := &fakePeer{epoch: 1}
	srv := httptest.NewServer(peer.handler(t))
	defer srv.Close()
	client, _ := handoffService(t, srv.URL, 10)

	var outcome admin.EpochOutcome
	err := client.Call(ctx, "epoch.rotate", nil, &outcome)
	if err == nil || !strings.Contains(err.Error(), "nothing to re-key") {
		t.Fatalf("a rotation of an empty registry = %v, want a refusal", err)
	}
}
