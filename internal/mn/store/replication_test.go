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

// journal reads one server's log from the start.
func journal(t *testing.T, s *store.Store, mnID string) []store.ReplicationEvent {
	t.Helper()
	events, err := s.EventsSince(context.Background(), mnID, 0, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	return events
}

// Every write that changes a replicated field leaves exactly one entry, in one growing log: the row and
// its entry commit together, so a replica can rebuild from the log alone.
func TestEveryReplicatedWriteLeavesOneEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, _ := twoStores(t)

	node := enrolled(t, home, "uv_one", "one.example.com")
	if err := home.SetUVTransports(ctx, node.ID, []string{"vless"}, nil); err != nil {
		t.Fatalf("SetUVTransports: %v", err)
	}
	if _, err := home.SetUVState(ctx, node.ID, store.StateActive, "checks passed", "prober", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	if err := home.RelocateUV(ctx, node.ID, "moved.example.com", node.SelfUUID, node.ProfileJSON, ""); err != nil {
		t.Fatalf("RelocateUV: %v", err)
	}
	if err := home.SetAgentKeyHash(ctx, node.ID, "sha256$new", "sealed"); err != nil {
		t.Fatalf("SetAgentKeyHash: %v", err)
	}

	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if events, err := home.EventsSince(ctx, "", 0, 0); err == nil || len(events) != 0 {
		t.Fatalf("EventsSince with no server = %v, %d events", err, len(events))
	}
	events := journal(t, home, mnID)

	if len(events) != 5 {
		t.Fatalf("journal length = %d, want 5 (enroll, transports, state, relocate, key)", len(events))
	}
	for i, event := range events {
		if event.Seq != int64(i+1) || event.MNID != mnID || event.Kind != store.EventNode || event.At.IsZero() {
			t.Fatalf("event %d = %+v, want seq %d of %s", i, event, i+1, mnID)
		}
	}
	if last := events[len(events)-1]; last.Kind != store.EventNode {
		t.Errorf("last event kind = %q", last.Kind)
	}

	// A touch is a request, not a decision: it must not grow the log, or every sync would.
	if err := home.TouchUV(ctx, node.ID, "203.0.113.9", "termit-uv v0.7.0", testNow); err != nil {
		t.Fatalf("TouchUV: %v", err)
	}
	if after := journal(t, home, mnID); len(after) != 5 {
		t.Errorf("a touch grew the journal to %d entries", len(after))
	}

	// A forgotten node leaves a removal, and nothing of it stays in the transports.
	if _, err := home.SetUVState(ctx, node.ID, store.StateRemoved, "forgotten", "admin", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	if err := home.ForgetUV(ctx, node.ID); err != nil {
		t.Fatalf("ForgetUV: %v", err)
	}
	events = journal(t, home, mnID)
	removal := events[len(events)-1]
	if removal.Kind != store.EventNodeRemoved {
		t.Errorf("last event = %+v, want a removal", removal)
	}
	served, err := home.UVTransports(ctx)
	if err != nil {
		t.Fatalf("UVTransports: %v", err)
	}
	if len(served[node.ID]) != 0 {
		t.Errorf("a forgotten node still has transports: %v", served[node.ID])
	}

	if _, err := home.AppendEvent(ctx, "policy", map[string]string{"x": "y"}); !errors.Is(err, store.ErrUnknownEvent) {
		t.Errorf("AppendEvent(policy) = %v, want ErrUnknownEvent", err)
	}
}

// The journal a server writes must rebuild its registry on another one, field for field: that is the
// whole contract, and the reason every write path above was wired.
func TestTheJournalRebuildsTheRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	node := enrolled(t, home, "uv_one", "one.example.com")
	if err := home.SetUVTransports(ctx, node.ID, []string{"vless", "hysteria2"}, nil); err != nil {
		t.Fatalf("SetUVTransports: %v", err)
	}
	if _, err := home.SetUVState(ctx, node.ID, store.StateActive, "checks passed", "prober", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	if err := home.RelocateUV(ctx, node.ID, "moved.example.com", "not-the-old-uuid", `{"network":"xhttp"}`, "pin"); err != nil {
		t.Fatalf("RelocateUV: %v", err)
	}

	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	events := journal(t, home, mnID)
	applied, err := replica.ApplyEvents(ctx, events)
	if err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if applied != len(events) {
		t.Errorf("applied = %d, want %d", applied, len(events))
	}

	want, err := home.UVByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("UVByID on the home: %v", err)
	}
	got, err := replica.UVByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("UVByID on the replica: %v", err)
	}
	got.HomeMN = want.HomeMN // the origin's own row is empty; the replica records who sent it
	if got != want {
		t.Errorf("replica row = %+v\nhome row    = %+v", got, want)
	}
	if got.Domain != "moved.example.com" || got.State != store.StateActive {
		t.Errorf("replica row = %+v, want the moved, active node", got)
	}
	served, err := replica.UVTransports(ctx)
	if err != nil {
		t.Fatalf("UVTransports: %v", err)
	}
	if len(served[node.ID]) != 2 {
		t.Errorf("replicated transports = %v, want both kinds", served[node.ID])
	}
	if cursor, err := replica.Cursor(ctx, mnID); err != nil || cursor != int64(len(events)) {
		t.Errorf("cursor = %d, %v; want %d", cursor, err, len(events))
	}

	// A retry of the same batch changes nothing and is not an error: the transport will retry.
	if applied, err := replica.ApplyEvents(ctx, events); err != nil || applied != 0 {
		t.Errorf("replay = %d, %v; want 0, nil", applied, err)
	}

	// A node that leaves the mesh leaves the replica too.
	if _, err := home.SetUVState(ctx, node.ID, store.StateRemoved, "forgotten", "admin", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	if err := home.ForgetUV(ctx, node.ID); err != nil {
		t.Fatalf("ForgetUV: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, journal(t, home, mnID)[len(events):]); err != nil {
		t.Fatalf("ApplyEvents(after the removal): %v", err)
	}
	if _, err := replica.UVByID(ctx, node.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the removed node is still on the replica: %v", err)
	}
}

func TestReplicaRefusesAGapAndBadPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	enrolled(t, home, "uv_one", "one.example.com")
	enrolled(t, home, "uv_two", "two.example.com")
	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	events := journal(t, home, mnID)
	if len(events) != 2 {
		t.Fatalf("journal = %d events, want 2", len(events))
	}

	// The second event cannot be applied on its own: it would leave the replica with a node it cannot
	// explain and a cursor that hides the hole.
	if _, err := replica.ApplyEvents(ctx, events[1:]); !errors.Is(err, store.ErrReplicationGap) {
		t.Fatalf("ApplyEvents(gap) = %v, want ErrReplicationGap", err)
	}
	if cursor, _ := replica.Cursor(ctx, mnID); cursor != 0 {
		t.Errorf("cursor = %d after a refused batch, want 0", cursor)
	}

	// A batch that starts well and then fails leaves nothing behind: no half-applied node, no cursor.
	broken := store.ReplicationEvent{MNID: events[1].MNID, Seq: events[1].Seq, Kind: events[1].Kind,
		At: events[1].At, Payload: json.RawMessage(`{"node":{"id":"uv_two"}}`)}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{events[0], broken}); err == nil {
		t.Fatal("a node without a domain was applied")
	}
	if _, err := replica.UVByID(ctx, "uv_one"); !errors.Is(err, store.ErrNotFound) {
		t.Error("the first event of a refused batch was committed")
	}
	if cursor, _ := replica.Cursor(ctx, mnID); cursor != 0 {
		t.Errorf("cursor = %d after a failed batch, want 0", cursor)
	}

	// A kind this build does not know is refused rather than skipped: an older build must not silently
	// ignore a newer peer's change.
	unknown := store.ReplicationEvent{MNID: mnID, Seq: 1, Kind: "policy", Payload: json.RawMessage(`{}`)}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{unknown}); !errors.Is(err, store.ErrUnknownEvent) {
		t.Errorf("ApplyEvents(unknown kind) = %v, want ErrUnknownEvent", err)
	}
}

