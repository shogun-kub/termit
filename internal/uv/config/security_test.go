package config_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// TestSecurityDefaultsToOff: the whole block is inert until an operator asks for it, and the defaults
// that are visible (default_deny, the scan detector) are the conservative ones.
func TestSecurityDefaultsToOff(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Security.Firewall.Enabled {
		t.Error("a fresh configuration asks for the firewall")
	}
	if cfg.Security.Guard.Enabled {
		t.Error("a fresh configuration asks for the guard")
	}
	if !cfg.Security.Firewall.DenyOthers() {
		t.Error("default_deny should default to on")
	}
	if len(cfg.Security.Firewall.SSHFrom) != 0 {
		t.Errorf("ssh_from defaults to %v, want empty", cfg.Security.Firewall.SSHFrom)
	}
	scan := cfg.Security.Guard.Scan
	if scan.Attempts != config.DefaultScanAttempts {
		t.Errorf("scan.attempts = %d, want %d", scan.Attempts, config.DefaultScanAttempts)
	}
	if scan.Window.Std() != config.DefaultScanWindow {
		t.Errorf("scan.window = %s, want %s", scan.Window, config.DefaultScanWindow)
	}
	if scan.Remember.Std() != config.DefaultScanRemember {
		t.Errorf("scan.remember = %s, want %s", scan.Remember, config.DefaultScanRemember)
	}
}

// TestSecurityReadsWhatTheOperatorWrote: every key in the block is read, including the explicit
// `default_deny: false`, which a plain bool could not tell from an absent key.
func TestSecurityReadsWhatTheOperatorWrote(t *testing.T) {
	t.Parallel()
	document := minimal + `security:
  firewall:
    enabled: true
    default_deny: false
    ssh_from: [192.0.2.0/24, 2001:db8::1]
  guard:
    enabled: true
    scan:
      attempts: 3
      window: 1h
      remember: 30m
`
	cfg, err := config.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Security.Firewall.Enabled || !cfg.Security.Guard.Enabled {
		t.Error("enabled was not read")
	}
	if cfg.Security.Firewall.DenyOthers() {
		t.Error("default_deny: false was ignored")
	}
	if len(cfg.Security.Firewall.SSHFrom) != 2 {
		t.Errorf("ssh_from = %v", cfg.Security.Firewall.SSHFrom)
	}
	scan := cfg.Security.Guard.Scan
	if scan.Attempts != 3 || scan.Window.Std() != time.Hour || scan.Remember.Std() != 30*time.Minute {
		t.Errorf("scan = %+v", scan)
	}
}

