package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/state"
)

const (
	selfUUID  = "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5"
	probeUUID = "11111111-2222-4333-8444-555555555555"
	peerUUID  = "9821a6ed-3746-47b3-a5d2-149af557bf2f"
)

func answer() proto.SyncResponse {
	response := proto.SyncResponse{APIVersion: proto.APIVersion}
	response.Self.ID = "uv_one"
	response.Self.Domain = "one.example.com"
	response.Self.State = "active"
	response.Self.ProbeUUID = probeUUID
	response.Peers = []proto.SyncPeer{{
		ID: "uv_two", Domain: "two.example.com", UUID: peerUUID, Path: "/other/", Mode: "packet-up", State: "active",
	}}
	return response
}

// fakeRunner records the commands the applier runs and can fail the restart, which is the branch that
// has to roll back.
type fakeRunner struct {
	calls       []string
	failRestart bool
}

func (r *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name == "systemctl" && r.failRestart {
		return nil, errors.New("the unit refused to start")
	}
	return nil, nil
}

func TestFetchSendsTheAgentKeyAndReadsTheAnswer(t *testing.T) {
	t.Parallel()
	var (
		gotKey  string
		gotPath string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer())
	}))
	defer server.Close()

	remote := agent.Remote{BaseURL: server.URL}
	got, err := remote.Fetch(context.Background(), "agent-key")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotKey != "Bearer agent-key" {
		t.Errorf("authorization = %q", gotKey)
	}
	if gotPath != "/api/v1/agent/sync" {
		t.Errorf("path = %q", gotPath)
	}
	if len(got.Peers) != 1 || got.Peers[0].UUID != peerUUID {
		t.Errorf("answer = %+v", got)
	}
}

func TestFetchReportsARefusal(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(proto.NewProblem(http.StatusForbidden, "node_inactive", "Forbidden", "this node is not in the mesh"))
	}))
	defer server.Close()

	_, err := agent.Remote{BaseURL: server.URL}.Fetch(context.Background(), "key")
	if err == nil {
		t.Fatal("Fetch accepted a refusal")
	}
	if !strings.Contains(err.Error(), "node_inactive") {
		t.Errorf("error %q does not name the problem code", err)
	}
}

func TestFetchRefusesToRunUnenrolled(t *testing.T) {
	t.Parallel()
	if _, err := (agent.Remote{BaseURL: "https://mn.example.com"}).Fetch(context.Background(), ""); err == nil {
		t.Fatal("Fetch ran without an agent key")
	}
	if _, err := (agent.Remote{}).Fetch(context.Background(), "key"); err == nil {
		t.Fatal("Fetch ran without a server")
	}
}

func applier(t *testing.T) (agent.Applier, string) {
	t.Helper()
	dir := t.TempDir()
	return agent.Applier{
		Binary:     "/usr/bin/xray",
		ConfigPath: filepath.Join(dir, "xray.json"),
		Path:       "/secret/",
		SelfUUID:   selfUUID,
		SelfEmail:  "owner",
	}, dir
}

func TestApplyWritesTheDesiredClients(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"

	result, err := app.Apply(context.Background(), answer(), []state.LocalUser{{ID: "aaaaaaaa-1111-4111-8111-111111111111", Label: "phone"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Unchanged {
		t.Error("the first apply reported no change")
	}
	if result.Clients != 4 {
		t.Errorf("clients = %d, want self + probe + peer + one local device", result.Clients)
	}

	body, err := os.ReadFile(filepath.Join(dir, "xray.json"))
	if err != nil {
		t.Fatalf("read the config: %v", err)
	}
	for _, want := range []string{selfUUID, probeUUID, peerUUID, "/secret/"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the config does not contain %s", want)
		}
	}
	// The core was checked before the file was installed and restarted after it.
	if len(runner.calls) != 2 || !strings.Contains(runner.calls[0], "-test") || !strings.Contains(runner.calls[1], "restart") {
		t.Errorf("commands = %v", runner.calls)
	}

	// A second apply with the same answer changes nothing and does not restart the core.
	runner.calls = nil
	again, err := app.Apply(context.Background(), answer(), []state.LocalUser{{ID: "aaaaaaaa-1111-4111-8111-111111111111", Label: "phone"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !again.Unchanged {
		t.Error("the second apply reported a change")
	}
	if len(runner.calls) != 0 {
		t.Errorf("the second apply ran %v", runner.calls)
	}
}

func TestApplyDropsAPeerThatLeft(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.Runner = &fakeRunner{}
	app.Unit = "termit-xray.service"
	if _, err := app.Apply(context.Background(), answer(), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The server now hands out a set without that peer: the credential must disappear from the core,
	// or a removed node keeps a key to this one.
	changed := answer()
	changed.Peers = nil
	result, err := app.Apply(context.Background(), changed, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != peerUUID {
		t.Errorf("removed = %v, want the departed peer", result.Removed)
	}
	body, err := os.ReadFile(filepath.Join(dir, "xray.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), peerUUID) {
		t.Error("the departed peer is still accepted by the core")
	}
	if !strings.Contains(string(body), selfUUID) || !strings.Contains(string(body), probeUUID) {
		t.Error("the owner or the probe credential was dropped as well")
	}
}

func TestApplyRollsBackWhenTheCoreRefusesToRestart(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	if _, err := app.Apply(context.Background(), answer(), nil); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	previous, err := os.ReadFile(filepath.Join(dir, "xray.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	app.Runner = &fakeRunner{failRestart: true}
	app.Unit = "termit-xray.service"
	changed := answer()
	changed.Peers = nil // a set without the peer: a real change that the core will refuse
	_, err = app.Apply(context.Background(), changed, nil)
	if err == nil {
		t.Fatal("Apply ignored a failed restart")
	}
	if !strings.Contains(err.Error(), "restored") {
		t.Errorf("error %q does not say the previous configuration was restored", err)
	}

	restored, err := os.ReadFile(filepath.Join(dir, "xray.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(restored) != string(previous) {
		t.Error("the previous configuration was not restored")
	}
}

func TestPreviewReportsDriftWithoutWriting(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.Runner = &fakeRunner{}
	app.Unit = "termit-xray.service"

	// Nothing on disk yet: everything is drift, and the preview must not create the file.
	first, err := app.Preview(answer(), nil)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if first.Unchanged {
		t.Error("Preview claimed the node was in sync with no config at all")
	}
	if _, err := os.Stat(filepath.Join(dir, "xray.json")); !os.IsNotExist(err) {
		t.Error("Preview wrote the config; a check must not change what it measures")
	}

	// Apply, then preview again: now it is in sync.
	if _, err := app.Apply(context.Background(), answer(), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	second, err := app.Preview(answer(), nil)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !second.Unchanged {
		t.Errorf("Preview reported drift right after a successful apply: %+v", second)
	}
}
