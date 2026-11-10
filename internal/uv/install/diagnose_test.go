package install

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// checkStatuses turns a check list into a name → status map, which is what these tests assert on.
func checkStatuses(checks []Check) map[string]CheckStatus {
	out := make(map[string]CheckStatus, len(checks))
	for _, check := range checks {
		out[check.Name] = check.Status
	}
	return out
}

// fixedNow is the clock the whole install suite runs on.
func fixedNow() time.Time { return engineNow }

// installForDiagnose installs a node without the two steps that need a real host or a CA.
func installForDiagnose(t *testing.T) (*config.Config, *sandbox) {
	t.Helper()
	cfg := testConfig(t)
	sb := newSandbox()
	inst := newTestInstaller(t, cfg, sb)
	if _, err := inst.Run(context.Background(), RunOptions{
		Command: "init",
		Skip:    []string{"preflight", "certificate"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	return cfg, sb
}

func TestDiagnoseSkipsWithoutANode(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	checks := Diagnose(context.Background(), DiagnoseOptions{Config: cfg, Exec: newSandbox().exec, Now: fixedNow})
	if len(checks) != 1 || checks[0].Name != "state" || checks[0].Status != StatusSkip {
		t.Fatalf("diagnose without a node = %+v, want a single skipped state check", checks)
	}
	if checks[0].Hint == "" {
		t.Error("the skip does not tell the operator what to do")
	}
}

func TestDiagnoseOnAnInstalledNode(t *testing.T) {
	t.Parallel()
	cfg, sb := installForDiagnose(t)

	checks := Diagnose(context.Background(), DiagnoseOptions{Config: cfg, Exec: sb.exec, Now: fixedNow,
		WarpHost: &warp.Host{GOOS: "windows", GOARCH: "amd64"}})
	got := checkStatuses(checks)
	want := map[string]CheckStatus{
		"state":        StatusOK,
		"services":     StatusOK,
		"certificate":  StatusFail, // the step was skipped, so there is no certificate
		"decoy":        StatusOK,
		"proxy config": StatusOK,
		// WARP is on by default and this host cannot run it: a skip with a reason, not a failure.
		"warp": StatusSkip,
	}
	for name, status := range want {
		if got[name] != status {
			t.Errorf("%s = %q, want %q (checks: %+v)", name, got[name], status, checks)
		}
	}
	if len(got) != len(want) {
		t.Errorf("diagnose returned %d checks, want %d: %+v", len(got), len(want), checks)
	}
}

func TestDiagnoseCatchesABrokenNode(t *testing.T) {
	t.Parallel()
	cfg, sb := installForDiagnose(t)

	// The site is changed behind the tool's back: the digest in the state no longer matches what the
	// seed produces, which is exactly the drift doctor is for.
	if err := os.WriteFile(filepath.Join(cfg.Paths.SiteDir, "index.html"), []byte("<html>tampered</html>"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	checks := Diagnose(context.Background(), DiagnoseOptions{Config: cfg, Exec: sb.exec, Now: fixedNow,
		WarpHost: &warp.Host{GOOS: "windows", GOARCH: "amd64"}})
	if got := checkStatuses(checks)["decoy"]; got != StatusFail {
		t.Errorf("decoy = %q, want %q", got, StatusFail)
	}

	// A stopped unit is a failure, not a skip.
	sb.active = map[string]bool{}
	checks = Diagnose(context.Background(), DiagnoseOptions{Config: cfg, Exec: sb.exec, Now: fixedNow})
	if got := checkStatuses(checks)["services"]; got != StatusFail {
		t.Errorf("services = %q, want %q", got, StatusFail)
	}

	// A node whose state is gone is a skip with a hint, not five failures.
	if err := os.Remove(cfg.Paths.StatePath()); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	checks = Diagnose(context.Background(), DiagnoseOptions{Config: cfg, Exec: sb.exec, Now: fixedNow})
	if len(checks) != 1 || checks[0].Status != StatusSkip {
		t.Errorf("diagnose without state = %+v, want one skip", checks)
	}
}

func TestDiagnoseCertificateWindow(t *testing.T) {
	t.Parallel()
	cfg, sb := installForDiagnose(t)
	certPath := filepath.Join(cfg.Paths.CertDir, certFile)
	keyPath := filepath.Join(cfg.Paths.CertDir, keyFile)

	certificate := func() CheckStatus {
		return checkStatuses(Diagnose(context.Background(), DiagnoseOptions{
			Config: cfg, Exec: sb.exec, Now: fixedNow,
		}))["certificate"]
	}

	writeSelfSigned(t, certPath, keyPath, engineNow.AddDate(0, 0, 80))
	if got := certificate(); got != StatusOK {
		t.Errorf("fresh certificate = %q, want ok", got)
	}
	writeSelfSigned(t, certPath, keyPath, engineNow.AddDate(0, 0, 10))
	if got := certificate(); got != StatusWarn {
		t.Errorf("certificate expiring soon = %q, want warn", got)
	}
	writeSelfSigned(t, certPath, keyPath, engineNow.AddDate(0, 0, -1))
	if got := certificate(); got != StatusFail {
		t.Errorf("expired certificate = %q, want fail", got)
	}
}
