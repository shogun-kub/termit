package store_test

import (
	"context"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// A rotation is one decision and one entry: every row moves together, and a peer that applies that entry ends
// up at the same number without re-keying anything itself.
func TestRotationReKeysEveryRowInOneEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	for _, id := range []string{"uv_a", "uv_b", "uv_c"} {
		enrolled(t, home, id, id+".example.com")
	}
	homeID, _ := home.MeshID(ctx)
	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}

	changed, err := home.RotateEpoch(ctx, 2, testNow)
	if err != nil {
		t.Fatalf("RotateEpoch: %v", err)
	}
	if changed != 3 {
		t.Errorf("the rotation re-keyed %d rows, want 3", changed)
	}
	if epoch, err := home.Epoch(ctx); err != nil || epoch != 2 {
		t.Fatalf("the home is at epoch %d, %v; want 2", epoch, err)
	}
	events := journal(t, home, homeID)
	last := events[len(events)-1]
	if last.Kind != store.EventEpoch {
		t.Fatalf("the last entry is %q, want %q", last.Kind, store.EventEpoch)
	}

	// The peer applies the page it has not seen and moves with it, without an entry of its own.
	if _, err := replica.ApplyEvents(ctx, events[3:]); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if epoch, err := replica.Epoch(ctx); err != nil || epoch != 2 {
		t.Errorf("the replica is at epoch %d, %v; want 2", epoch, err)
	}
	if own := journal(t, replica, mustID(t, replica)); len(own) != 0 {
		t.Errorf("the replica journaled %d entries of its own for somebody else's rotation", len(own))
	}

	// A rotation is an increment: the same number again, or a smaller one, is refused rather than applied.
	if _, err := home.RotateEpoch(ctx, 2, testNow); err == nil {
		t.Error("RotateEpoch accepted the epoch the mesh is already at")
	}
	if _, err := home.RotateEpoch(ctx, 1, testNow); err == nil {
		t.Error("RotateEpoch accepted a smaller epoch")
	}
}

// A replay of the rotation entry changes nothing: peers retry, and the number only ever grows.
func TestAReplayedRotationChangesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	enrolled(t, home, "uv_a", "a.example.com")
	homeID, _ := home.MeshID(ctx)
	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if _, err := home.RotateEpoch(ctx, 2, testNow); err != nil {
		t.Fatalf("RotateEpoch: %v", err)
	}
	events := journal(t, home, homeID)
	if _, err := replica.ApplyEvents(ctx, events); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, events); err != nil {
		t.Fatalf("ApplyEvents(replay): %v", err)
	}
	if epoch, err := replica.Epoch(ctx); err != nil || epoch != 2 {
		t.Errorf("the replica is at epoch %d, %v; want 2", epoch, err)
	}
}

func mustID(t *testing.T, s *store.Store) string {
	t.Helper()
	mnID, err := s.MeshID(context.Background())
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	return mnID
}
