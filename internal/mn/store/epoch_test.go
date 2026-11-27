package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// The epoch is what re-keys the mesh: every pair credential is derived from it, so a change is a migration
// every management server has to acknowledge (docs/IMPLEMENTATION_PLAN.md §5.2.1). Nothing in this build may
// move it by accident, and this test is what says so: the operators' commands are run and the epoch is read
// back unchanged.
func TestNoCommandChangesTheCredentialEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTestStore(t)
	node := testUV("uv_one", "one.example.com")
	node.CredentialEpoch = 3
	if err := s.CreateUV(ctx, node); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	writes := []struct {
		name string
		do   func() error
	}{
		{"state change", func() error {
			_, err := s.SetUVState(ctx, node.ID, store.StateActive, "approved", "test", testNow)
			return err
		}},
		{"transports", func() error { return s.SetUVTransports(ctx, node.ID, []string{"hysteria2"}) }},
		{"agent key", func() error { return s.SetAgentKeyHash(ctx, node.ID, "new-hash", "new-sealed") }},
		{"subscription token", func() error { return s.SetSubTokenHash(ctx, node.ID, "new-hash", "new-sealed") }},
		{"certificate pin", func() error { return s.SetUVCertPin(ctx, node.ID, strings.Repeat("ab", 32)) }},
		{"sighting", func() error { return s.TouchUV(ctx, node.ID, "192.0.2.1", "v0.7.1", testNow) }},
		{"schedule", func() error { return s.ScheduleCheck(ctx, node.ID, testNow.Add(time.Hour), testNow) }},
		{"failed cycles", func() error { return s.SetFailedCycles(ctx, node.ID, 2) }},
		{"relocation", func() error {
			return s.RelocateUV(ctx, node.ID, "moved.example.com", "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
				`{"network":"xhttp"}`, "")
		}},
	}
	for _, write := range writes {
		if err := write.do(); err != nil {
			t.Fatalf("%s: %v", write.name, err)
		}
		got, err := s.UVByID(ctx, node.ID)
		if err != nil {
			t.Fatalf("UVByID after %s: %v", write.name, err)
		}
		if got.CredentialEpoch != 3 {
			t.Fatalf("%s moved the epoch to %d; it is a migration, not a field", write.name, got.CredentialEpoch)
		}
	}
}

// A handoff and an adoption keep the epoch too: the node id and the master key survive both, so its pair
// credentials must keep deriving the same values (a rotation is a separate, quorum-gated operation).
func TestMovingANodeKeepsItsEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, other := twoStores(t)
	enrolled(t, home, "uv_one", "one.example.com")
	homeID, _ := home.MeshID(ctx)
	if _, err := other.ApplyEvents(ctx, journal(t, home, homeID)); err != nil {
		t.Fatalf("ApplyEvents: %v", err)
	}
	before, err := other.UVByID(ctx, "uv_one")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	taken, err := other.AcceptHandoff(ctx, "uv_one", homeID, "hash", "sealed", testNow)
	if err != nil {
		t.Fatalf("AcceptHandoff: %v", err)
	}
	if taken.CredentialEpoch != before.CredentialEpoch {
		t.Errorf("the handoff moved the epoch from %d to %d", before.CredentialEpoch, taken.CredentialEpoch)
	}
	if err := home.HandoffOut(ctx, "uv_one", taken.HomeMN, taken.HomeTerm, "hash", "sealed"); err != nil {
		t.Fatalf("HandoffOut: %v", err)
	}
	adopted, err := home.AdoptUV(ctx, "uv_one", taken.HomeMN, testNow)
	if err != nil {
		t.Fatalf("AdoptUV: %v", err)
	}
	if adopted.CredentialEpoch != before.CredentialEpoch {
		t.Errorf("the adoption moved the epoch from %d to %d", before.CredentialEpoch, adopted.CredentialEpoch)
	}
	if _, err := other.UVByID(ctx, "uv_one"); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UVByID: %v", err)
	}
}
