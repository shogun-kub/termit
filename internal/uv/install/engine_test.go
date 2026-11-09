package install

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

var engineNow = time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)

// sandbox is a fake host: it tracks the user and the packages the steps created.
// skippedSteps counts the steps the suite never runs: preflight reports every host check and the
// certificate step talks to a CA, which no unit test may do.
const skippedSteps = 2

type sandbox struct {
	exec     *fakeExecutor
	user     bool
	packages bool
	aptFails bool
	active   map[string]bool
}

func newSandbox() *sandbox {
	s := &sandbox{active: map[string]bool{}}
	s.exec = &fakeExecutor{handler: func(cmd Cmd) (Result, error) {
		switch cmd.Name {
		case "id":
			if s.user {
				return okResult("999\n")
			}
			return failedResult(cmd, 1, "")
		case "useradd":
			s.user = true
			return okResult("")
		case "dpkg-query":
			if s.packages {
				return okResult("install ok installed\n")
			}
			return failedResult(cmd, 1, "")
		case "apt-get":
			if s.aptFails {
				return failedResult(cmd, 100, "E: Unable to fetch some archives")
			}
			s.packages = true
			return okResult("")
		case "timedatectl":
			return okResult("yes")
		case "ss":
			return okResult("")
		case "systemctl":
			return s.systemctl(cmd)
		case "nginx", "chown":
			return okResult("")
		case "userdel":
			s.user = false
			return okResult("")
		}
		if len(cmd.Args) > 0 && (cmd.Args[0] == "version" || cmd.Args[0] == "run" || cmd.Args[0] == "api") {
			if cmd.Args[0] == "version" {
				return okResult("Xray 26.11.9 (Xray, Penetrates Everything.) abcdef (go1.27.3 linux/amd64)\n")
			}
			return okResult("")
		}
		return failedResult(cmd, 127, "unexpected command "+cmd.String())
	}}
	return s
}

// systemctl answers like a host where nothing has been started yet, and remembers what starts.
func (s *sandbox) systemctl(cmd Cmd) (Result, error) {
	if len(cmd.Args) == 0 {
		return okResult("")
	}
	unit := cmd.Args[len(cmd.Args)-1]
	unit = strings.TrimPrefix(unit, "--quiet")
	switch cmd.Args[0] {
	case "is-active":
		for _, arg := range cmd.Args[1:] {
			if arg != "--quiet" {
				unit = arg
				break
			}
		}
		if s.active[unit] {
			return okResult("active\n")
		}
		return failedResult(cmd, 3, "inactive\n")
	case "enable", "start", "restart", "reload-or-restart":
		for _, arg := range cmd.Args[1:] {
			if arg != "--now" && arg != "--quiet" {
				s.active[arg] = true
			}
		}
		return okResult("")
	case "daemon-reload", "reload":
		return okResult("")
	}
	return okResult("")
}

// testConfig is a configuration whose paths all live under one temporary tree.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	cfg.Paths.ConfigDir = filepath.Join(dir, "etc")
	cfg.Paths.StateDir = filepath.Join(dir, "var")
	cfg.Paths.CertDir = filepath.Join(cfg.Paths.StateDir, "certs")
	cfg.Paths.ACMEWebroot = filepath.Join(cfg.Paths.StateDir, "acme")
	cfg.Paths.SiteDir = filepath.Join(cfg.Paths.StateDir, "site")
	cfg.Paths.BinDir = filepath.Join(dir, "sbin")
	cfg.Paths.XrayConfig = filepath.Join(cfg.Paths.ConfigDir, "xray.json")
	cfg.Paths.NginxSite = filepath.Join(dir, "nginx", "sites-available", "termit.conf")
	cfg.Paths.SitesEnabled = filepath.Join(dir, "nginx", "sites-enabled", "termit.conf")
	cfg.Paths.UnitDir = filepath.Join(dir, "systemd")
	cfg.Paths.NginxDropIn = filepath.Join(cfg.Paths.UnitDir, "nginx.service.d", "termit.conf")
	cfg.Paths.DecoySnippet = filepath.Join(cfg.Paths.StateDir, "nginx", "decoy.conf")
	cfg.Paths.AccessLog = filepath.Join(dir, "log", "access.log")
	cfg.Paths.ErrorLog = filepath.Join(dir, "log", "error.log")

	// A local release directory, so the test never touches the network.
	archive := buildZip(t, []zipEntry{{name: "xray", body: "#!/bin/sh\nexit 0\n", mode: 0o755}})
	releaseDir := filepath.Join(dir, "release", "v"+cfg.Xray.Version)
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "Xray-linux-64.zip"), archive, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg.Xray.DownloadBase = filepath.Join(dir, "release")
	cfg.Xray.SHA256 = map[string]string{"amd64": digestOf(archive)}
	return cfg
}

