package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// twoStores is the mesh from the store's point of view: a home with its registry, and a replica that has
// never talked to it. Both live in one process, which is what makes the journal testable before the
// transport exists.
func twoStores(t *testing.T) (home, replica *store.Store) {
	t.Helper()
	return openTestStore(t), openTestStore(t)
}

// enrolled writes a node the way enrollment does, so the test starts from a row the journal can carry.
func enrolled(t *testing.T, s *store.Store, id, domain string) store.UV {
	t.Helper()
	node := testUV(id, domain)
	if err := s.CreateUV(context.Background(), node); err != nil {
		t.Fatalf("CreateUV(%s): %v", id, err)
	}
	return node
}

func TestJournalIsOneGrowingLogPerServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, _ := twoStores(t)

	node := enrolled(t, home, "uv_one", "one.example.com")
	first, err := home.AppendNode(ctx, node, []string{"vless"})
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}
	if first.Seq != 1 {
		t.Errorf("first seq = %d, want 1", first.Seq)
	}
	if first.MNID == "" || first.At.IsZero() || first.Kind != store.EventNode {
		t.Errorf("first event = %+v, want an identified node event with a timestamp", first)
	}
	second, err := home.AppendNodeRemoved(ctx, "uv_one")
	if err != nil {
		t.Fatalf("AppendNodeRemoved: %v", err)
	}
	if second.Seq != 2 || second.MNID != first.MNID {
		t.Errorf("second event = %+v, want seq 2 of the same log", second)
	}

	journal, err := home.EventsSince(ctx, first.MNID, 0, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(journal) != 2 || journal[0].Seq != 1 || journal[1].Seq != 2 {
		t.Fatalf("journal = %+v, want two events in order", journal)
	}
	after, err := home.EventsSince(ctx, first.MNID, 1, 0)
	if err != nil {
		t.Fatalf("EventsSince(after 1): %v", err)
	}
	if len(after) != 1 || after[0].Seq != 2 {
		t.Errorf("events after 1 = %+v, want only the removal", after)
	}

	// The kind set is closed: an event this build does not know is refused at the door, not stored for
	// someone else to choke on.
	if _, err := home.AppendEvent(ctx, "policy", map[string]string{"x": "y"}); !errors.Is(err, store.ErrUnknownEvent) {
		t.Errorf("AppendEvent(policy) = %v, want ErrUnknownEvent", err)
	}
}

func TestReplicaAppliesTheJournalInOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	node := enrolled(t, home, "uv_one", "one.example.com")
	if err := home.SetUVTransports(ctx, node.ID, []string{"vless", "hysteria2"}); err != nil {
		t.Fatalf("SetUVTransports: %v", err)
	}
	event, err := home.AppendNode(ctx, node, []string{"vless", "hysteria2"})
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}

	applied, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{event})
	if err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	got, err := replica.UVByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("UVByID on the replica: %v", err)
	}
	if got.Domain != node.Domain || got.HomeMN != event.MNID {
		t.Errorf("replicated node = %+v, want %s homed by %s", got, node.Domain, event.MNID)
	}
	served, err := replica.UVTransports(ctx)
	if err != nil {
		t.Fatalf("UVTransports: %v", err)
	}
	if len(served[node.ID]) != 2 {
		t.Errorf("replicated transports = %v, want both kinds", served[node.ID])
	}
	if cursor, err := replica.Cursor(ctx, event.MNID); err != nil || cursor != event.Seq {
		t.Errorf("cursor = %d, %v; want %d", cursor, err, event.Seq)
	}

	// A retry of the same batch changes nothing and is not an error: the transport will retry.
	applied, err = replica.ApplyEvents(ctx, []store.ReplicationEvent{event})
	if err != nil {
		t.Fatalf("re-applying the same event: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d on a replay, want 0", applied)
	}

	// Mixing the applied event with a new one applies only the new one.
	removed, err := home.AppendNodeRemoved(ctx, node.ID)
	if err != nil {
		t.Fatalf("AppendNodeRemoved: %v", err)
	}
	applied, err = replica.ApplyEvents(ctx, []store.ReplicationEvent{event, removed})
	if err != nil {
		t.Fatalf("ApplyEvents(event, removal): %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	if _, err := replica.UVByID(ctx, node.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the removed node is still on the replica: %v", err)
	}
}

func TestReplicaRefusesAGapAndBadPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	first := enrolled(t, home, "uv_one", "one.example.com")
	event1, err := home.AppendNode(ctx, first, nil)
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}
	second := enrolled(t, home, "uv_two", "two.example.com")
	event2, err := home.AppendNode(ctx, second, nil)
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}

	// The event after the missing one cannot be applied on its own: applying it would leave the replica
	// with a node it cannot explain and a cursor that hides the hole.
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{event2}); !errors.Is(err, store.ErrReplicationGap) {
		t.Fatalf("ApplyEvents(gap) = %v, want ErrReplicationGap", err)
	}
	if cursor, _ := replica.Cursor(ctx, event1.MNID); cursor != 0 {
		t.Errorf("cursor = %d after a refused batch, want 0", cursor)
	}

	// A batch that starts well and then fails leaves nothing behind: no half-applied node, no cursor.
	broken := store.ReplicationEvent{MNID: event2.MNID, Seq: event2.Seq, Kind: event2.Kind,
		Payload: json.RawMessage(`{"node":{"id":"uv_two"}}`)}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{event1, broken}); err == nil {
		t.Fatal("a node without a domain was applied")
	}
	if _, err := replica.UVByID(ctx, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Error("the first event of a refused batch was committed")
	}
	if cursor, _ := replica.Cursor(ctx, event1.MNID); cursor != 0 {
		t.Errorf("cursor = %d after a failed batch, want 0", cursor)
	}

	// A kind this build does not know is refused rather than skipped: an older build must not silently
	// ignore a newer peer's change.
	unknown := store.ReplicationEvent{MNID: event1.MNID, Seq: 1, Kind: "policy", Payload: json.RawMessage(`{}`)}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{unknown}); !errors.Is(err, store.ErrUnknownEvent) {
		t.Errorf("ApplyEvents(unknown kind) = %v, want ErrUnknownEvent", err)
	}
}