// A replica's own log is what a third server would pull, so it must not announce a change to a row it
// does not home — and now that the write is refused outright, the log cannot even be tempted.
func TestReplicaDoesNotJournalARowItDoesNotHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	enrolled(t, home, "uv_one", "one.example.com")
	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, journal(t, home, mnID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	_, err = replica.SetUVState(ctx, "uv_one", store.StateBlocked, "someone edited it", "admin", testNow)
	if !errors.Is(err, store.ErrReplicationConflict) {
		t.Fatalf("SetUVState on the replica = %v, want ErrReplicationConflict", err)
	}
	got, err := replica.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if got.State != store.StatePending {
		t.Errorf("the replica's copy is %s, want the home's pending state", got.State)
	}
	ownID, err := replica.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if events := journal(t, replica, ownID); len(events) != 0 {
		t.Errorf("the replica journaled %d events for a row it does not home: %+v", len(events), events)
	}
}

func TestReplicaRefusesToTouchANodeItHomes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	// The same node id exists on both sides: two homes for one id is the split the mesh refuses to
	// merge.
	local := enrolled(t, replica, "uv_shared", "shared.example.com")
	if local.HomeMN != "" {
		t.Fatalf("a locally created node has home %q, want empty", local.HomeMN)
	}
	enrolled(t, home, "uv_shared", "shared.example.com")
	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, journal(t, home, mnID)[:1]); !errors.Is(err, store.ErrReplicationConflict) {
		t.Errorf("ApplyEvents over a homed row = %v, want ErrReplicationConflict", err)
	}
	if cursor, _ := replica.Cursor(ctx, mnID); cursor != 0 {
		t.Errorf("cursor = %d after a conflict, want 0", cursor)
	}

	// A removal of this server's own node is skipped, not refused: a removal from a server that no longer
	// homes the row must never take it away here, and refusing would stall the cursor behind it.
	other, _ := twoStores(t)
	local2 := enrolled(t, replica, "uv_shared2", "shared2.example.com")
	removal, err := other.AppendNodeRemoved(ctx, local2.ID)
	if err != nil {
		t.Fatalf("AppendNodeRemoved: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{removal}); err != nil {
		t.Errorf("ApplyEvents removing a homed row = %v, want it skipped", err)
	}
	if _, err := replica.UVByID(ctx, local2.ID); err != nil {
		t.Errorf("a stale removal took the node away: %v", err)
	}
}

