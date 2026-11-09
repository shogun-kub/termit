package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// unitsStep installs the systemd units.
//
// They are written, not shipped, so the paths inside them follow the configuration; and they are
// compared before writing so `uv init` does not reload systemd on every run.
type unitsStep struct{ e *stepEnv }

func (*unitsStep) Name() string { return "units" }

// unitFiles returns the absolute unit paths and their content.
func (s *unitsStep) unitFiles() (map[string][]byte, error) {
	xrayUnit, err := service.Xray(s.e.cfg.Paths.XrayBinary(), s.e.cfg.Paths.XrayConfig)
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
	return map[string][]byte{
		filepath.Join(s.e.cfg.Paths.UnitDir, service.XrayUnitName):   xrayUnit,
		s.e.cfg.Paths.NginxDropIn:                                    dropIn,
		filepath.Join(s.e.cfg.Paths.UnitDir, service.RenewUnitName):  renewUnit,
		filepath.Join(s.e.cfg.Paths.UnitDir, service.RenewTimerName): renewTimer,
	}, nil
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
