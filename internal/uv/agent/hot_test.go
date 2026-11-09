package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// fakeHot is the core API in memory: it keeps the live set and applies the same rules the real one
// does, so a test can observe a reconciliation end to end without an Xray process.
type fakeHot struct {
	live     []agent.LiveUser
	adds     []xray.Client
	removes  []string
	failRead bool
	failAdd  bool
}

func (f *fakeHot) Users(context.Context) ([]agent.LiveUser, error) {
	if f.failRead {
		return nil, errors.New("the API is not reachable")
	}
	return f.live, nil
}

func (f *fakeHot) Add(_ context.Context, clients []xray.Client) error {
	if f.failAdd {
		return errors.New("the core refused the users")
	}
	f.adds = append(f.adds, clients...)
	for _, client := range clients {
		f.live = append(f.live, agent.LiveUser{Email: client.Email, ID: strings.ToLower(client.ID)})
	}
	return nil
}

func (f *fakeHot) Remove(_ context.Context, emails []string) error {
	f.removes = append(f.removes, emails...)
	drop := make(map[string]bool, len(emails))
	for _, email := range emails {
		drop[email] = true
	}
	kept := f.live[:0]
	for _, user := range f.live {
		if !drop[user.Email] {
			kept = append(kept, user)
		}
	}
	f.live = kept
	return nil
}

// fakeCanary answers with a fixed sequence: nil is success, an error is a failed canary.
type fakeCanary struct {
	results []error
	calls   int
}

func (c *fakeCanary) Check(context.Context) error {
	c.calls++
	if c.calls <= len(c.results) {
		return c.results[c.calls-1]
	}
	return nil
}

// writeLiveConfig writes the configuration a node had before the change: the owner and the probe
// credential, no peers.
func writeLiveConfig(t *testing.T, app agent.Applier) []byte {
	t.Helper()
	spec := xray.DefaultSpec()
	spec.Path = app.Path
	spec.Clients = []xray.Client{
		{ID: app.SelfUUID, Email: "owner"},
		{ID: probeUUID, Email: "probe@termit"},
	}
	config, err := xray.Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.WriteFile(app.ConfigPath, config, 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return config
}

func TestApplyUsesTheAPIAndSkipsTheRestart(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.ConfigPath = filepath.Join(dir, "xray.json")
	writeLiveConfig(t, app)
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"
	app.Hot = &fakeHot{live: []agent.LiveUser{
		{Email: "owner", ID: selfUUID},
		{Email: "probe@termit", ID: probeUUID},
	}}
	canary := &fakeCanary{}
	app.Canary = canary

	result, err := app.Apply(context.Background(), answer(), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Hot {
		t.Error("Apply did not use the API")
	}
	if !result.CanaryRan || canary.calls != 1 {
		t.Errorf("canary ran %d time(s), want once", canary.calls)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "systemctl restart") {
			t.Errorf("the core was restarted despite a successful API update: %s", call)
		}
	}
	data, err := os.ReadFile(app.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), peerUUID) {
		t.Error("the installed config does not carry the new peer")
	}
}

