package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// runURI prints the client URI for this node's own device.
//
// It is the one secret a user asks for by name, so it goes to stdout and nowhere else: no logging, no
// journal, no config. Everything else in the node's state stays behind a fingerprint.
func (a *application) runURI(args []string) int {
	flags := flag.NewFlagSet("uri", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	label := flags.String("label", "", "display name to embed in the URI (default: the domain)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := a.loadConfig(*configPath)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", *configPath)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "uv uri: %v\n", err)
		return exitFailure
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if errors.Is(err, state.ErrNotInstalled) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no state at %s\n", cfg.Paths.StatePath())
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "uv uri: %v\n", err)
		return exitFailure
	}
	if node.Identity.SelfUUID == "" || node.Identity.XHTTPPath == "" {
		fmt.Fprintln(a.stderr, "uv uri: the node has no identity yet; run `uv init`")
		return exitFailure
	}

	name := *label
	if name == "" {
		name = cfg.Domain
	}
	uri, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: cfg.Domain,
		UUID:   node.Identity.SelfUUID,
		Path:   "/" + node.Identity.XHTTPPath + "/",
		Label:  name,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "uv uri: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(a.stdout, uri)
	return exitOK
}
