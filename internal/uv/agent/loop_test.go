package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/state"
)

var cycleNow = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

// enrolled writes a minimal enrolled node and returns its state path.
func enrolled(t *testing.T, at time.Time) string {
	t.Helper()
	st := state.New("one.example.com", at.Add(-24*time.Hour))
	st.Identity.SelfUUID = selfUUID
	st.Identity.XHTTPPath = "secretpath"
	st.Credentials = &state.Credentials{AgentKey: "agent-key", EnrolledAt: at}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.SaveAt(path, at); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return path
}

func answerAt(next time.Time) proto.SyncResponse {
	response := answer()
	response.NextSyncAfter = next
	response.Policy = proto.SyncPolicy{
		CheckIntervalSeconds: int((24 * time.Hour).Seconds()),
		SyncIntervalSeconds:  int((24 * time.Hour).Seconds()),
		MinSyncDelaySeconds:  int((2 * time.Hour).Seconds()),
	}
	return response
}

func TestNextDueHonoursTheServerAndTheEnrollmentFloor(t *testing.T) {
	t.Parallel()
	enrolledAt := cycleNow.Add(-3 * time.Hour)

	t.Run("the server's absolute time wins, plus this node's jitter", func(t *testing.T) {
		t.Parallel()
		want := cycleNow.Add(20 * time.Hour)
		got := agent.NextDue(cycleNow, answerAt(want), enrolledAt, selfUUID)
		if got.Before(want) {
			t.Errorf("NextDue = %s, before the server's %s", got, want)
		}
		if got.After(want.Add(10 * time.Minute)) {
			t.Errorf("NextDue = %s, more than the jitter after %s", got, want)
		}
		if again := agent.NextDue(cycleNow, answerAt(want), enrolledAt, selfUUID); !again.Equal(got) {
			t.Errorf("jitter is not stable: %s then %s", got, again)
		}
		if other := agent.NextDue(cycleNow, answerAt(want), enrolledAt, "another-node"); other.Equal(got) {
			t.Error("two different nodes got the same jitter")
		}
	})

	t.Run("never earlier than the enrollment floor", func(t *testing.T) {
		t.Parallel()
		justEnrolled := cycleNow
		// The server answered with a time inside the two-hour window; the floor must win.
		got := agent.NextDue(cycleNow, answerAt(cycleNow.Add(10*time.Minute)), justEnrolled, selfUUID)
		floor := justEnrolled.Add(2 * time.Hour)
		if got.Before(floor) {
			t.Errorf("NextDue = %s, before the floor %s", got, floor)
		}
	})

	t.Run("an empty answer falls back to the interval", func(t *testing.T) {
		t.Parallel()
		got := agent.NextDue(cycleNow, proto.SyncResponse{}, enrolledAt, "")
		if got.Before(cycleNow.Add(24 * time.Hour)) {
			t.Errorf("NextDue = %s, want at least a day", got)
		}
	})

	t.Run("a due time in the past becomes now", func(t *testing.T) {
		t.Parallel()
		got := agent.NextDue(cycleNow, answerAt(cycleNow.Add(-time.Hour)), time.Time{}, "")
		if got.Before(cycleNow) {
			t.Errorf("NextDue = %s, want not in the past", got)
		}
		if got.After(cycleNow.Add(time.Minute)) {
			t.Errorf("NextDue = %s, want about now", got)
		}
	})
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	t.Parallel()
	want := []time.Duration{
		5 * time.Minute,
		10 * time.Minute,
		20 * time.Minute,
		40 * time.Minute,
		time.Hour,
		time.Hour,
	}
	for i, expected := range want {
		if got := agent.Backoff(i + 1); got != expected {
			t.Errorf("Backoff(%d) = %s, want %s", i+1, got, expected)
		}
	}
	if got := agent.Backoff(0); got != 5*time.Minute {
		t.Errorf("Backoff(0) = %s, want the first step", got)
	}
}

