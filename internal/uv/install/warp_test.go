package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// supportedHost is a host WARP could run on: a Linux box with a tun device.
func supportedHost(t *testing.T) *warp.Host {
	t.Helper()
	tun := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(tun, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return &warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: tun}
}

func warpConfig(t *testing.T, mode string) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Warp.Mode = mode
	if mode == config.WarpSelected {
		cfg.Warp.Selected = []string{"example.com"}
	}
	return cfg
}

func runWarpStep(t *testing.T, cfg *config.Config, host *warp.Host, exec *fakeExecutor) *Report {
	t.Helper()
	installer, err := New(Options{Config: cfg, Exec: exec, WarpHost: host})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := installer.Run(context.Background(), RunOptions{Command: "init", Only: []string{"warp"}})
	if err != nil {
		t.Fatalf("Run: %v\n%+v", err, report)
	}
	return report
}

func TestWarpStepIsOffWhenTheOwnerTurnedItOff(t *testing.T) {
	t.Parallel()
	exec := &fakeExecutor{handler: func(Cmd) (Result, error) { return okResult("") }}
	report := runWarpStep(t, warpConfig(t, config.WarpOff), supportedHost(t), exec)
	if got := report.Steps[0].Status; got != state.StepPresent {
		t.Errorf("status = %q, want present", got)
	}
	if !strings.Contains(report.Steps[0].Detail, "off") {
		t.Errorf("detail = %q", report.Steps[0].Detail)
	}
	if len(exec.calls) != 0 {
		t.Errorf("the step ran commands with WARP off: %v", exec.calls)
	}
}

// The refusal is the point: a container without TUN gets a reason, not a failed install.
func TestWarpStepRefusesAnUnsupportedHost(t *testing.T) {
	t.Parallel()
	exec := &fakeExecutor{handler: func(Cmd) (Result, error) { return okResult("") }}
	host := &warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: filepath.Join(t.TempDir(), "absent")}
	report := runWarpStep(t, warpConfig(t, config.WarpAll), host, exec)
	if got := report.Steps[0].Status; got != state.StepPresent {
		t.Fatalf("status = %q, want present (the refusal is a converged state)", got)
	}
	if !strings.Contains(report.Steps[0].Detail, "is missing") {
		t.Errorf("detail = %q, want the refusal reason", report.Steps[0].Detail)
	}
	if len(exec.calls) != 0 {
		t.Errorf("the step touched the host although WARP cannot run: %v", exec.calls)
	}
}

func TestWarpStepConvergesTheSidecar(t *testing.T) {
	t.Parallel()
	installed := false
	exec := &fakeExecutor{handler: func(cmd Cmd) (Result, error) {
		switch {
		case cmd.Name == "sh":
			installed = true
			return okResult("")
		case cmd.Name == warp.DefaultBinary && strings.Join(cmd.Args, " ") == "--version":
			if !installed {
				return failedResult(cmd, 127, "not installed")
			}
			return okResult("2026.1.1")
		case cmd.Name == warp.DefaultBinary && strings.Join(cmd.Args, " ") == "--accept-tos status":
			return okResult("Status update: Connected")
		case cmd.Name == warp.DefaultBinary && strings.Join(cmd.Args, " ") == "--accept-tos registration show":
			return failedResult(cmd, 1, "Registration missing")
		case cmd.Name == warp.DefaultBinary:
			return okResult("")
		}
		return failedResult(cmd, 1, "unexpected command")
	}}
	report := runWarpStep(t, warpConfig(t, config.WarpAll), supportedHost(t), exec)
	if got := report.Steps[0].Status; got != state.StepOK {
		t.Fatalf("status = %q, want ok: %+v", got, report.Steps[0])
	}
	var commands []string
	for _, call := range exec.calls {
		commands = append(commands, call.String())
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"sh -c " + warp.InstallScript,
		warp.DefaultBinary + " --accept-tos registration new",
		warp.DefaultBinary + " --accept-tos mode proxy",
		warp.DefaultBinary + " --accept-tos connect",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the step never ran %q; it ran:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "registration new") != 1 {
		t.Errorf("registration ran %d times, want once:\n%s", strings.Count(joined, "registration new"), joined)
	}
}

func TestWarpStepReportsAConvergedSidecarAsPresent(t *testing.T) {
	t.Parallel()
	exec := &fakeExecutor{handler: func(cmd Cmd) (Result, error) {
		switch {
		case strings.Join(cmd.Args, " ") == "--version":
			return okResult("2026.1.1")
		case strings.Join(cmd.Args, " ") == "--accept-tos status":
			return okResult("Status update: Connected")
		}
		return failedResult(cmd, 1, "unexpected command")
	}}
	report := runWarpStep(t, warpConfig(t, config.WarpSelected), supportedHost(t), exec)
	if got := report.Steps[0].Status; got != state.StepPresent {
		t.Fatalf("status = %q, want present", got)
	}
	if !strings.Contains(report.Steps[0].Detail, "connected") {
		t.Errorf("detail = %q", report.Steps[0].Detail)
	}
}
