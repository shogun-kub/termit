package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// health reads the unauthenticated health answer.
func (f *fixture) health(t *testing.T) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz = %d: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func (f *fixture) meshGeneration(t *testing.T) string {
	t.Helper()
	generation, _ := f.health(t)["mesh_generation"].(string)
	if generation == "" {
		t.Fatal("healthz carries no mesh_generation")
	}
	return generation
}

func TestTheMeshDigestChangesWhenTheMeshDoes(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	ctx := context.Background()

	before := fx.meshGeneration(t)

	// The same registry answers the same digest, and a sync that only refreshes last_seen is not a
	// change: a node whose digest moved on every touch would sync the whole fleet every poll.
	response := httptest.NewRecorder()
	fx.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))
	if err := fx.store.TouchUV(ctx, fx.nodeID, "203.0.113.10", "termit-uv/test", testNow); err != nil {
		t.Fatalf("TouchUV: %v", err)
	}
	if after := fx.meshGeneration(t); after != before {
		t.Errorf("a touch changed the digest: %s then %s", before, after)
	}

	// The sync answer carries the digest the node applies, so the poll does not fire again for a mesh
	// this node already has.
	synced := fx.sync(t, fx.agentKey, `{"agent_version":"termit-uv/test"}`)
	var answer proto.SyncResponse
	if err := json.Unmarshal(synced.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the sync: %v", err)
	}
	if answer.MeshGeneration != before {
		t.Errorf("sync digest = %q, healthz says %q", answer.MeshGeneration, before)
	}

	// A node leaving the mesh is a change.
	if _, err := fx.store.SetUVState(ctx, fx.peerID, store.StateBlocked, "blocked by a test", "test", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	if after := fx.meshGeneration(t); after == before {
		t.Error("blocking a node left the digest unchanged")
	}
}

func TestThePolicyCarriesTheMeshPollInterval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		poll    time.Duration
		wantSec int
	}{
		{"the operator's interval", 90 * time.Second, 90},
		{"polling turned off", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newFixtureWith(t, okProber{}, func(o *server.Options) { o.MeshPoll = tc.poll })
			synced := fx.sync(t, fx.agentKey, `{"agent_version":"termit-uv/test"}`)
			var answer proto.SyncResponse
			if err := json.Unmarshal(synced.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode the sync: %v", err)
			}
			if answer.Policy.MeshPollSeconds != tc.wantSec {
				t.Errorf("mesh_poll_seconds = %d, want %d", answer.Policy.MeshPollSeconds, tc.wantSec)
			}
		})
	}
}
