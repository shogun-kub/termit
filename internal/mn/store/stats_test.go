package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// schedule puts a node in a state with a next check, which is all Queue and StateCounts look at.
func schedule(t *testing.T, s *store.Store, id, domain string, state store.State, next *time.Time) {
	t.Helper()
	ctx := context.Background()
	node := testUV(id, domain)
	node.State = state
	if err := s.CreateUV(ctx, node); err != nil {
		t.Fatalf("CreateUV(%s): %v", id, err)
	}
	if next == nil {
		return
	}
	if err := s.ScheduleCheck(ctx, id, *next, testNow); err != nil {
		t.Fatalf("ScheduleCheck(%s): %v", id, err)
	}
}

func TestStateCountsIncludeTheEmptyStates(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	past := testNow.Add(-time.Hour)
	schedule(t, s, "uv_1", "one.example.com", store.StateActive, &past)
	schedule(t, s, "uv_2", "two.example.com", store.StateActive, &past)

	counts, err := s.StateCounts(context.Background())
	if err != nil {
		t.Fatalf("StateCounts: %v", err)
	}
	if counts[string(store.StateActive)] != 2 {
		t.Errorf("active = %d, want 2", counts[string(store.StateActive)])
	}
	// A dashboard renders a fixed set of gauges; a missing key would silently hide a state.
	for _, state := range []string{"pending", "unavailable", "blocked", "removed"} {
		if _, ok := counts[state]; !ok {
			t.Errorf("state %q is missing from %v", state, counts)
		}
	}
}

func TestQueueSeparatesDueFromScheduled(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()

	long := testNow.Add(-48 * time.Hour)
	soon := testNow.Add(-5 * time.Minute)
	later := testNow.Add(6 * time.Hour)

	schedule(t, s, "uv_1", "one.example.com", store.StateActive, &long)
	schedule(t, s, "uv_2", "two.example.com", store.StateUnavailable, &soon)
	schedule(t, s, "uv_3", "three.example.com", store.StateActive, &later)
	schedule(t, s, "uv_4", "four.example.com", store.StateActive, nil) // never scheduled: due
	schedule(t, s, "uv_5", "five.example.com", store.StateRemoved, &long)

	stats, err := s.Queue(ctx, testNow)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	// Three due: the two past their time and the one never scheduled. The removed node is not the
	// scheduler's business, and neither is the future one.
	if stats.Due != 3 {
		t.Errorf("due = %d, want 3", stats.Due)
	}
	if stats.OldestDue == nil || !stats.OldestDue.Equal(long) {
		t.Errorf("oldest due = %v, want %s", stats.OldestDue, long)
	}
	if stats.NextDue == nil || !stats.NextDue.Equal(later) {
		t.Errorf("next due = %v, want %s", stats.NextDue, later)
	}
}

func TestQueueIsEmptyWithoutNodes(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)

	stats, err := s.Queue(context.Background(), testNow)
	if err != nil {
		t.Fatalf("Queue: %v", err)
	}
	if stats.Due != 0 || stats.OldestDue != nil || stats.NextDue != nil {
		t.Errorf("stats = %+v, want zero", stats)
	}
}
