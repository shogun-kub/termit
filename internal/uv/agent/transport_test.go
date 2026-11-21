package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/hysteria"
)

func transportSpec() hysteria.Spec {
	const uuid = "11111111-2222-4333-8444-555555555555"
	return hysteria.Spec{
		CertFile:      "/var/lib/termit/uv/certs/fullchain.pem",
		KeyFile:       "/var/lib/termit/uv/certs/privkey.pem",
		Users:         map[string]string{proto.HysteriaUser(uuid): proto.HysteriaPassword(uuid)},
		HopAddress:    "127.0.0.1:10801",
		MasqueradeURL: "https://127.0.0.1:443",
	}
}

func ranCalls(runner *fakeRunner, want string) bool {
	return strings.Contains(strings.Join(runner.calls, "\n"), want)
}

// TestTransportRestartsOnlyWhenTheSetChanges: the transport has no API, so a restart is the price of a
// peer change - and it must not be paid when nothing changed, or every sync would drop every session.
func TestTransportRestartsOnlyWhenTheSetChanges(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	transport := agent.Transport{
		ConfigPath: filepath.Join(t.TempDir(), "hysteria.yaml"),
		Unit:       "termit-hysteria.service",
		Runner:     runner,
	}
	ctx := context.Background()

	changed, err := transport.Apply(ctx, transportSpec())
	if err != nil || !changed {
		t.Fatalf("first Apply: changed=%v err=%v", changed, err)
	}
	info, err := os.Stat(transport.ConfigPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Windows emulates permission bits, so the mode is only meaningful where they are real.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Errorf("the config has mode %04o, want 0640: it holds the passwords", info.Mode().Perm())
	}
	if !ranCalls(runner, "systemctl restart termit-hysteria.service") {
		t.Errorf("the first write did not restart the unit: %v", runner.calls)
	}

	runner.calls = nil
	if changed, err = transport.Apply(ctx, transportSpec()); err != nil || changed {
		t.Fatalf("the same set reported a change: changed=%v err=%v", changed, err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("the same set ran %v", runner.calls)
	}

	spec := transportSpec()
	spec.Users["u99999999"] = proto.HysteriaPassword("99999999-2222-4333-8444-555555555555")
	if changed, err = transport.Apply(ctx, spec); err != nil || !changed {
		t.Fatalf("a changed set: changed=%v err=%v", changed, err)
	}
}

// TestTransportRollsBackWhenTheUnitRefuses: a file the binary will not read must not survive, or the node
// is left with a transport that restarts forever and carries nothing.
func TestTransportRollsBackWhenTheUnitRefuses(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{}
	transport := agent.Transport{
		ConfigPath: filepath.Join(t.TempDir(), "hysteria.yaml"),
		Unit:       "termit-hysteria.service",
		Owner:      "root:termit-hy",
		Runner:     runner,
	}
	ctx := context.Background()
	if _, err := transport.Apply(ctx, transportSpec()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	before, err := os.ReadFile(transport.ConfigPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !ranCalls(runner, "chown root:termit-hy "+transport.ConfigPath) {
		t.Errorf("the file was not given to the transport's user: %v", runner.calls)
	}

	runner.failRestart = true
	spec := transportSpec()
	spec.Users["u99999999"] = proto.HysteriaPassword("99999999-2222-4333-8444-555555555555")
	if _, err := transport.Apply(ctx, spec); err == nil {
		t.Fatal("a refused restart was reported as success")
	}
	after, err := os.ReadFile(transport.ConfigPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != string(before) {
		t.Error("the rejected config was not rolled back")
	}
}
