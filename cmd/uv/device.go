package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"strings"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// deviceUsage is the help for the device command group.
const deviceUsage = `Manages the devices the owner connects with: a phone, a laptop, a router.

These are local users. They exist only on this node, they are never shared with the mesh, and their
UUIDs never leave this machine.

  termit-uv device list
  termit-uv device add --label phone [--uuid 8f1c2d3e-...]
  termit-uv device remove phone

Adding or removing a device rewrites the proxy configuration and restarts the core, which drops
connections for a moment.`

// runDevice dispatches the device subcommands.
func (a *application) runDevice(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(a.stdout, deviceUsage)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	switch args[0] {
	case "list":
		return a.deviceList(args[1:])
	case "add":
		return a.deviceAdd(args[1:])
	case "remove", "rm":
		return a.deviceRemove(args[1:])
	default:
		fmt.Fprintf(a.stderr, "termit-uv device: unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

// deviceList prints what this node accepts besides itself.
func (a *application) deviceList(args []string) int {
	flags := flag.NewFlagSet("device list", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	cfg, node, code := a.loadInstalled(*configPath, "termit-uv device list")
	if code != exitOK {
		return code
	}

	if len(node.LocalUsers) == 0 {
		fmt.Fprintf(a.stdout, "no local devices; only this node's own device is configured\n")
		return exitOK
	}
	for _, user := range node.LocalUsers {
		uri, err := a.deviceURI(cfg, node, user.ID, user.Label)
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv device list: %v\n", err)
			return exitFailure
		}
		fmt.Fprintf(a.stdout, "%-16s %s\n", user.Label, state.Fingerprint(user.ID))
		fmt.Fprintf(a.stdout, "%16s %s\n", "", uri)
	}
	return exitOK
}

// deviceAdd records a new device and applies it.
func (a *application) deviceAdd(args []string) int {
	flags := flag.NewFlagSet("device add", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	label := flags.String("label", "", "a name you will recognise, e.g. phone")
	uuid := flags.String("uuid", "", "reuse a UUID instead of generating one")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if strings.TrimSpace(*label) == "" {
		fmt.Fprintln(a.stderr, "termit-uv device add: --label is required")
		return exitUsage
	}
	cfg, node, code := a.loadInstalled(*configPath, "termit-uv device add")
	if code != exitOK {
		return code
	}

	id := *uuid
	if id == "" {
		generated, err := install.NewUUID()
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv device add: %v\n", err)
			return exitFailure
		}
		id = generated
	}
	if err := node.AddLocalUser(id, *label, a.now()); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv device add: %v\n", err)
		return exitFailure
	}
	if err := node.SaveAt(cfg.Paths.StatePath(), a.now()); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv device add: %v\n", err)
		return exitFailure
	}
	if code := a.applyXrayConfig(cfg, *configPath, "termit-uv device add"); code != exitOK {
		return code
	}
	uri, err := a.deviceURI(cfg, node, id, *label)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv device add: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "added %s\n%s\n", *label, uri)
	return exitOK
}

// deviceRemove forgets a device and applies the change.
func (a *application) deviceRemove(args []string) int {
	flags := flag.NewFlagSet("device remove", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(a.stderr, "termit-uv device remove: give one device label or UUID")
		return exitUsage
	}
	cfg, node, code := a.loadInstalled(*configPath, "termit-uv device remove")
	if code != exitOK {
		return code
	}
	if err := node.RemoveLocalUser(flags.Arg(0)); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv device remove: %v\n", err)
		return exitFailure
	}
	if err := node.SaveAt(cfg.Paths.StatePath(), a.now()); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv device remove: %v\n", err)
		return exitFailure
	}
	if code := a.applyXrayConfig(cfg, *configPath, "termit-uv device remove"); code != exitOK {
		return code
	}
	fmt.Fprintf(a.stdout, "removed %s\n", flags.Arg(0))
	return exitOK
}

// applyXrayConfig regenerates the proxy configuration after a device change.
func (a *application) applyXrayConfig(cfg *config.Config, configPath, command string) int {
	ctx, cancel := a.context()
	defer cancel()
	installer, err := install.New(a.installOptions(cfg, configPath))
	if err != nil {
		fmt.Fprintf(a.stderr, "%s: %v\n", command, err)
		return exitFailure
	}
	report, err := installer.Run(ctx, install.RunOptions{
		Command: command,
		Only:    []string{"xray-config"},
		Force:   []string{"xray-config"},
	})
	a.printSteps(report)
	if err != nil {
		fmt.Fprintf(a.stderr, "%s: %v\n", command, err)
		return exitFailure
	}
	return exitOK
}

// deviceURI renders the client URI for one device.
func (a *application) deviceURI(cfg *config.Config, node *state.State, id, label string) (string, error) {
	return proto.BuildVLESSUrl(proto.Profile{
		Domain: cfg.Domain,
		UUID:   id,
		Path:   "/" + node.Identity.XHTTPPath + "/",
		Label:  label,
	})
}

// loadInstalled loads the configuration and state, reporting the standard messages.
func (a *application) loadInstalled(configPath, command string) (*config.Config, *state.State, int) {
	cfg, err := a.loadConfig(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", configPath)
		return nil, nil, exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "%s: %v\n", command, err)
		return nil, nil, exitFailure
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if errors.Is(err, state.ErrNotInstalled) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no state at %s\n", cfg.Paths.StatePath())
		return nil, nil, exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "%s: %v\n", command, err)
		return nil, nil, exitFailure
	}
	return cfg, node, exitOK
}