func TestCycleAppliesTheAnswerAndPersistsTheSchedule(t *testing.T) {
	t.Parallel()
	var seen proto.SyncRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_ = json.NewEncoder(w).Encode(answerAt(cycleNow.Add(24 * time.Hour)))
	}))
	defer server.Close()

	statePath := enrolled(t, cycleNow.Add(-3*time.Hour))
	var applied bool
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Check: func(context.Context, *state.State) state.SelfCheckReport {
			return state.SelfCheckReport{At: cycleNow, OK: true, Items: []state.SelfCheckItem{{Name: "xray", OK: true}}}
		},
		Apply: func(_ context.Context, st *state.State, got proto.SyncResponse) (agent.Result, error) {
			applied = true
			if len(got.Peers) != 1 {
				t.Errorf("Apply got %d peers", len(got.Peers))
			}
			return agent.Result{Clients: 3, Added: []string{peerUUID}}, nil
		},
	}
	report, err := loop.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if !applied || report.Result != "ok" {
		t.Fatalf("cycle did not apply: report=%+v", report)
	}
	if seen.Check == nil || !seen.Check.OK {
		t.Errorf("the sync request carried no self-check: %+v", seen)
	}
	st, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.Schedule.Failures != 0 || st.Schedule.LastResult != "ok" {
		t.Errorf("schedule = %+v", st.Schedule)
	}
	if st.Schedule.NextSyncAt.Before(cycleNow.Add(24 * time.Hour)) {
		t.Errorf("next sync = %s, before the server's time", st.Schedule.NextSyncAt)
	}
	if len(st.Peers) != 1 || st.Peers[0].ID != "uv_two" {
		t.Errorf("peers = %+v", st.Peers)
	}
	if st.LastSelfCheck == nil || !st.LastSelfCheck.OK {
		t.Errorf("the self-check was not persisted: %+v", st.LastSelfCheck)
	}
}

func TestCycleLeavesTheCoreAloneWhenTheServerIsUnreachable(t *testing.T) {
	t.Parallel()
	statePath := enrolled(t, cycleNow.Add(-3*time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := server.URL
	server.Close() // now the address refuses connections

	var applied bool
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: base},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			applied = true
			return agent.Result{}, nil
		},
	}
	if _, err := loop.Once(context.Background()); err == nil {
		t.Fatal("Once succeeded against a dead server")
	}
	if applied {
		t.Error("the core was changed while the server was unreachable")
	}
	st, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.Schedule.Failures != 1 || st.Schedule.LastResult != "failed" {
		t.Errorf("schedule = %+v", st.Schedule)
	}
	if want := cycleNow.Add(5 * time.Minute); !st.Schedule.NextSyncAt.Equal(want) {
		t.Errorf("next sync = %s, want %s", st.Schedule.NextSyncAt, want)
	}
}

func TestCycleResetsTheBackoffAfterASuccess(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(answerAt(cycleNow.Add(24 * time.Hour)))
	}))
	defer server.Close()

	statePath := enrolled(t, cycleNow.Add(-3*time.Hour))
	st, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	st.Schedule.Failures = 4
	st.Schedule.NextSyncAt = cycleNow.Add(-time.Minute)
	if err := st.SaveAt(statePath, cycleNow); err != nil {
		t.Fatalf("save: %v", err)
	}

	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			return agent.Result{Clients: 1}, nil
		},
	}
	if _, err := loop.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	st, err = state.Load(statePath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if st.Schedule.Failures != 0 {
		t.Errorf("failures = %d, want 0 after a success", st.Schedule.Failures)
	}
}

func TestCycleDoesNotHammerAServerThatRefusedTheNode(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(proto.NewProblem(http.StatusForbidden,
			proto.CodeNodeInactive, "Forbidden", "this node is not in the mesh"))
	}))
	defer server.Close()

	statePath := enrolled(t, cycleNow.Add(-3*time.Hour))
	var applied bool
	loop := &agent.Loop{
		Remote:    agent.Remote{BaseURL: server.URL},
		StatePath: statePath,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			applied = true
			return agent.Result{}, nil
		},
	}
	_, err := loop.Once(context.Background())
	if err == nil {
		t.Fatal("Once succeeded on a refused node")
	}
	var refusal *agent.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a refusal", err)
	}
	if applied {
		t.Error("the core was changed for a refused node")
	}
	st, loadErr := state.Load(statePath)
	if loadErr != nil {
		t.Fatalf("load: %v", loadErr)
	}
	if st.Schedule.LastResult != "refused" || st.Schedule.Failures != 0 {
		t.Errorf("schedule = %+v", st.Schedule)
	}
	if st.Schedule.NextSyncAt.Before(cycleNow.Add(23 * time.Hour)) {
		t.Errorf("next sync = %s, a refused node must wait for the normal interval", st.Schedule.NextSyncAt)
	}
}

func TestCycleRefusesToRunAnUnenrolledNode(t *testing.T) {
	t.Parallel()
	st := state.New("one.example.com", cycleNow.Add(-time.Hour))
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.SaveAt(path, cycleNow); err != nil {
		t.Fatalf("save: %v", err)
	}
	loop := &agent.Loop{
		StatePath: path,
		Now:       func() time.Time { return cycleNow },
		Apply: func(context.Context, *state.State, proto.SyncResponse) (agent.Result, error) {
			return agent.Result{}, nil
		},
	}
	if _, err := loop.Once(context.Background()); !errors.Is(err, agent.ErrNotEnrolled) {
		t.Fatalf("Once = %v, want ErrNotEnrolled", err)
	}
}
