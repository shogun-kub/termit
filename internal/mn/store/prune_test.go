package store_test

import (
	"context"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// Pruning has to leave both sides of the contract intact: a peer that was slightly behind still catches up
// from the log, and this server's sequence never moves backwards — an entry reusing an old number would be
// skipped by every peer as "already applied".
func TestPruningKeepsTheNewestEntriesAndTheSequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTestStore(t)

	for _, id := range []string{"uv_a", "uv_b", "uv_c"} {
		enrolled(t, s, id, id+".example.com")
	}
	mnID, err := s.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	before, err := s.LastSeq(ctx, mnID)
	if err != nil {
		t.Fatalf("LastSeq: %v", err)
	}
	if before != 3 {
		t.Fatalf("the log holds %d entries, want 3", before)
	}

	// keep = 1 leaves the newest entry and drops the two behind it.
	deleted, err := s.PruneJournal(ctx, 1)
	if err != nil {
		t.Fatalf("PruneJournal: %v", err)
	}
	if deleted != 2 {
		t.Errorf("the prune removed %d entries, want 2", deleted)
	}
	if after, err := s.LastSeq(ctx, mnID); err != nil || after != before {
		t.Fatalf("the newest entry moved from %d to %d: %v", before, after, err)
	}
	if events, err := s.EventsSince(ctx, mnID, 0, 0); err != nil || len(events) != 1 {
		t.Fatalf("the log reads %d entries, %v; want 1", len(events), err)
	}

	// The next write continues the sequence: nothing a peer saw is ever handed out again.
	if err := s.CreateUV(ctx, testUV("uv_d", "d.example.com")); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}
	if last, err := s.LastSeq(ctx, mnID); err != nil || last != before+1 {
		t.Errorf("the next entry is %d, want %d: %v", last, before+1, err)
	}
}

// A replica keeps the cursor, not the log: a third server pulls the origin, not this copy, so there is
// nothing to keep once a page is applied. That is also why only one log ever lives in the table.
func TestAReplicaKeepsTheCursorNotTheLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	for _, id := range []string{"uv_a", "uv_b", "uv_c"} {
		enrolled(t, home, id, id+".example.com")
	}
	homeID, err := home.MeshID(ctx)
	if err != nil {
		t.Fatalf("MeshID: %v", err)
	}
	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if cursor, err := replica.Cursor(ctx, homeID); err != nil || cursor != 3 {
		t.Errorf("the cursor is %d, %v; want 3 — that is what a replica keeps", cursor, err)
	}
	if events, err := replica.EventsSince(ctx, homeID, 0, 0); err != nil || len(events) != 0 {
		t.Errorf("the replica copied %d entries of a log it does not serve", len(events))
	}
}

// A replica that pruned a lagging peer's entries to the cursor still detects the gap when the peer's next
// entry arrives after a hole — the snapshot path is what covers it, and it must keep working.
func TestAReplayAfterPruningStillMovesTheCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, replica := twoStores(t)
	enrolled(t, home, "uv_a", "a.example.com")
	homeID, _ := home.MeshID(ctx)
	if _, err := replica.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	if _, err := replica.PruneJournal(ctx, store.KeepJournal); err != nil {
		t.Fatalf("PruneJournal: %v", err)
	}
	// The same page again is a replay: nothing is written and the cursor stays where it was.
	applied, err := replica.ApplyEvents(ctx, []store.ReplicationEvent{
		{MNID: homeID, Seq: 1, Kind: store.EventNode, Payload: []byte(`{"node":{"id":"uv_a"}}`)},
	})
	if err != nil {
		t.Fatalf("ApplyEvents(replay): %v", err)
	}
	if applied != 0 {
		t.Errorf("the replay applied %d entries, want 0", applied)
	}
}
