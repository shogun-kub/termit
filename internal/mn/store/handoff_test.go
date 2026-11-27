package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// A handoff is two writes on two servers: the target takes the row with a new term and a new agent key, and
// the old home gives it up without journaling anything — its log is no longer where the row is announced.
func TestAHandoffMovesTheRowToTheNewHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, other := twoStores(t)

	enrolled(t, home, "uv_one", "one.example.com")
	homeID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	otherID, err := other.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	// The target must already hold the row: it takes over what it can see, not what it is told about.
	if _, err := other.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}

	taken, err := other.AcceptHandoff(ctx, "uv_one", homeID, "hash-new", "sealed-new", testNow)
	if err != nil {
		t.Fatalf("AcceptHandoff: %v", err)
	}
	if taken.HomeMN != otherID || taken.HomeTerm != 1 {
		t.Errorf("the target records home %q term %d, want %q term 1", taken.HomeMN, taken.HomeTerm, otherID)
	}
	if taken.AgentKeyHash != "hash-new" || taken.AgentKeySealed != "sealed-new" {
		t.Errorf("the target kept the old agent key: %q/%q", taken.AgentKeyHash, taken.AgentKeySealed)
	}
	// Its journal is where the handoff is announced, so the old home learns it by pulling as usual.
	if _, err := home.ApplyEvents(ctx, journal(t, other, otherID)); err != nil {
		t.Fatalf("the home could not apply the handoff: %v", err)
	}
	given, err := home.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if given.HomeMN != otherID || given.HomeTerm != 1 {
		t.Errorf("the old home still records home %q term %d", given.HomeMN, given.HomeTerm)
	}
}

// HandoffOut is what the old home writes: the row is the new home's, at the term that home answered with. A
// term that does not move forward, or a row this server does not home, is refused.
func TestHandoffOutNeedsAFreshTermAndItsOwnRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	enrolled(t, home, "uv_one", "one.example.com")
	homeID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}

	if err := home.HandoffOut(ctx, "uv_one", "mn_new", 0, "h", "s"); err == nil {
		t.Error("HandoffOut accepted a term that does not move the row")
	}
	if err := home.HandoffOut(ctx, "uv_missing", "mn_new", 1, "h", "s"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("HandoffOut on a missing node = %v, want ErrNotFound", err)
	}
	if err := home.HandoffOut(ctx, "uv_one", "mn_new", 1, "hash-new", "sealed-new"); err != nil {
		t.Fatalf("HandoffOut: %v", err)
	}
	given, err := home.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if given.HomeMN != "mn_new" || given.HomeTerm != 1 {
		t.Errorf("the row reads home %q term %d, want mn_new term 1", given.HomeMN, given.HomeTerm)
	}
	// A row the replica copied is not the replica's to hand out.
	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if err := replica.HandoffOut(ctx, "uv_one", "mn_new", 9, "h", "s"); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("HandoffOut of a foreign row = %v, want ErrReplicationConflict", err)
	}
}

// The target takes only what it can see, and only from the server that says it is handing it over.
func TestAcceptHandoffRefusesWhatItCannotOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, other := twoStores(t)
	enrolled(t, home, "uv_one", "one.example.com")
	homeID, _ := home.MeshID(ctx)

	if _, err := other.AcceptHandoff(ctx, "uv_one", homeID, "h", "s", testNow); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a node the target has never seen = %v, want ErrNotFound", err)
	}
	if _, err := other.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	// The claim has to match the home the row already carries, or any server could take any row it sees.
	if _, err := other.AcceptHandoff(ctx, "uv_one", "mn_somebody_else", "h", "s", testNow); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("a handoff from the wrong server = %v, want ErrReplicationConflict", err)
	}
}
