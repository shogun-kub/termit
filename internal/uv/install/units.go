package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// unitsStep installs the systemd units.
//
// They are written, not shipped, so the paths inside them follow the configuration; and they are
// compared before writing so `termit-uv init` does not reload systemd on every run.
type unitsStep struct{ e *stepEnv }

func (*unitsStep) Name() string { return "units" }

// unitFiles returns the absolute unit paths and their content.
func (s *unitsStep) unitFiles() (map[string][]byte, error) {
	// The WireGuard tunnel lives inside the core; on the other transports the tunnel's own unit carries
	// the schedule, so the core must not restart on it.
	var coreRestart time.Duration
	if s.e.cfg.Warp.UsesWireGuard() {
		coreRestart = s.e.cfg.Warp.RestartEvery
	}
	xrayUnit, err := service.Xray(s.e.cfg.Paths.XrayBinary(), s.e.cfg.Paths.XrayConfig, coreRestart)
	if err != nil {
		return nil, err
	}
	dropIn, err := service.NginxDropIn()
	if err != nil {
		return nil, err
	}
	binary, err := s.e.binaryPath()
	if err != nil {
		return nil, err
	}
	renewUnit, err := service.Renew(binary, s.e.configPath)
	if err != nil {
		return nil, err
	}
	renewTimer, err := service.RenewTimer()
	if err != nil {
		return nil, err
	}
	var readable []string
	if s.e.cfg.MN.CAFile != "" {
		readable = append(readable, filepath.Dir(s.e.cfg.MN.CAFile))
	}
	agentUnit, err := service.Agent(binary, s.e.configPath, []string{
		s.e.cfg.Paths.ConfigDir,
		s.e.cfg.Paths.StateDir,
		// The agent re-renders the subscription page after every sync, and ProtectSystem=strict makes
		// everything outside ReadWritePaths read-only: without the site the page is written nowhere.
		s.e.cfg.Paths.SiteDir,
	}, readable)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{
		filepath.Join(s.e.cfg.Paths.UnitDir, service.XrayUnitName):   xrayUnit,
		s.e.cfg.Paths.NginxDropIn:                                    dropIn,
		filepath.Join(s.e.cfg.Paths.UnitDir, service.RenewUnitName):  renewUnit,
		filepath.Join(s.e.cfg.Paths.UnitDir, service.RenewTimerName): renewTimer,
		filepath.Join(s.e.cfg.Paths.UnitDir, service.AgentUnitName):  agentUnit,
	}
	// The boot unit is installed always, not only when the configuration asks for a table: what decides
	// is the record of what was applied and confirmed, and an operator who applies a table after this
	// install must not have to run `init` again to get it back after a reboot.
	bootUnit, err := service.FirewallBoot(binary, s.e.configPath, s.e.cfg.Paths.StateDir, s.e.cfg.Paths.UnitDir)
	if err != nil {
		return nil, err
	}
	files[filepath.Join(s.e.cfg.Paths.UnitDir, service.FirewallBootUnitName)] = bootUnit
	// The guard is installed only when the operator asked for it, and it needs read access to the web
	// server's log — the 4xx signal is the whole point of it.
	if s.e.cfg.Security.Guard.Enabled {
		readable := []string{s.e.cfg.Paths.ConfigDir, filepath.Dir(s.e.cfg.Paths.AccessLog)}
		guardUnit, err := service.Guard(binary, s.e.configPath, s.e.cfg.Paths.StateDir, readable...)
		if err != nil {
			return nil, err
		}
		files[filepath.Join(s.e.cfg.Paths.UnitDir, service.GuardUnitName)] = guardUnit
	}
	// The transport's unit exists only when the transport is enabled: a unit for a binary that is not
	// installed is a failed service in the journal on every boot.
	if s.e.cfg.Hysteria != nil {
		transportUnit, err := service.Hysteria(s.e.cfg.Paths.HysteriaBinary(), s.e.cfg.Paths.HysteriaConfig)
		if err != nil {
			return nil, err
		}
		files[filepath.Join(s.e.cfg.Paths.UnitDir, service.HysteriaUnitName)] = transportUnit
	}
	// The MASQUE sidecar's unit exists only while it is the transport, for the same reason as the QUIC
	// transport's: a unit for a binary that is not installed is a failed service on every boot.
	if s.e.cfg.Warp.UsesUsque() {
		sidecarUnit, err := service.Usque(s.e.cfg.Paths.UsqueBinary(), s.e.cfg.Paths.UsqueConfig, xray.DefaultUsqueSidecar, s.e.cfg.Warp.RestartEvery)
		if err != nil {
			return nil, err
		}
		files[filepath.Join(s.e.cfg.Paths.UnitDir, service.UsqueUnitName)] = sidecarUnit
		relayUnit, err := service.UsqueRelay(binary, xray.DefaultUsqueSocks, xray.DefaultUsqueSidecar)
		if err != nil {
			return nil, err
		}
		files[filepath.Join(s.e.cfg.Paths.UnitDir, service.UsqueRelayUnitName)] = relayUnit
	}
	// The blocklist timer exists only when there is a list to fetch: a daily unit that fails because
	// nobody configured a URL is noise in the journal, not a feature.
	if s.e.cfg.Security.Firewall.BlocklistURL != "" {
		blocklistUnit, err := service.Blocklist(binary, s.e.configPath)
		if err != nil {
			return nil, err
		}
		blocklistTimer, err := service.BlocklistTimer(s.e.cfg.Security.Firewall.BlocklistRefresh.Std())
		if err != nil {
			return nil, err
		}
		files[filepath.Join(s.e.cfg.Paths.UnitDir, service.BlocklistUnitName)] = blocklistUnit
		files[filepath.Join(s.e.cfg.Paths.UnitDir, service.BlocklistTimerName)] = blocklistTimer
	}
	return files, nil
}

