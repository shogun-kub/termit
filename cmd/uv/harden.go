package main

import (
	"context"
	"net/netip"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/hardencmd"
	"github.com/shogun-kub/termit/internal/uv/service"
)

// uvHarden is the node's half of the shared security commands: its table, its ports, its units, and the
// one thing the node has that the server does not — a web server access log for the 4xx signal.
type uvHarden struct {
	app *application
}

func (*uvHarden) Name() string { return "termit-uv" }

func (*uvHarden) Table() (string, []int) { return firewall.NodeTable, firewall.NodePorts }

func (*uvHarden) Units() hardencmd.Units {
	return hardencmd.Units{
		Guard:          service.GuardUnitName,
		Blocklist:      service.BlocklistUnitName,
		BlocklistTimer: service.BlocklistTimerName,
		Rollback:       service.FirewallRollbackUnitName,
		RollbackTimer:  service.FirewallRollbackTimerName,
	}
}

// Open loads the node's configuration and derives everything that depends on it. The management server
// is resolved once per command and cached for ten minutes: it is in the never-ban set, and a lookup on
// every cycle would hammer the resolver for an answer that changes rarely.
func (h *uvHarden) Open(path string) (*hardencmd.Component, error) {
	cfg, err := h.app.loadConfig(path)
	if err != nil {
		return nil, err
	}
	ssh, err := firewall.Prefixes(cfg.Security.Firewall.SSHFrom)
	if err != nil {
		return nil, err
	}
	addresses := &hardencmd.AddressCache{Now: h.app.now, TTL: 10 * time.Minute}
	return &hardencmd.Component{
		Security:   &cfg.Security.Config,
		ConfigPath: path,
		UnitDir:    cfg.Paths.UnitDir,
		AccessLog:  cfg.Paths.AccessLog,
		Paths: hardencmd.Paths{
			Firewall:  cfg.Paths.FirewallPath(),
			Bans:      cfg.Paths.BansPath(),
			Blocklist: cfg.Paths.BlocklistPath(),
		},
		Never: func(ctx context.Context) []netip.Prefix {
			extra := append([]netip.Prefix(nil), ssh...)
			return append(extra, addresses.Get(ctx, cfg.MN.URL)...)
		},
	}, nil
}

// env is the shared command's view of this process.
func (h *uvHarden) env() *hardencmd.Env {
	return &hardencmd.Env{
		Target:     h,
		ConfigPath: configPathDefault(),
		Stdout:     h.app.stdout,
		Stderr:     h.app.stderr,
		Exec:       h.app.exec,
		Now:        h.app.now,
		Root:       h.app.isRoot,
		Self:       h.app.executablePath,
		NewFlags:   h.app.newFlagSet,
		Rollback:   h.rollback,
	}
}

// rollback is the node's confirm-or-remove timer. The unit text and the paths are the node's, so the
// shared command only has to call Arm and Disarm.
func (h *uvHarden) rollback(component *hardencmd.Component) hardencmd.Rollback {
	return uvRollback{app: h.app, unitDir: component.UnitDir, configPath: component.ConfigPath}
}

// harden is the Env for one command.
func (a *application) harden() *hardencmd.Env { return (&uvHarden{app: a}).env() }