func newTestInstaller(t *testing.T, cfg *config.Config, sb *sandbox) *Installer {
	t.Helper()
	installer, err := New(Options{
		Config:  cfg,
		Exec:    sb.exec,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		Now:     func() time.Time { return engineNow },
		Version: "test",
		Arch:    "amd64",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return installer
}

func TestEngineInstallsAndReusesState(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	ctx := context.Background()

	first := newTestInstaller(t, cfg, sb)
	report, err := first.Run(ctx, RunOptions{Command: "init", Skip: []string{"preflight", "certificate"}})
	if err != nil {
		t.Fatalf("Run: %v\n%+v", err, report)
	}
	if counts := report.Counts(); counts[state.StepOK] != len(first.Steps())-skippedSteps {
		t.Fatalf("first run counts = %v, want every step after preflight to be ok: %+v", counts, report.Steps)
	}

	node := first.State()
	if !node.Installed() {
		t.Error("state has no identity after install")
	}
	if node.Xray.Version != cfg.Xray.Version || node.Xray.SHA256 == "" {
		t.Errorf("core not recorded: %+v", node.Xray)
	}
	if _, err := os.Stat(cfg.Paths.XrayBinary()); err != nil {
		t.Errorf("core binary missing: %v", err)
	}
	if !node.HasPath(serviceUser) {
		t.Error("manifest does not record the service user")
	}
	if !node.HasPath(cfg.Paths.XrayBinary()) {
		t.Error("manifest does not record the installed core")
	}
	if node.Decoy.Digest == "" {
		t.Error("state does not record the decoy digest")
	}
	index, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, "index.html"))
	if err != nil {
		t.Fatalf("read the generated site: %v", err)
	}
	if !strings.Contains(string(index), "<form") {
		t.Error("generated page has no sign-in form")
	}
	snippet, err := os.ReadFile(cfg.Paths.DecoySnippet)
	if err != nil {
		t.Fatalf("read the nginx fragment: %v", err)
	}
	if !strings.Contains(string(snippet), "location = /") {
		t.Errorf("nginx fragment has no location block:\n%s", snippet)
	}
	if !node.HasPath(cfg.Paths.DecoySnippet) {
		t.Error("manifest does not record the nginx fragment")
	}
	identity := node.Identity.SelfUUID
	if identity == "" || node.Decoy.Seed == "" {
		t.Fatalf("identity incomplete: %+v", node)
	}

	statePath := cfg.Paths.StatePath()
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	second := newTestInstaller(t, cfg, sb)
	secondReport, err := second.Run(ctx, RunOptions{Command: "init", Skip: []string{"preflight", "certificate"}})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if counts := secondReport.Counts(); counts[state.StepPresent] != len(second.Steps())-skippedSteps {
		t.Fatalf("second run counts = %v, want every step after preflight to be present: %+v", counts, secondReport.Steps)
	}
	if second.State().Identity.SelfUUID != identity {
		t.Error("a re-run regenerated the identity")
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a no-op re-run rewrote the state file")
	}
}