func TestApplyFallsBackToARestartWhenTheAPIIsUnavailable(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.ConfigPath = filepath.Join(dir, "xray.json")
	writeLiveConfig(t, app)
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"
	app.Hot = &fakeHot{failRead: true}
	app.Canary = &fakeCanary{}

	result, err := app.Apply(context.Background(), answer(), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Hot {
		t.Error("Apply claims the API succeeded although it could not read the users")
	}
	restarted := false
	for _, call := range runner.calls {
		if strings.Contains(call, "systemctl restart termit-xray.service") {
			restarted = true
		}
	}
	if !restarted {
		t.Error("the fallback did not restart the core")
	}
}

func TestApplyFallsBackToARestartWhenTheAPIAddFails(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.ConfigPath = filepath.Join(dir, "xray.json")
	writeLiveConfig(t, app)
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"
	app.Hot = &fakeHot{failAdd: true, live: []agent.LiveUser{
		{Email: "owner", ID: selfUUID},
		{Email: "probe@termit", ID: probeUUID},
	}}
	app.Canary = &fakeCanary{}

	if _, err := app.Apply(context.Background(), answer(), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	restarted := false
	for _, call := range runner.calls {
		if strings.Contains(call, "systemctl restart termit-xray.service") {
			restarted = true
		}
	}
	if !restarted {
		t.Error("the fallback did not restart the core")
	}
}

func TestApplyRollsBackWhenTheCanaryFailsAfterAHotUpdate(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.ConfigPath = filepath.Join(dir, "xray.json")
	previous := writeLiveConfig(t, app)
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"
	app.Hot = &fakeHot{live: []agent.LiveUser{
		{Email: "owner", ID: selfUUID},
		{Email: "probe@termit", ID: probeUUID},
	}}
	failing := errors.New("no traffic through the node")
	app.Canary = &fakeCanary{results: []error{failing, failing}}

	_, err := app.Apply(context.Background(), answer(), nil)
	if err == nil {
		t.Fatal("Apply succeeded although the canary failed twice")
	}
	if !strings.Contains(err.Error(), "previous configuration was restored") {
		t.Errorf("error %q does not say the rollback happened", err)
	}
	data, readErr := os.ReadFile(app.ConfigPath)
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if string(data) != string(previous) {
		t.Error("the previous configuration was not restored")
	}
}

func TestApplyRollsBackWhenTheCanaryFailsAfterARestart(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.ConfigPath = filepath.Join(dir, "xray.json")
	previous := writeLiveConfig(t, app)
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"
	app.Canary = &fakeCanary{results: []error{errors.New("no traffic")}}

	if _, err := app.Apply(context.Background(), answer(), nil); err == nil {
		t.Fatal("Apply succeeded although the canary failed")
	} else if !strings.Contains(err.Error(), "previous configuration was restored") {
		t.Errorf("error %q does not say the rollback happened", err)
	}
	data, err := os.ReadFile(app.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(data) != string(previous) {
		t.Error("the previous configuration was not restored")
	}
}

func TestApplyRestartsWhenSomethingOtherThanTheClientsChanged(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.ConfigPath = filepath.Join(dir, "xray.json")
	// The old file was generated for another path. The API cannot move the inbound, so the change has
	// to go through a validated restart even though the client diff is empty.
	spec := xray.DefaultSpec()
	spec.Path = "/old/"
	spec.Clients = []xray.Client{{ID: app.SelfUUID, Email: "owner"}}
	old, err := xray.Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.WriteFile(app.ConfigPath, old, 0o640); err != nil {
		t.Fatalf("write config: %v", err)
	}
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"
	app.Hot = &fakeHot{live: []agent.LiveUser{
		{Email: "owner", ID: selfUUID},
		{Email: "probe@termit", ID: probeUUID},
		{Email: "peer-uv_two@termit", ID: peerUUID},
	}}
	app.Canary = &fakeCanary{}

	result, err := app.Apply(context.Background(), answer(), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Hot {
		t.Error("the API path was used for a change it cannot make")
	}
	restarted := false
	for _, call := range runner.calls {
		if strings.Contains(call, "systemctl restart termit-xray.service") {
			restarted = true
		}
	}
	if !restarted {
		t.Error("the path change did not restart the core")
	}
}

func TestApplyReconcilesLiveDriftWhenTheFileIsAlreadyRight(t *testing.T) {
	t.Parallel()
	app, dir := applier(t)
	app.ConfigPath = filepath.Join(dir, "xray.json")
	runner := &fakeRunner{}
	app.Runner = runner
	app.Unit = "termit-xray.service"
	if _, err := app.Apply(context.Background(), answer(), nil); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	runner.calls = nil

	// The file is correct; the running core still accepts a user nobody asked for. Only the API can
	// see that, and only the API should fix it.
	hot := &fakeHot{live: []agent.LiveUser{
		{Email: "owner", ID: selfUUID},
		{Email: "probe@termit", ID: probeUUID},
		{Email: "peer-uv_two@termit", ID: peerUUID},
		{Email: "stray@termit", ID: "00000000-0000-4000-8000-000000000001"},
	}}
	app.Hot = hot
	canary := &fakeCanary{}
	app.Canary = canary

	result, err := app.Apply(context.Background(), answer(), nil)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if !result.Unchanged {
		t.Error("the file changed although it was already correct")
	}
	if !result.Hot {
		t.Errorf("the live drift was not fixed through the API: %s", result.HotNote)
	}
	if len(hot.removes) != 1 || hot.removes[0] != "stray@termit" {
		t.Errorf("removed = %v, want the stray user", hot.removes)
	}
	if canary.calls != 1 {
		t.Errorf("canary ran %d time(s), want once", canary.calls)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "systemctl restart") {
			t.Errorf("the core was restarted for a live-only drift: %s", call)
		}
	}
}

func TestCompareNamesClientsByEmail(t *testing.T) {
	t.Parallel()
	oldSpec := xray.DefaultSpec()
	oldSpec.Path = "/secret/"
	oldSpec.Clients = []xray.Client{{ID: selfUUID, Email: "owner"}, {ID: peerUUID, Email: "peer-uv_two@termit"}}
	previous, err := xray.Generate(oldSpec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	newSpec := xray.DefaultSpec()
	newSpec.Path = "/secret/"
	newSpec.Clients = []xray.Client{{ID: selfUUID, Email: "owner"}}
	next, err := xray.Generate(newSpec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	result := agent.Compare(previous, next, map[string]string{selfUUID: "owner"})
	if len(result.Removed) != 1 || result.Removed[0] != "peer-uv_two@termit" {
		t.Errorf("removed = %v, want the peer's email, never its UUID", result.Removed)
	}
	if len(result.Added) != 0 {
		t.Errorf("added = %v", result.Added)
	}
}

func TestDiffUsers(t *testing.T) {
	t.Parallel()
	desired := map[string]string{
		selfUUID:  "owner",
		probeUUID: "probe@termit",
		peerUUID:  "peer-uv_two@termit",
	}
	live := []agent.LiveUser{
		{Email: "owner", ID: strings.ToUpper(selfUUID)},
		{Email: "peer-uv_old@termit", ID: peerUUID},
		{Email: "probe@termit", ID: "00000000-0000-4000-8000-000000000000"},
	}
	add, remove := agent.DiffUsersForTest(desired, live)
	if len(add) != 2 {
		t.Errorf("add = %+v, want the new peer and the changed probe", add)
	}
	for _, client := range add {
		if client.Email == "owner" {
			t.Error("a user whose email and UUID match was re-added")
		}
	}
	if len(remove) != 2 {
		t.Errorf("remove = %v, want the old peer and the changed probe", remove)
	}
	for _, email := range remove {
		if email == "owner" {
			t.Error("the matching owner was removed")
		}
	}
}

func TestXrayAPIReadsUsers(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{output: []byte(`{"users":[
		{"email":"owner","account":{"id":"` + selfUUID + `"}},
		{"email":"probe@termit","account":{"id":"` + probeUUID + `"}}
	]}`)}
	api := agent.XrayAPI{Binary: "/usr/bin/xray", Tag: "proxy", Runner: runner}
	users, err := api.Users(context.Background())
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if len(users) != 2 || users[0].Email != "owner" || users[0].ID != selfUUID {
		t.Errorf("users = %+v", users)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "inbounduser") ||
		!strings.Contains(runner.calls[0], "-tag=proxy") {
		t.Errorf("calls = %v", runner.calls)
	}
}

func TestXrayAPIAddSendsTheFullInbound(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	api := agent.XrayAPI{
		Binary: "/usr/bin/xray", Path: "/secret/", Tag: "proxy",
		WorkDir: t.TempDir(), Runner: runner,
	}
	if err := api.Add(context.Background(), []xray.Client{{ID: peerUUID, Email: "peer@termit"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "api adu") {
		t.Fatalf("calls = %v", runner.calls)
	}
	var payload struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Listen   string `json:"listen"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			Settings struct {
				Clients []xray.Client `json:"clients"`
			} `json:"settings"`
			Stream struct {
				Network string `json:"network"`
				XHTTP   struct {
					Path string `json:"path"`
				} `json:"xhttpSettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(runner.written, &payload); err != nil {
		t.Fatalf("decode the staged request %q: %v", runner.written, err)
	}
	if len(payload.Inbounds) != 1 {
		t.Fatalf("inbounds = %d", len(payload.Inbounds))
	}
	inbound := payload.Inbounds[0]
	if inbound.Tag != "proxy" || inbound.Listen == "" || inbound.Port == 0 || inbound.Protocol != "vless" {
		t.Errorf("inbound = %+v", inbound)
	}
	if len(inbound.Settings.Clients) != 1 || inbound.Settings.Clients[0].ID != peerUUID {
		t.Errorf("clients = %+v", inbound.Settings.Clients)
	}
	if inbound.Stream.Network != "xhttp" || inbound.Stream.XHTTP.Path != "/secret/" {
		t.Errorf("stream = %+v", inbound.Stream)
	}
}

func TestXrayAPIRemovesByEmail(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	api := agent.XrayAPI{Binary: "/usr/bin/xray", Tag: "proxy", Runner: runner}
	if err := api.Remove(context.Background(), []string{"a@termit", "b@termit"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "api rmu") ||
		!strings.Contains(runner.calls[0], "a@termit") || !strings.Contains(runner.calls[0], "b@termit") {
		t.Errorf("calls = %v", runner.calls)
	}
}

// scriptedRunner answers every command with the same output and keeps the contents of a staged file
// the command was given, which is how the add-user request is observed.
type scriptedRunner struct {
	output  []byte
	written []byte
	calls   []string
}

func (r *scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	for _, arg := range args {
		if data, err := os.ReadFile(arg); err == nil { // #nosec G304 -- the test's own staged file.
			r.written = data
		}
	}
	return r.output, nil
}
