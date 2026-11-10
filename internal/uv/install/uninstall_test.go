package install

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/service"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
}

func TestUninstallWithoutPurgeKeepsIdentity(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	ctx := context.Background()

	installer := newTestInstaller(t, cfg, sb)
	if _, err := installer.Run(ctx, RunOptions{Skip: []string{"preflight", "certificate"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	report, err := Uninstall(ctx, UninstallOptions{
		Config: cfg,
		Exec:   sb.exec,
		Logger: discardLogger(),
		Now:    func() time.Time { return engineNow },
	})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(report.Removed) == 0 {
		t.Fatal("nothing was removed")
	}
	if _, err := os.Stat(cfg.Paths.XrayBinary()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("core binary survived uninstall: %v", err)
	}
	if _, err := os.Stat(cfg.Paths.StatePath()); err != nil {
		t.Errorf("state was removed without --purge: %v", err)
	}
	if !sb.user {
		t.Error("the service user was removed without --purge")
	}
}

func TestUninstallPurgeRemovesEverything(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	ctx := context.Background()

	installer := newTestInstaller(t, cfg, sb)
	if _, err := installer.Run(ctx, RunOptions{Skip: []string{"preflight", "certificate"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := Uninstall(ctx, UninstallOptions{
		Config: cfg,
		Exec:   sb.exec,
		Logger: discardLogger(),
		Now:    func() time.Time { return engineNow },
		Purge:  true,
	}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	for _, dir := range []string{cfg.Paths.StateDir, cfg.Paths.ConfigDir} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived --purge: %v", dir, err)
		}
	}
	if sb.user {
		t.Error("the service user survived --purge")
	}
}

func TestUninstallDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	ctx := context.Background()

	installer := newTestInstaller(t, cfg, sb)
	if _, err := installer.Run(ctx, RunOptions{Skip: []string{"preflight", "certificate"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	report, err := Uninstall(ctx, UninstallOptions{
		Config: cfg,
		Exec:   sb.exec,
		Logger: discardLogger(),
		Now:    func() time.Time { return engineNow },
		Purge:  true,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(report.Removed) == 0 {
		t.Error("dry run reported nothing to remove")
	}
	if _, err := os.Stat(cfg.Paths.StatePath()); err != nil {
		t.Errorf("dry run removed the state: %v", err)
	}
	if _, err := os.Stat(cfg.Paths.XrayBinary()); err != nil {
		t.Errorf("dry run removed the core: %v", err)
	}
}

func TestUninstallStopsEveryUnit(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	ctx := context.Background()

	installer := newTestInstaller(t, cfg, sb)
	if _, err := installer.Run(ctx, RunOptions{Skip: []string{"preflight", "certificate"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := Uninstall(ctx, UninstallOptions{
		Config: cfg,
		Exec:   sb.exec,
		Logger: discardLogger(),
		Now:    func() time.Time { return engineNow },
	}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	// A unit left running outlives the binary it executes: the agent would keep syncing and
	// rewriting a core whose config this command just deleted.
	var disabled []string
	for _, call := range sb.exec.calls {
		if call.Name != "systemctl" || len(call.Args) == 0 || call.Args[0] != "disable" {
			continue
		}
		disabled = call.Args
	}
	if disabled == nil {
		t.Fatal("uninstall never disabled a unit")
	}
	for _, unit := range []string{service.XrayUnitName, service.AgentUnitName, service.RenewTimerName} {
		found := false
		for _, arg := range disabled {
			if arg == unit {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s was not stopped: systemctl %v", unit, disabled)
		}
	}
}

func TestUninstallWithoutState(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	report, err := Uninstall(context.Background(), UninstallOptions{
		Config: cfg,
		Exec:   newSandbox().exec,
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(report.Removed) != 0 {
		t.Errorf("removed %v without an installed node", report.Removed)
	}
	if len(report.Warnings) == 0 {
		t.Error("a missing state file was not reported")
	}
}

func TestSafeToRemove(t *testing.T) {
	t.Parallel()
	refused := []string{"/", "/etc", "/var", "/usr", "/tmp", "/etc/termit", "/var/lib"}
	for _, target := range refused {
		if safeToRemove(target) {
			t.Errorf("safeToRemove(%q) = true, want false", target)
		}
	}
	allowed := []string{"/etc/termit/uv", "/var/lib/termit/uv", filepath.Join(t.TempDir(), "x", "y")}
	for _, target := range allowed {
		if !safeToRemove(target) {
			t.Errorf("safeToRemove(%q) = false, want true", target)
		}
	}
}