func (s *unitsStep) Check(context.Context) (CheckResult, error) {
	files, err := s.unitFiles()
	if err != nil {
		return CheckResult{}, err
	}
	var stale []string
	for name, want := range files {
		got, err := os.ReadFile(name) // #nosec G304 -- a path from our own config.
		switch {
		case errors.Is(err, os.ErrNotExist):
			stale = append(stale, name)
		case err != nil:
			return CheckResult{}, fmt.Errorf("read %s: %w", name, err)
		case !bytes.Equal(got, want):
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		return CheckResult{Detail: "out of date: " + joinPaths(stale)}, nil
	}
	return CheckResult{Satisfied: true, Detail: "units are up to date"}, nil
}

func (s *unitsStep) Run(ctx context.Context) error {
	files, err := s.unitFiles()
	if err != nil {
		return err
	}
	now := s.e.now()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(name), err)
		}
		if err := state.WriteFileAtomic(name, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		// /etc/systemd/system is the distribution's; only a directory we created (the nginx drop-in)
		// is ours to remove.
		if dir := filepath.Dir(name); dir != s.e.cfg.Paths.UnitDir {
			s.e.st.AddPath(dir, state.KindDir, "units", now)
		}
		s.e.st.AddPath(name, state.KindFile, "units", now)
	}
	s.e.st.AddPath(filepath.Join(s.e.cfg.Paths.UnitDir, service.XrayUnitName), state.KindUnit, "units", now)
	if s.e.cfg.Hysteria != nil {
		s.e.st.AddPath(filepath.Join(s.e.cfg.Paths.UnitDir, service.HysteriaUnitName), state.KindUnit, "units", now)
	}
	s.e.st.Touch()

	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	s.e.log.Info("installed systemd units", "files", len(files))
	return nil
}

// joinPaths renders a list for a one-line Check detail.
func joinPaths(paths []string) string {
	out := ""
	for i, p := range paths {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
