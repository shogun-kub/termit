package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// scheduled writes a node whose schedule says it may not sync yet and which already applied a mesh.
func scheduled(t *testing.T, next time.Time, mesh string) string {
	t.Helper()
	path := enrolled(t, cycleNow.Add(-3*time.Hour))
	st, err := state.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st.Schedule.NextSyncAt = next
	st.MeshGeneration = mesh
	if err := st.SaveAt(path, cycleNow); err != nil {
		t.Fatalf("save: %v", err)
	}
	return path
}

// withMeshPoll states the poll interval the server asked for; nil means it never said.
func withMeshPoll(t *testing.T, path string, seconds *int) {
	t.Helper()
	st, err := state.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st.MeshPollSeconds = seconds
	if err := st.SaveAt(path, cycleNow); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func seconds(n int) *int { return &n }

// drain stops the loop after a few wake-ups without sleeping, and records every requested wait.
func drain(loop *agent.Loop, wakeups int, waits *[]time.Duration) {
	loop.Sleep = func(_ context.Context, d time.Duration) bool {
		*waits = append(*waits, d)
		return len(*waits) < wakeups
	}
}

func TestTheLoopWaitsForTheSchedule(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("the loop synced before it was due: %s", r.URL.Path)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mesh_generation": "mesh-one"})
	}))
	defer server.Close()

	statePath := scheduled(t, cycleNow.Add(time.Hour), "mesh-one")
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			t.Error("the core was reconciled before the schedule allowed it")
			return agent.Result{}, nil
		},
	}
	var waits []time.Duration
	drain(loop, 3, &waits)
	_ = loop.Run(context.Background())

	if len(waits) != 3 {
		t.Fatalf("waits = %v, want three wake-ups", waits)
	}
	for i, wait := range waits {
		// A node that is not due must wake on the cheap poll, not sleep the whole interval away.
		if want := 15 * time.Minute; wait != want {
			t.Errorf("wait %d = %s, want %s", i, wait, want)
		}
	}
}

func TestTheLoopReconcilesWhenTheMeshChanges(t *testing.T) {
	t.Parallel()
	var synced int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mesh_generation": "mesh-two"})
			return
		}
		synced++
		answer := answerAt(cycleNow.Add(24 * time.Hour))
		answer.MeshGeneration = "mesh-two"
		_ = json.NewEncoder(w).Encode(answer)
	}))
	defer server.Close()

	statePath := scheduled(t, cycleNow.Add(time.Hour), "mesh-one")
	var applied int
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			applied++
			return agent.Result{Clients: 2}, nil
		},
	}
	var waits []time.Duration
	drain(loop, 4, &waits)
	_ = loop.Run(context.Background())

	if len(waits) == 0 {
		t.Fatal("the loop never woke")
	}
	if synced != 1 || applied != 1 {
		t.Fatalf("synced %d, applied %d times, want exactly one of each", synced, applied)
	}
	st, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if st.MeshGeneration != "mesh-two" {
		t.Errorf("stored generation = %q, want the one that was applied", st.MeshGeneration)
	}
	if st.Schedule.LastResult != "ok" {
		t.Errorf("schedule = %+v", st.Schedule)
	}
}

func TestAnUnreadableMeshDigestChangesNothing(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	statePath := scheduled(t, cycleNow.Add(time.Hour), "mesh-one")
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			t.Error("a failed poll reconciled the core")
			return agent.Result{}, nil
		},
	}
	var waits []time.Duration
	drain(loop, 3, &waits)
	_ = loop.Run(context.Background())
	if len(waits) != 3 {
		t.Fatalf("waits = %v", waits)
	}
	st, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if st.MeshGeneration != "mesh-one" {
		t.Errorf("generation = %q, a failed poll must not change the state", st.MeshGeneration)
	}
}

func TestTheLoopPollsOnTheIntervalTheServerSet(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mesh_generation": "mesh-one"})
	}))
	defer server.Close()

	statePath := scheduled(t, cycleNow.Add(time.Hour), "mesh-one")
	withMeshPoll(t, statePath, seconds(60))
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			t.Error("the core was reconciled before the schedule allowed it")
			return agent.Result{}, nil
		},
	}
	var waits []time.Duration
	drain(loop, 2, &waits)
	_ = loop.Run(context.Background())
	for i, wait := range waits {
		if want := time.Minute; wait != want {
			t.Errorf("wait %d = %s, want the server's %s", i, wait, want)
		}
	}
}

func TestTheLoopStopsPollingWhenTheServerSaysZero(t *testing.T) {
	t.Parallel()
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mesh_generation": "mesh-two"})
	}))
	defer server.Close()

	statePath := scheduled(t, cycleNow.Add(time.Hour), "mesh-one")
	withMeshPoll(t, statePath, seconds(0))
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			t.Error("a disabled poll still reconciled the core")
			return agent.Result{}, nil
		},
	}
	var waits []time.Duration
	drain(loop, 2, &waits)
	_ = loop.Run(context.Background())
	if polls != 0 {
		t.Errorf("the node made %d health requests with the poll turned off", polls)
	}
	for i, wait := range waits {
		if want := time.Hour; wait != want {
			t.Errorf("wait %d = %s, want the whole interval %s", i, wait, want)
		}
	}
}