// TestSecurityRejectsWhatNftablesCannotDo: the kernel rate is N per one unit, so a window it cannot
// express has to be refused where it is read rather than silently changed.
func TestSecurityRejectsWhatNftablesCannotDo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{
			"ten-minute window",
			minimal + "security:\n  guard:\n    enabled: true\n    scan:\n      attempts: 5\n      window: 10m\n",
			"exactly 1s, 1m, 1h, 1d or 1w",
		},
		{
			"threshold of one",
			minimal + "security:\n  guard:\n    enabled: true\n    scan:\n      attempts: 1\n",
			"at least 2 attempts",
		},
		{
			"bad network",
			minimal + "security:\n  firewall:\n    ssh_from: [\"192.0.2.0/33\"]\n",
			"ssh_from",
		},
		{
			"not a duration",
			minimal + "security:\n  guard:\n    enabled: true\n    scan:\n      window: soon\n",
			"not a duration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Parse(strings.NewReader(tc.document))
			if err == nil {
				t.Fatalf("Parse accepted a configuration it should refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestSecurityTableAssemblesTheNodeTable is the join between the configuration and the renderer: the
// node's ports, its network restriction and the detector the guard reads.
func TestSecurityTableAssemblesTheNodeTable(t *testing.T) {
	t.Parallel()
	document := minimal + `security:
  firewall:
    enabled: true
    ssh_from: [192.0.2.0/24]
  guard:
    enabled: true
    scan:
      attempts: 7
      window: 1m
      remember: 20m
`
	cfg, err := config.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	table, err := cfg.Security.Table(firewall.NodePorts)
	if err != nil {
		t.Fatalf("Table: %v", err)
	}
	if table.Name != firewall.NodeTable {
		t.Errorf("name = %q", table.Name)
	}
	if len(table.Ports) != 3 || table.Ports[0] != 22 {
		t.Errorf("ports = %v", table.Ports)
	}
	if !table.DefaultDeny {
		t.Error("default_deny did not reach the table")
	}
	if len(table.SSHFrom) != 1 || table.SSHFrom[0].String() != "192.0.2.0/24" {
		t.Errorf("ssh_from = %v", table.SSHFrom)
	}
	if !table.Scan.Enabled || table.Scan.Attempts != 7 || table.Scan.Remember != 20*time.Minute {
		t.Errorf("scan = %+v", table.Scan)
	}
	if _, err := table.Render(); err != nil {
		t.Errorf("the assembled table does not render: %v", err)
	}
}

// TestSecurityRoundTrip: the block is written by `init` and read back by every command, so it has to
// survive the encoder — including a duration, which yaml would otherwise write as nanoseconds.
func TestSecurityRoundTrip(t *testing.T) {
	t.Parallel()
	document := minimal + `security:
  firewall:
    enabled: true
    default_deny: false
    ssh_from: [192.0.2.0/24]
  guard:
    enabled: true
    scan:
      attempts: 4
      window: 1h
      remember: 45m
`
	cfg, err := config.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if back.Security.Firewall.DenyOthers() || !back.Security.Guard.Enabled {
		t.Errorf("security changed in the round trip: %+v", back.Security)
	}
	if back.Security.Guard.Scan.Window.Std() != time.Hour {
		t.Errorf("window came back as %s", back.Security.Guard.Scan.Window)
	}
	if back.Security.Guard.Scan.Remember.Std() != 45*time.Minute {
		t.Errorf("remember came back as %s", back.Security.Guard.Scan.Remember)
	}
}

// TestSecurityLadder pins the two ladders the guard applies and the constraints the loader enforces.
func TestSecurityLadder(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ladder := cfg.Security.Ladder()
	if ladder.Window != 24*time.Hour {
		t.Errorf("window = %s, want 24h", ladder.Window)
	}
	if ladder.Scan.First != time.Hour || ladder.Scan.Repeat != 24*time.Hour {
		t.Errorf("scan ladder = %+v", ladder.Scan)
	}
	if ladder.Path404.First != 5*time.Minute || ladder.Path404.Repeat != time.Hour || !ladder.Path404.Double {
		t.Errorf("path404 ladder = %+v", ladder.Path404)
	}
	if err := ladder.Validate(); err != nil {
		t.Errorf("the default ladder is refused: %v", err)
	}

	// The keys are read, not just defaulted.
	document := minimal + `security:
  guard:
    enabled: true
    ladder_window: 12h
    scan:
      ban: 30m
      repeat: 12h
    path404:
      attempts: 3
      window: 2m
      ban: 2m
      max: 20m
`
	edited, err := config.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ladder = edited.Security.Ladder()
	if ladder.Window != 12*time.Hour || ladder.Scan.First != 30*time.Minute ||
		ladder.Path404.Repeat != 20*time.Minute {
		t.Errorf("ladder = %+v", ladder)
	}
}

// TestSecurityLadderRejectsAnImpossibleOrder: a repeat shorter than the first offence, or a detector
// window longer than the ladder's memory, would make the ladder mean nothing.
func TestSecurityLadderRejectsAnImpossibleOrder(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"repeat shorter": minimal + "security:\n  guard:\n    scan:\n      ban: 1h\n      repeat: 5m\n",
		"no window":      minimal + "security:\n  guard:\n    path404:\n      window: 30h\n",
		"zero attempts":  minimal + "security:\n  guard:\n    path404:\n      attempts: 1\n",
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := config.Parse(strings.NewReader(document)); err == nil {
				t.Error("the configuration was accepted")
			}
		})
	}
}