func TestReplicaRefusesToTouchANodeItHomes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	// The same node id exists on both sides: the home has a row for it, and so does the replica. Two
	// homes for one id is the split the mesh refuses to merge.
	local := enrolled(t, replica, "uv_shared", "shared.example.com")
	if local.HomeMN != "" {
		t.Fatalf("a locally created node has home %q, want empty", local.HomeMN)
	}
	claim := testUV("uv_shared", "shared.example.com")
	event, err := home.AppendNode(ctx, claim, nil)
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{event}); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("ApplyEvents over a homed row = %v, want ErrReplicationConflict", err)
	}
	if cursor, _ := replica.Cursor(ctx, event.MNID); cursor != 0 {
		t.Errorf("cursor = %d after a conflict, want 0", cursor)
	}

	// A removal of this server's own node is refused for the same reason. A second home is used because
	// the first one's log is stuck at the event the replica refused: that is the gap rule, not this one.
	other, _ := twoStores(t)
	local2 := enrolled(t, replica, "uv_shared2", "shared2.example.com")
	removal, err := other.AppendNodeRemoved(ctx, local2.ID)
	if err != nil {
		t.Fatalf("AppendNodeRemoved: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{removal}); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("ApplyEvents removing a homed row = %v, want ErrReplicationConflict", err)
	}
	if _, err := replica.UVByID(ctx, local2.ID); err != nil {
		t.Errorf("a refused removal took the node away: %v", err)
	}
}

func TestSnapshotCarriesTheOriginNodesAndNothingElse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	one := enrolled(t, home, "uv_one", "one.example.com")
	two := enrolled(t, home, "uv_two", "two.example.com")
	if err := home.SetUVTransports(ctx, one.ID, []string{"vless"}); err != nil {
		t.Fatalf("SetUVTransports: %v", err)
	}
	enrolled(t, replica, "uv_local", "local.example.com")

	snap, err := home.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.SchemaVersion != store.SchemaVersion || len(snap.Nodes) != 2 {
		t.Fatalf("snapshot = %+v, want this schema with both nodes", snap)
	}
	if err := replica.ApplySnapshot(ctx, snap); err != nil {
		t.Fatalf("ApplySnapshot: %v", err)
	}
	// The rows are the home's, and the replica's own row is untouched.
	for _, node := range []store.UV{one, two} {
		got, err := replica.UVByID(ctx, node.ID)
		if err != nil {
			t.Fatalf("UVByID(%s) on the replica: %v", node.ID, err)
		}
		if got.HomeMN != snap.MNID {
			t.Errorf("%s has home %q, want %q", node.ID, got.HomeMN, snap.MNID)
		}
	}
	if _, err := replica.UVByID(ctx, "uv_local"); err != nil {
		t.Errorf("the replica's own node is gone: %v", err)
	}
	served, err := replica.UVTransports(ctx)
	if err != nil {
		t.Fatalf("UVTransports: %v", err)
	}
	if len(served[one.ID]) != 1 {
		t.Errorf("transports of %s = %v, want the snapshot's one kind", one.ID, served[one.ID])
	}
	if cursor, err := replica.Cursor(ctx, snap.MNID); err != nil || cursor != snap.Seq {
		t.Errorf("cursor = %d, %v; want the snapshot's %d", cursor, err, snap.Seq)
	}

	// Applying the same snapshot again is free, and a snapshot taken before one already applied is
	// refused: replaying it would roll the registry back.
	if err := replica.ApplySnapshot(ctx, snap); err != nil {
		t.Fatalf("re-applying the same snapshot: %v", err)
	}
	if _, err := home.SetUVState(ctx, two.ID, store.StateRemoved, "test", "test", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	if err := home.ForgetUV(ctx, two.ID); err != nil {
		t.Fatalf("ForgetUV: %v", err)
	}
	// A change on the home moves the journal, and the journal position is what makes a snapshot's age
	// meaningful: without it two snapshots of the same content are indistinguishable from any order.
	if _, err := home.AppendNodeRemoved(ctx, two.ID); err != nil {
		t.Fatalf("AppendNodeRemoved: %v", err)
	}
	newer, err := home.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if newer.Seq <= snap.Seq {
		t.Fatalf("the newer snapshot is at %d, want past %d", newer.Seq, snap.Seq)
	}
	if err := replica.ApplySnapshot(ctx, newer); err != nil {
		t.Fatalf("ApplySnapshot(newer): %v", err)
	}
	if _, err := replica.UVByID(ctx, two.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a node the home no longer has is still on the replica: %v", err)
	}
	if err := replica.ApplySnapshot(ctx, snap); !errors.Is(err, store.ErrSnapshotStale) {
		t.Errorf("ApplySnapshot(older) = %v, want ErrSnapshotStale", err)
	}
}

func TestSnapshotRefusesAnotherSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	snap, err := home.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap.SchemaVersion = store.SchemaVersion + 1
	if err := replica.ApplySnapshot(ctx, snap); err == nil {
		t.Error("a snapshot from a newer schema was applied")
	}
	if err := replica.ApplySnapshot(ctx, store.Snapshot{}); err == nil {
		t.Error("a snapshot without an origin was applied")
	}
}