func TestEngineResumesAfterFailure(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	sb.aptFails = true
	ctx := context.Background()

	failing := newTestInstaller(t, cfg, sb)
	report, err := failing.Run(ctx, RunOptions{Command: "init", Skip: []string{"preflight", "certificate"}})
	if err == nil {
		t.Fatal("Run succeeded even though apt failed")
	}
	if !strings.Contains(err.Error(), "packages") {
		t.Errorf("error %q does not name the failing step", err)
	}
	failed := report.Counts()[state.StepFailed]
	if failed != 1 {
		t.Errorf("failed steps = %d, want 1: %+v", failed, report.Steps)
	}
	if failing.State().Installed() {
		t.Error("a failed install should not have generated an identity")
	}
	if !sb.user {
		t.Error("steps before the failure should have run")
	}

	sb.aptFails = false
	resumed := newTestInstaller(t, cfg, sb)
	resumeReport, err := resumed.Run(ctx, RunOptions{Command: "init", Skip: []string{"preflight", "certificate"}})
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	counts := resumeReport.Counts()
	if counts[state.StepPresent] != 2 || counts[state.StepOK] != len(resumed.Steps())-skippedSteps-2 {
		t.Errorf("resume counts = %v, want the two steps before the failure present and the rest ok: %+v", counts, resumeReport.Steps)
	}
	if !resumed.State().Installed() {
		t.Error("resumed install has no identity")
	}
}

func TestEngineDryRunTouchesNothing(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()

	installer := newTestInstaller(t, cfg, sb)
	report, err := installer.Run(context.Background(), RunOptions{DryRun: true, Skip: []string{"preflight", "certificate"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if counts := report.Counts(); counts[state.StepPlanned] != len(installer.Steps())-skippedSteps {
		t.Errorf("dry run counts = %v, want 6 planned: %+v", counts, report.Steps)
	}
	if _, err := os.Stat(cfg.Paths.StateDir); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", cfg.Paths.StateDir)
	}
	if _, err := os.Stat(cfg.Paths.JournalPath()); !os.IsNotExist(err) {
		t.Errorf("dry run wrote a journal at %s", cfg.Paths.JournalPath())
	}
	if sb.user {
		t.Error("dry run created a user")
	}
}

func TestEngineRejectsUnknownStep(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	installer := newTestInstaller(t, cfg, newSandbox())
	if _, err := installer.Run(context.Background(), RunOptions{Only: []string{"nope"}}); err == nil {
		t.Fatal("an unknown --only step was accepted")
	}
	if _, err := installer.Run(context.Background(), RunOptions{Skip: []string{"nope"}}); err == nil {
		t.Fatal("an unknown --skip step was accepted")
	}
}

func TestNewRejectsDomainChange(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	installer := newTestInstaller(t, cfg, sb)
	if _, err := installer.Run(context.Background(), RunOptions{Skip: []string{"preflight", "certificate"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	other := *cfg
	other.Domain = "other.example.com"
	_, err := New(Options{
		Config: &other,
		Exec:   sb.exec,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return engineNow },
		Arch:   "amd64",
	})
	if err == nil || !strings.Contains(err.Error(), "uv migrate") {
		t.Fatalf("New error = %v, want a migrate hint", err)
	}
}

func TestEngineJournalRecordsRun(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	installer := newTestInstaller(t, cfg, newSandbox())
	if _, err := installer.Run(context.Background(), RunOptions{Command: "init", Skip: []string{"preflight", "certificate"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	journal, err := state.LoadJournal(cfg.Paths.JournalPath())
	if err != nil {
		t.Fatalf("LoadJournal: %v", err)
	}
	last := journal.Last()
	if last == nil || last.Result != state.RunOK {
		t.Fatalf("journal last run = %+v", last)
	}
	if last.Command != "init" || last.Version != "test" {
		t.Errorf("journal run identity = %+v", last)
	}
	if want := len(installer.Steps()) - skippedSteps; len(last.Steps) != want {
		t.Errorf("journal recorded %d steps, want %d: %+v", len(last.Steps), want, last.Steps)
	}
}