// A handoff is what home_term exists for: the server that takes the node announces it as its own with a
// higher term, and the replica flips to it. An event that still claims the old home at the old term — what a
// server returning from a partition has in its journal — loses without stalling the cursor.
func TestANewerHomeTermWinsTheMerge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	enrolled(t, home, "uv_one", "one.example.com")
	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, journal(t, home, mnID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}

	other, _ := twoStores(t)
	otherID, err := other.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	handed := testUV("uv_one", "one.example.com")
	handed.HomeMN, handed.HomeTerm = otherID, 1
	event, err := other.AppendNode(ctx, handed, nil)
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{event}); err != nil {
		t.Fatalf("apply the handoff: %v", err)
	}
	got, err := replica.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if got.HomeMN != otherID || got.HomeTerm != 1 {
		t.Fatalf("the replica records home %q term %d, want %q term 1", got.HomeMN, got.HomeTerm, otherID)
	}

	// The old home's next event still says term 0: it is skipped, and the change is not counted.
	stale, err := home.AppendNode(ctx, testUV("uv_one", "one.example.com"), nil)
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}
	applied, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{stale})
	if err != nil {
		t.Fatalf("apply the stale event: %v", err)
	}
	if applied != 0 {
		t.Errorf("the stale event applied %d changes, want 0", applied)
	}
	if got, err = replica.UVByID(ctx, "uv_one"); err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if got.HomeMN != otherID || got.HomeTerm != 1 {
		t.Errorf("the stale event undid the handoff: home %q term %d", got.HomeMN, got.HomeTerm)
	}
	if cursor, err := replica.Cursor(ctx, mnID); err != nil || cursor != stale.Seq {
		t.Errorf("the cursor is %d, %v; want %d — a skipped event must still move it", cursor, err, stale.Seq)
	}
}

