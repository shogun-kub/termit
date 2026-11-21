package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/uv/service"
)

// hysteriaOffStep retires the transport when the configuration stops enabling it.
//
// The block in config.yaml is the switch, and a switch that only turns one way is not one: `init`
// converges on what the file says, so a node whose file no longer has `hysteria:` must end up without the
// unit, the configuration and the binary. Leaving them behind would mean a port still open and a process
// still running that nothing in the configuration explains.
//
// The service user and the manifest entries stay. The user is harmless — no shell, no home, no writable
// path — and `uninstall --purge` is what removes it; the manifest is how uninstall still knows what this
// node once had.
type hysteriaOffStep struct{ e *stepEnv }

func (*hysteriaOffStep) Name() string { return "hysteria-off" }

// leftovers are the things an enabled transport leaves on disk.
func (s *hysteriaOffStep) leftovers() []string {
	paths := []string{
		filepath.Join(s.e.cfg.Paths.UnitDir, service.HysteriaUnitName),
		s.e.cfg.Paths.HysteriaConfig,
		s.e.cfg.Paths.HysteriaBinary(),
	}
	if pin, err := hysteriaPin(s.e.arch); err == nil {
		// The verified download is cached beside the core's archive; remove ours, not the directory.
		paths = append(paths, filepath.Join(s.e.cfg.Paths.StateDir, "cache", pin.Name))
	}
	return paths
}

func (s *hysteriaOffStep) Check(ctx context.Context) (CheckResult, error) {
	if s.e.cfg.Hysteria != nil {
		return CheckResult{Satisfied: true, Detail: "the QUIC transport is enabled"}, nil
	}
	var found []string
	for _, path := range s.leftovers() {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	if s.e.serviceActive(ctx, service.HysteriaUnitName) {
		found = append(found, service.HysteriaUnitName+" is running")
	}
	if len(found) == 0 {
		return CheckResult{Satisfied: true, Detail: "the transport is not installed"}, nil
	}
	return CheckResult{Detail: fmt.Sprintf("the transport is still installed: %d item(s) left behind", len(found))}, nil
}

func (s *hysteriaOffStep) Run(ctx context.Context) error {
	if s.e.cfg.Hysteria != nil {
		return nil
	}
	// Stop it before removing the unit file: a service whose unit disappears from under a running systemd
	// keeps running until the next boot, which is the opposite of what the operator asked for.
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"disable", "--now", service.HysteriaUnitName}}); err != nil {
		s.e.log.Debug("disabling the retired transport reported an error", "err", err)
	}
	removed := 0
	for _, path := range s.leftovers() {
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("hysteria-off: remove %s: %w", path, err)
		}
		removed++
	}
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		s.e.log.Debug("daemon-reload after retiring the transport reported an error", "err", err)
	}
	if removed > 0 {
		s.e.log.Info("retired the QUIC transport: the configuration no longer enables it", "removed", removed)
	}
	return nil
}
