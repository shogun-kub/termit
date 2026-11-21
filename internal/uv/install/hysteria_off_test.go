package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// TestInitRetiresTheTransportWhenTheBlockIsGone: the block in config.yaml is the switch, and `init`
// converges on what the file says. A node that removes the block must end up without the unit, the
// configuration and the binary — otherwise a port stays open and a process keeps running that nothing in
// the configuration explains, which is the one thing an operator cannot debug from the file.
func TestInitRetiresTheTransportWhenTheBlockIsGone(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	ctx := context.Background()

	// A node that had the transport enabled once: its leftovers are on disk and its unit is known.
	leftovers := []string{
		filepath.Join(cfg.Paths.UnitDir, service.HysteriaUnitName),
		cfg.Paths.HysteriaConfig,
		cfg.Paths.HysteriaBinary(),
	}
	for _, path := range leftovers {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("left over"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	installer := newTestInstaller(t, cfg, sb)
	report, err := installer.Run(ctx, RunOptions{Command: "init", Skip: []string{"preflight", "certificate", "warp"}})
	if err != nil {
		t.Fatalf("Run: %v\n%+v", err, report)
	}
	for _, path := range leftovers {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived an init that no longer enables the transport", path)
		}
	}
	retired := false
	for _, step := range report.Steps {
		if step.Name != "hysteria-off" {
			continue
		}
		retired = true
		if step.Status != state.StepOK {
			t.Errorf("the retire step reports %q, want it to have done the work", step.Status)
		}
	}
	if !retired {
		t.Error("the journal has no hysteria-off step")
	}

	// The second run has nothing to retire and says so, which is what keeps `init` idempotent.
	second := newTestInstaller(t, cfg, sb)
	secondReport, err := second.Run(ctx, RunOptions{Command: "init", Skip: []string{"preflight", "certificate", "warp"}})
	if err != nil {
		t.Fatalf("second Run: %v\n%+v", err, secondReport)
	}
	for _, step := range secondReport.Steps {
		if step.Name == "hysteria-off" && step.Status != state.StepPresent {
			t.Errorf("the second retire step reports %q, want present", step.Status)
		}
	}
}
