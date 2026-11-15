package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/hardencmd"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/uv/install"
)

// The server's unit names. Its units come from the package in deploy/systemd, not from a renderer: the
// server is deployed as a package, and a binary that rewrites its own units is a binary that can break a
// host nobody is watching. `firewall on` therefore refuses to run until the rollback timer is installed.
const (
	serverGuardUnitName      = "termit-mn-guard.service"
	serverBlocklistUnitName  = "termit-mn-blocklist.service"
	serverBlocklistTimerName = "termit-mn-blocklist.timer"
	serverRollbackUnitName   = "termit-mn-firewall-rollback.service"
	serverRollbackTimerName  = "termit-mn-firewall-rollback.timer"
	serverConfigPath         = "/etc/termit/mn/config.yaml"
)

// mnHarden is the server's half of the shared security commands.
type mnHarden struct {
	exec install.Executor
	now  func() time.Time
	root func() bool
}

func (*mnHarden) Name() string { return "termit-mn" }

func (*mnHarden) Table() (string, []int) { return firewall.ServerTable, firewall.ServerPorts }

func (*mnHarden) Units() hardencmd.Units {
	return hardencmd.Units{
		Guard:          serverGuardUnitName,
		Blocklist:      serverBlocklistUnitName,
		BlocklistTimer: serverBlocklistTimerName,
		Rollback:       serverRollbackUnitName,
		RollbackTimer:  serverRollbackTimerName,
	}
}

// Open loads the server's configuration. There is no access log to watch — the server writes its log to
// the journal, not to a file — so only the kernel's scan meter produces bans here.
func (h *mnHarden) Open(path string) (*hardencmd.Component, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return &hardencmd.Component{
		Security:   &cfg.Security,
		ConfigPath: path,
		Paths: hardencmd.Paths{
			Firewall:  cfg.FirewallPath(),
			Bans:      cfg.BansPath(),
			Blocklist: cfg.BlocklistPath(),
		},
		// The built-in ranges are the whole protected set on the server: it has no management server of its
		// own to keep reachable, and ssh_from is already part of that set.
		Never: nil,
	}, nil
}

// env is the shared command's view of this process.
func (h *mnHarden) env(stdout, stderr io.Writer) *hardencmd.Env {
	return &hardencmd.Env{
		Target:     h,
		ConfigPath: serverConfigPath,
		Stdout:     stdout,
		Stderr:     stderr,
		Exec:       h.exec,
		Now:        h.now,
		Root:       h.root,
		Rollback:   h.rollback,
	}
}

// rollback is the server's confirm-or-remove timer: the pair is installed by the package, so Arm can only
// restart it and Disarm can only stop it. Arm failing is the safe outcome — an apply with no way back is
// worse than a refused command.
func (h *mnHarden) rollback(*hardencmd.Component) hardencmd.Rollback {
	return staticRollback{exec: h.exec, timer: serverRollbackTimerName}
}

// staticRollback starts and stops a unit that is already installed.
type staticRollback struct {
	exec  install.Executor
	timer string
}

func (r staticRollback) Arm(ctx context.Context) error {
	if _, err := r.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"restart", r.timer}}); err != nil {
		return fmt.Errorf("%s is not installed: install the units from deploy/systemd before applying the table (%w)", r.timer, err)
	}
	return nil
}

func (r staticRollback) Disarm(ctx context.Context) error {
	_, _ = r.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"disable", "--now", r.timer}})
	return nil
}
