package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// runMigrate is Scenario C from the node's side: the box was installed under a new domain with the
// same agent key, and this asks the server to verify the new address and move the record onto it.
//
// The box must already answer on the configured domain: the request carries the endpoint the server
// will check, and claiming a name the box does not serve would fail the same verification anyway.
func (a *application) runMigrate(args []string) int {
	flags := a.newFlagSet("migrate", `Moves this node to a new domain. Install the box on the new domain first
(uv init --domain NEW --relocate --agent-key-file ...), then --relocate asks the server to verify the
new address and swap it into the record.`)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	domain := flags.String("domain", "", "the new domain (default: the configured domain)")
	relocate := flags.Bool("relocate", false, "move this node to its configured domain (Scenario C)")
	noSync := flags.Bool("no-sync", false, "do not run a sync after a successful relocation")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv migrate: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if !*relocate {
		fmt.Fprintln(a.stderr, "uv migrate: the only mode in this build is --relocate")
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv migrate: %v\n", err)
		return exitFailure
	}
	st, err := state.LoadInstalled(filepath.Join(cfg.Paths.StateDir, "state.json"))
	if err != nil {
		fmt.Fprintf(a.stderr, "uv migrate: %v\n", err)
		return exitFailure
	}
	if st.Credentials == nil || st.Credentials.AgentKey == "" {
		fmt.Fprintln(a.stderr, "uv migrate: this node is not enrolled; there is nothing to move")
		return exitFailure
	}
	if target := *domain; target != "" && !strings.EqualFold(target, cfg.Domain) {
		fmt.Fprintf(a.stderr, "uv migrate: this box answers on %s, not %s; install it with `uv init --domain %s --relocate` first\n",
			cfg.Domain, target, target)
		return exitFailure
	}
	if st.Domain != "" && !strings.EqualFold(st.Domain, cfg.Domain) {
		fmt.Fprintf(a.stderr, "uv migrate: the state was written for %s but the configuration answers on %s; run `uv init` to converge them\n",
			st.Domain, cfg.Domain)
		return exitFailure
	}

	profile, err := localProfile(cfg, st, cfg.Domain)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv migrate: %v\n", err)
		return exitFailure
	}
	url, err := proto.BuildVLESSUrl(profile)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv migrate: %v\n", err)
		return exitFailure
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	remote := agent.Remote{BaseURL: cfg.MN.URL, CAFile: cfg.MN.CAFile}
	answer, err := remote.Relocate(ctx, st.Credentials.AgentKey, proto.RelocateRequest{
		Domain:        cfg.Domain,
		BasicVLESSURL: url,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "uv migrate: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "node:   %s\n", answer.Node.ID)
	fmt.Fprintf(a.stdout, "domain: %s\n", answer.Node.Domain)
	fmt.Fprintf(a.stdout, "state:  %s\n", answer.Node.State)
	if answer.Notice != "" {
		fmt.Fprintf(a.stdout, "notice: %s\n", answer.Notice)
	}
	if *noSync {
		return exitOK
	}
	// The record points here now; syncing immediately installs the probe credential and the peer set,
	// so the next scheduled check finds a node that is already serving everything it should.
	fmt.Fprintln(a.stdout)
	if code := a.runSync([]string{"--config", *configPath}); code != exitOK {
		fmt.Fprintln(a.stderr, "uv migrate: the node moved, but the first sync did not finish; the agent will retry")
	}
	return exitOK
}
