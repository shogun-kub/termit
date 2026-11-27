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

// Adoption is the rescue a dead home needs: no cooperation, the agent key survives (it was sealed with the
// key both servers hold), the term moves, and the queued report for the gone home is dropped because the
// node's next sync writes the same facts to the server that homes it now.
func TestAdoptionKeepsTheAgentKeyAndRaisesTheTerm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gone, survivor := twoStores(t)
	enrolled(t, gone, "uv_one", "one.example.com")
	goneID, err := gone.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	survivorID, err := survivor.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if _, err := survivor.ApplyEvents(ctx, journal(t, gone, goneID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	before, err := survivor.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	// A read-only sync left a report waiting for the home that is now gone.
	if err := survivor.QueueReport(ctx, "uv_one", testNow, []byte(`{}`)); err != nil {
		t.Fatalf("QueueReport: %v", err)
	}

	taken, err := survivor.AdoptUV(ctx, "uv_one", goneID, testNow)
	if err != nil {
		t.Fatalf("AdoptUV: %v", err)
	}
	if taken.HomeMN != survivorID || taken.HomeTerm != 1 {
		t.Errorf("the adopted row reads home %q term %d, want %q term 1", taken.HomeMN, taken.HomeTerm, survivorID)
	}
	if taken.AgentKeyHash != before.AgentKeyHash || taken.AgentKeySealed != before.AgentKeySealed {
		t.Error("the adoption replaced the agent key: the node would stop authenticating")
	}
	if pending, err := survivor.PendingReports(ctx); err != nil || len(pending) != 0 {
		t.Errorf("the queue reads %+v, %v; want it emptied by the adoption", pending, err)
	}
	// The adoption is announced from here on, so the old home learns it by pulling as usual.
	if _, err := gone.ApplyEvents(ctx, journal(t, survivor, survivorID)); err != nil {
		t.Fatalf("the returning home could not apply the adoption: %v", err)
	}
	if back, err := gone.UVByID(ctx, "uv_one"); err != nil || back.HomeMN != survivorID {
		t.Errorf("the old home still homes the node: %+v, %v", back.HomeMN, err)
	}
	// A second adoption of the same row is refused: it is this server's now.
	if _, err := survivor.AdoptUV(ctx, "uv_one", goneID, testNow); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("adopting an already-adopted row = %v, want ErrReplicationConflict", err)
	}
}

// Adoption takes only what the named server homed: a row of another home, or of this server, is not for the
// taking, and a removed node is not worth taking.
func TestAdoptionRefusesRowsItDoesNotOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gone, survivor := twoStores(t)
	goneID, _ := gone.MeshID(ctx)
	enrolled(t, gone, "uv_one", "one.example.com")
	enrolled(t, survivor, "uv_local", "local.example.com")
	if _, err := survivor.ApplyEvents(ctx, journal(t, gone, goneID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}

	if _, err := survivor.AdoptUV(ctx, "uv_local", goneID, testNow); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("adopting this server's own row = %v, want ErrReplicationConflict", err)
	}
	if _, err := survivor.AdoptUV(ctx, "uv_missing", goneID, testNow); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("adopting an unknown row = %v, want ErrNotFound", err)
	}
	if _, err := survivor.AdoptUV(ctx, "uv_one", goneID, testNow); err != nil {
		t.Fatalf("AdoptUV: %v", err)
	}
	if _, err := survivor.AdoptUV(ctx, "uv_one", goneID, testNow); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("adopting twice = %v, want ErrReplicationConflict", err)
	}
}

// The list `home adopt` takes over is the rows one server homes, in a stable order and without removed nodes.
func TestUVsHomedByListsWhatAdoptionWouldTake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gone, survivor := twoStores(t)
	goneID, _ := gone.MeshID(ctx)
	enrolled(t, gone, "uv_b", "b.example.com")
	enrolled(t, gone, "uv_a", "a.example.com")
	enrolled(t, survivor, "uv_local", "local.example.com")
	if _, err := survivor.ApplyEvents(ctx, journal(t, gone, goneID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}

	nodes, err := survivor.UVsHomedBy(ctx, goneID)
	if err != nil {
		t.Fatalf("UVsHomedBy: %v", err)
	}
	if len(nodes) != 2 || nodes[0].Domain != "a.example.com" || nodes[1].Domain != "b.example.com" {
		t.Fatalf("the list reads %v, want a and b in that order", nodes)
	}
}

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
