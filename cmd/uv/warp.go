package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// jsonWarp is the machine-readable egress report.
type jsonWarp struct {
	Mode     string      `json:"mode"`
	State    string      `json:"state"`
	Detail   string      `json:"detail,omitempty"`
	Socks    string      `json:"socks,omitempty"`
	Selected []string    `json:"selected,omitempty"`
	Trace    *warp.Trace `json:"trace,omitempty"`
}

func (a *application) runWarp(args []string) int {
	if len(args) > 0 && args[0] == "mode" {
		return a.runWarpMode(args[1:])
	}
	return a.runWarpStatus(args)
}

// runWarpStatus reports what the node's egress is doing. It leaves the host only when WARP is on and
// this machine could run it: the whole point of the check is the address Cloudflare sees.
func (a *application) runWarpStatus(args []string) int {
	flags := a.newFlagSet("warp [status]", `Reports the egress: the configured mode, what warp-cli says, and what a fetch
through the tunnel says Cloudflare sees. WARP is on by default; `+"`uv warp mode off`"+` exits directly.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv warp: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv warp: %v\n", err)
		return exitFailure
	}
	report := jsonWarp{Mode: cfg.Warp.Mode, Socks: warp.SocksAddress, Selected: cfg.Warp.Selected}
	client := warp.New(warpRunner{exec: a.exec})
	hostErr := client.Host.Check()
	switch {
	case !cfg.Warp.UsesWarp():
		report.State = "off"
		report.Detail = "proxied traffic exits directly"
	case hostErr != nil:
		report.State = "unavailable"
		report.Detail = hostErr.Error()
	default:
		status, err := client.Status(context.Background())
		if err != nil {
			fmt.Fprintf(a.stderr, "uv warp: %v\n", err)
			return exitFailure
		}
		report.Detail = status.Detail
		report.Trace = status.Trace
		switch {
		case status.Healthy():
			report.State = "on"
		case status.State == "missing":
			report.State = "not-installed"
		default:
			report.State = "direct"
			if !cfg.Warp.FailOpenEnabled() {
				report.State = "blocked"
			}
		}
	}

	if *jsonOut {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "uv warp: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, string(body))
		return exitOK
	}
	fmt.Fprintf(a.stdout, "mode:    %s\n", report.Mode)
	fmt.Fprintf(a.stdout, "egress:  %s\n", report.State)
	if report.Detail != "" {
		fmt.Fprintf(a.stdout, "detail:  %s\n", report.Detail)
	}
	if cfg.Warp.UsesWarp() {
		fmt.Fprintf(a.stdout, "socks:   %s\n", report.Socks)
		if cfg.Warp.Mode == config.WarpSelected {
			fmt.Fprintf(a.stdout, "domains: %s\n", strings.Join(report.Selected, ", "))
		}
		fmt.Fprintf(a.stdout, "fail:    %s\n", map[bool]string{true: "open (direct)", false: "closed (no egress)"}[cfg.Warp.FailOpenEnabled()])
	}
	return exitOK
}

// runWarpMode changes the egress, converges the sidecar and regenerates the core's route. A routing
// change is not something the core's hot API can do, so the core is restarted — after `xray -test`
// has accepted the new file.
func (a *application) runWarpMode(args []string) int {
	flags := a.newFlagSet("warp mode", `Changes the egress mode: all (default), selected or off. Turning it on installs
and registers cloudflare-warp when this host can run it; turning it off disconnects the tunnel and
removes the route, and direct traffic is never affected.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	selected := flags.String("selected", "", "domains that use WARP in selected mode, comma separated")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(a.stderr, "uv warp mode: one mode is required: all, selected or off")
		return exitUsage
	}
	mode := strings.ToLower(strings.TrimSpace(flags.Arg(0)))
	switch mode {
	case config.WarpAll, config.WarpSelected, config.WarpOff:
	default:
		fmt.Fprintf(a.stderr, "uv warp mode: %q is not off, all or selected\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv warp: %v\n", err)
		return exitFailure
	}
	if *selected != "" {
		cfg.Warp.Selected = nil
		for _, domain := range splitList(*selected) {
			cfg.Warp.Selected = append(cfg.Warp.Selected, domain)
		}
	}
	cfg.Warp.Mode = mode
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(a.stderr, "uv warp mode: %v\n", err)
		return exitUsage
	}
	sort.Strings(cfg.Warp.Selected)

	client := warp.New(warpRunner{exec: a.exec})
	if mode != config.WarpOff {
		if err := client.Host.Check(); err != nil {
			// The intent is still worth recording: the node keeps its route and exits directly, and
			// the moment the host gains a tun device a re-run of `uv init` turns WARP on.
			fmt.Fprintf(a.stdout, "WARP cannot run on this host: %v\n", err)
			fmt.Fprintln(a.stdout, "the mode is recorded and traffic exits directly")
		} else if err := client.Ensure(context.Background()); err != nil {
			fmt.Fprintf(a.stderr, "uv warp mode: %v\n", err)
			return exitFailure
		}
	} else if client.Installed(context.Background()) {
		if err := client.Disconnect(context.Background()); err != nil {
			fmt.Fprintf(a.stderr, "uv warp mode: %v\n", err)
			return exitFailure
		}
	}

	if err := cfg.Save(*configFlag); err != nil {
		fmt.Fprintf(a.stderr, "uv warp mode: %v\n", err)
		return exitFailure
	}
	if code := a.applyWarpRoute(cfg, *configFlag); code != exitOK {
		return code
	}
	fmt.Fprintf(a.stdout, "egress mode: %s\n", mode)
	if mode == config.WarpOff {
		fmt.Fprintln(a.stdout, "the tunnel is disconnected and proxied traffic exits directly")
	}
	return exitOK
}

// applyWarpRoute regenerates the core's configuration from the new mode. The xray-config step writes
// it atomically, runs `xray -test`, activates it and restarts the core — and rolls back if the core
// refuses it — so this command never restarts anything itself.
//
// The agent is restarted afterwards on purpose: it holds the configuration it read at startup, and
// its next cycle would otherwise regenerate the old route and undo this change.
func (a *application) applyWarpRoute(cfg *config.Config, configPath string) int {
	installer, err := install.New(a.installOptions(cfg, configPath))
	if err != nil {
		fmt.Fprintf(a.stderr, "uv warp mode: %v\n", err)
		return exitFailure
	}
	report, err := installer.Run(context.Background(), install.RunOptions{
		Command: "warp",
		Only:    []string{"xray-config"},
		Force:   []string{"xray-config"},
	})
	if err != nil {
		a.printSteps(report)
		fmt.Fprintf(a.stderr, "uv warp mode: %v\n", err)
		return exitFailure
	}
	if _, err := a.exec.Run(context.Background(), install.Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", "termit-uv-agent"}}); err == nil {
		// Same reason as the core's restart in the step: a deliberate restart must not be refused
		// because an earlier one was too recent.
		_, _ = a.exec.Run(context.Background(), install.Cmd{Name: "systemctl", Args: []string{"reset-failed", "termit-uv-agent"}})
		if _, err := a.exec.Run(context.Background(), install.Cmd{Name: "systemctl", Args: []string{"restart", "termit-uv-agent"}}); err != nil {
			fmt.Fprintf(a.stderr, "uv warp mode: restart the agent: %v\n", err)
			return exitFailure
		}
	}
	return exitOK
}