func TestSnapshotCarriesTheOriginNodesAndNothingElse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)

	one := enrolled(t, home, "uv_one", "one.example.com")
	two := enrolled(t, home, "uv_two", "two.example.com")
	if err := home.SetUVTransports(ctx, one.ID, []string{"vless"}, nil); err != nil {
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

// A node is homed where it was enrolled. An empty home reads as "mine" to every ownership check — the
// scheduler, the write guard, the replica's sync — so a row created without one would be written by
// whichever server pulled it, and the mesh would have two authorities for one node.
func TestANewNodeIsHomedWhereItWasEnrolled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	homeID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}

	enrolled(t, home, "uv_one", "one.example.com")
	row, err := home.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if row.HomeMN != homeID {
		t.Errorf("a row enrolled here is homed by %q, want %q", row.HomeMN, homeID)
	}

	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	copied, err := replica.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID on the replica: %v", err)
	}
	// The replica's copy names the home, which is what stops it from writing a row it does not own.
	if copied.HomeMN != homeID {
		t.Errorf("the replica's copy is homed by %q, want %q", copied.HomeMN, homeID)
	}
}

// A peer can home a node whose domain this server already serves under another id — a node re-enrolled on the
// home while a replica still held the old one. That is a disagreement for an operator, but it must not freeze
// the journal: every later change from that peer would wait behind it. The local row stays and the cursor
// moves on.
func TestAForeignRowForATakenDomainIsSkipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	homeID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	enrolled(t, home, "uv_one", "one.example.com")

	// The same domain under another id, which this replica homed itself: the one authority it must not lose.
	enrolled(t, replica, "uv_mine", "one.example.com")
	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	mine, err := replica.UVByID(ctx, "uv_mine")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if mine.HomeMN == homeID {
		t.Errorf("the peer's row took the domain from this server's own row")
	}
	if cursor, err := replica.Cursor(ctx, homeID); err != nil || cursor == 0 {
		t.Errorf("the cursor is %d, %v; want the journal to have moved", cursor, err)
	}
	if _, err := replica.UVByID(ctx, "uv_one"); err == nil {
		t.Errorf("the skipped row was written anyway")
	}
}

// Two servers that each took the same node while the mesh was apart end at the same home term with different
// homes: nothing in the row says which one is right. The merge must refuse rather than pick — that is the
// split-brain guard, and the reason an operator has to be sure a home is gone before adopting.
func TestAnEqualHomeTermFromAnotherHomeIsAConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	homeID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	enrolled(t, home, "uv_one", "one.example.com")
	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}

	// One side takes the node while the mesh is apart ...
	replicaID, err := replica.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if _, err := replica.AdoptUV(ctx, "uv_one", homeID, testNow); err != nil {
		t.Fatalf("AdoptUV: %v", err)
	}
	// ... and the other raises its own row to the same term at the same time: neither saw the other.
	taken := testUV("uv_one", "one.example.com")
	taken.HomeMN, taken.HomeTerm = homeID, 1
	event, err := home.AppendNode(ctx, taken, nil)
	if err != nil {
		t.Fatalf("AppendNode: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{event}); !errors.Is(err, store.ErrReplicationConflict) {
		t.Fatalf("the equal term = %v, want a conflict", err)
	}
	got, err := replica.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if got.HomeMN != replicaID || got.HomeTerm != 1 {
		t.Errorf("the conflict changed the row to home %q term %d, want %q term 1",
			got.HomeMN, got.HomeTerm, replicaID)
	}
}

// A replica answers the subscription too, so the renderings have to reach it: through the journal for a
// write, and through the snapshot for a server that starts from one.
func TestTheRenderingsReplicateThroughTheJournal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	node := enrolled(t, home, "uv_one", "one.example.com")

	if err := home.SetUVTransports(ctx, node.ID, []string{"vless"}, []string{"websocket"}); err != nil {
		t.Fatalf("SetUVTransports: %v", err)
	}
	mnID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	events := journal(t, home, mnID)
	if len(events) == 0 {
		t.Fatal("no journal entry")
	}
	if _, err := replica.ApplyEvents(ctx, events); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	renderings, err := replica.UVRenderings(ctx)
	if err != nil {
		t.Fatalf("UVRenderings: %v", err)
	}
	if got := renderings[node.ID]; len(got) != 1 || got[0] != "websocket" {
		t.Errorf("the replica stored %v, want [websocket]", got)
	}

	// And the snapshot, which is how a peer that is too far behind catches up.
	snap, err := home.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snap.Renderings[node.ID]; len(got) != 1 || got[0] != "websocket" {
		t.Errorf("the snapshot carries %v", got)
	}
}
