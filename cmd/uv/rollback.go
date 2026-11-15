package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// uvRollback is the node's rollback pair: the timer that removes an unconfirmed table, and the oneshot
// that does it. The node writes both because it owns its unit directory; the management server's units
// come from its package, so it can only start and stop what is installed.
type uvRollback struct {
	app        *application
	unitDir    string
	configPath string
}

// Arm writes the two units, reloads systemd and restarts the timer.
func (r uvRollback) Arm(ctx context.Context) error {
	binary, err := r.app.executablePath()
	if err != nil {
		return err
	}
	oneshot, err := service.FirewallRollback(binary, r.configPath)
	if err != nil {
		return err
	}
	timer, err := service.FirewallRollbackTimer(service.ConfirmWindow)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		filepath.Join(r.unitDir, service.FirewallRollbackUnitName):  oneshot,
		filepath.Join(r.unitDir, service.FirewallRollbackTimerName): timer,
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(name), err)
		}
		if err := state.WriteFileAtomic(name, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if _, err := r.app.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	// restart, not start, and never enable: the timer exists only while an apply is unconfirmed, and
	// re-arming has to reset the window rather than be ignored because it is already running.
	if _, err := r.app.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"restart", service.FirewallRollbackTimerName}}); err != nil {
		return fmt.Errorf("start %s: %w", service.FirewallRollbackTimerName, err)
	}
	return nil
}

// Disarm stops the timer and removes both units. It is best effort by design: it is the way out of a
// firewall, so a host without the units must not fail it.
func (r uvRollback) Disarm(ctx context.Context) error {
	_, _ = r.app.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"disable", "--now", service.FirewallRollbackTimerName}})
	removed := false
	for _, name := range []string{service.FirewallRollbackTimerName, service.FirewallRollbackUnitName} {
		err := os.Remove(filepath.Join(r.unitDir, name))
		switch {
		case err == nil:
			removed = true
		case errors.Is(err, os.ErrNotExist):
		default:
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	if removed {
		if _, err := r.app.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w", err)
		}
	}
	return nil
}
