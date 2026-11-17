package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/tunnelprobe"
	"github.com/shogun-kub/termit/internal/uv/warp"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// jsonWarp is the machine-readable egress report.
type jsonWarp struct {
	Mode      string      `json:"mode"`
	Transport string      `json:"transport"`
	State     string      `json:"state"`
	Detail    string      `json:"detail,omitempty"`
	Socks     string      `json:"socks,omitempty"`
	Selected  []string    `json:"selected,omitempty"`
	Trace     *warp.Trace `json:"trace,omitempty"`
	// DeviceID and Peer describe the wireguard identity. The key material is never in this report.
	DeviceID string `json:"device_id,omitempty"`
	Peer     string `json:"peer,omitempty"`
}

func (a *application) runWarp(args []string) int {
	// `termit-uv warp` is the status. `termit-uv warp status` is accepted because the documentation has always
	// shown it that way — the same shape as `termit-uv warp mode`.
	if len(args) > 0 {
		switch args[0] {
		case "mode":
			return a.runWarpMode(args[1:])
		case "status":
			return a.runWarpStatus(args[1:])
		case "rotate":
			return a.runWarpRotate(args[1:])
		}
	}
	return a.runWarpStatus(args)
}

// runWarpStatus reports what the node's egress is doing. It leaves the host only when WARP is on and
// this machine could run it: the whole point of the check is the address Cloudflare sees.
func (a *application) runWarpStatus(args []string) int {
	flags := a.newFlagSet("warp [status]", `Reports the egress: the configured mode, what warp-cli says, and what a fetch
through the tunnel says Cloudflare sees. WARP is on by default; `+"`termit-uv warp mode off`"+` exits directly.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv warp: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv warp: %v\n", err)
		return exitFailure
	}
	report := jsonWarp{Mode: cfg.Warp.Mode, Transport: cfg.Warp.TransportOrDefault(), Socks: warp.SocksAddress, Selected: cfg.Warp.Selected}
	if cfg.Warp.UsesWireGuard() {
		if code := a.wireGuardStatus(cfg, &report); code != exitOK {
			return code
		}
	} else {
		client := a.warpClient()
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
				fmt.Fprintf(a.stderr, "termit-uv warp: %v\n", err)
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
	}

	if *jsonOut {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv warp: %v\n", err)
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
	if report.Transport == config.WarpTransportWireGuard {
		fmt.Fprintf(a.stdout, "tunnel:  wireguard (inside the core)\n")
		if report.DeviceID != "" {
			fmt.Fprintf(a.stdout, "identity: %s\n", report.DeviceID)
		}
		if report.Peer != "" {
			fmt.Fprintf(a.stdout, "peer:    %s\n", report.Peer)
		}
	} else if cfg.Warp.UsesWarp() {
		fmt.Fprintf(a.stdout, "socks:   %s\n", report.Socks)
	}
	if cfg.Warp.UsesWarp() {
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
		fmt.Fprintln(a.stderr, "termit-uv warp mode: one mode is required: all, selected or off")
		return exitUsage
	}
	mode := strings.ToLower(strings.TrimSpace(flags.Arg(0)))
	switch mode {
	case config.WarpAll, config.WarpSelected, config.WarpOff:
	default:
		fmt.Fprintf(a.stderr, "termit-uv warp mode: %q is not off, all or selected\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv warp: %v\n", err)
		return exitFailure
	}
	if *selected != "" {
		cfg.Warp.Selected = nil
		cfg.Warp.Selected = append(cfg.Warp.Selected, splitList(*selected)...)
	}
	cfg.Warp.Mode = mode
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv warp mode: %v\n", err)
		return exitUsage
	}
	sort.Strings(cfg.Warp.Selected)

	client := a.warpClient()
	switch {
	case mode == config.WarpOff:
		if client.Installed(context.Background()) {
			if err := client.Disconnect(context.Background()); err != nil {
				fmt.Fprintf(a.stderr, "termit-uv warp mode: %v\n", err)
				return exitFailure
			}
		}
	case cfg.Warp.UsesWireGuard():
		// The tunnel lives in the core: there is no sidecar to install, start or connect, and no tun
		// device to look for. The registration it does need is the `warp` step applyWarpRoute runs.
	default:
		if err := client.Host.Check(); err != nil {
			// The intent is still worth recording: the node keeps its route and exits directly, and
			// the moment the host gains a tun device a re-run of `termit-uv init` turns WARP on.
			fmt.Fprintf(a.stdout, "WARP cannot run on this host: %v\n", err)
			fmt.Fprintln(a.stdout, "the mode is recorded and traffic exits directly")
		} else if err := client.Ensure(context.Background()); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv warp mode: %v\n", err)
			return exitFailure
		}
	}

	if err := cfg.Save(*configFlag); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv warp mode: %v\n", err)
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
		fmt.Fprintf(a.stderr, "termit-uv warp mode: %v\n", err)
		return exitFailure
	}
	only := []string{"xray-config"}
	if cfg.Warp.UsesWarp() && cfg.Warp.UsesWireGuard() {
		// A node that has never been registered needs the `warp` step before the document can point
		// at the tunnel; the step reports an unreachable Cloudflare as a warning, not a failure.
		only = append(only, "warp")
	}
	report, err := installer.Run(context.Background(), install.RunOptions{
		Command: "warp",
		Only:    only,
		Force:   []string{"xray-config"},
	})
	if err != nil {
		a.printSteps(report)
		fmt.Fprintf(a.stderr, "termit-uv warp mode: %v\n", err)
		return exitFailure
	}
	a.printSteps(report)
	if _, err := a.exec.Run(context.Background(), install.Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", "termit-uv-agent"}}); err == nil {
		// Same reason as the core's restart in the step: a deliberate restart must not be refused
		// because an earlier one was too recent.
		_, _ = a.exec.Run(context.Background(), install.Cmd{Name: "systemctl", Args: []string{"reset-failed", "termit-uv-agent"}})
		if _, err := a.exec.Run(context.Background(), install.Cmd{Name: "systemctl", Args: []string{"restart", "termit-uv-agent"}}); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv warp mode: restart the agent: %v\n", err)
			return exitFailure
		}
	}
	return exitOK
}

// wireGuardStatus reports the transport that lives inside the core. There is no sidecar to ask and no
// SOCKS port to dial, so the exit is checked the way a client would check it: through the tunnel,
// against Cloudflare's trace. A probe that cannot run is a skip, never a claim.
func (a *application) wireGuardStatus(cfg *config.Config, report *jsonWarp) int {
	if !cfg.Warp.UsesWarp() {
		report.State = "off"
		report.Detail = "proxied traffic exits directly"
		return exitOK
	}
	identity, err := warp.LoadIdentity(cfg.Paths.WarpIdentityPath())
	if err != nil {
		report.State = "not-installed"
		report.Detail = "no WARP identity: " + err.Error()
		return exitOK
	}
	report.DeviceID = identity.DeviceID
	report.Peer = identity.Endpoint
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		report.State = "unknown"
		report.Detail = "the node has no identity to probe with: " + err.Error()
		return exitOK
	}
	body, err := tunnelprobe.Fetch(context.Background(), tunnelprobe.Options{
		XrayBinary: cfg.Paths.XrayBinary(),
		ProxyAddr:  fmt.Sprintf("%s:%d", xray.DefaultListen, xray.DefaultPort),
		Path:       "/" + node.Identity.XHTTPPath + "/",
		UUID:       node.Identity.SelfUUID,
		TargetURL:  xray.WarpProbeURL,
	})
	if err != nil {
		report.State = "unknown"
		report.Detail = "the probe through the tunnel failed: " + err.Error()
		return exitOK
	}
	trace := warp.ParseTrace(body)
	report.Trace = &trace
	if trace.ThroughWarp() {
		report.State = "on"
		report.Detail = "via " + trace.IP
		return exitOK
	}
	report.State = "direct"
	report.Detail = "the tunnel exits at " + trace.IP
	if !cfg.Warp.FailOpenEnabled() {
		report.State = "blocked"
	}
	return exitOK
}

// runWarpRotate replaces the node's WARP registration on the wireguard transport. It is a deliberate
// act: a new identity is a new device on Cloudflare's side, and the old one keeps existing there. The
// proxy transport has nothing to rotate — warp-cli owns its registration.
func (a *application) runWarpRotate(args []string) int {
	flags := a.newFlagSet("warp rotate", `Registers this node with Cloudflare again, replacing the WARP identity the
core's WireGuard outbound uses. The old device stays on Cloudflare's side; this command gives the node a
new one.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv warp rotate: %v\n", err)
		return exitFailure
	}
	if !cfg.Warp.UsesWireGuard() {
		fmt.Fprintf(a.stderr, "termit-uv warp rotate: this node uses the proxy transport, where warp-cli owns the registration\n")
		return exitUsage
	}
	identity, err := warp.Registrar{}.Register(context.Background())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv warp rotate: %v\n", err)
		return exitFailure
	}
	if err := warp.SaveIdentity(cfg.Paths.WarpIdentityPath(), identity); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv warp rotate: %v\n", err)
		return exitFailure
	}
	if code := a.applyWarpRoute(cfg, *configFlag); code != exitOK {
		return code
	}
	fmt.Fprintf(a.stdout, "warp: new identity %s, endpoint %s\n", identity.DeviceID, identity.Endpoint)
	return exitOK
}
