package main

import (
	"context"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// execRunner runs one command and returns its combined output.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

// runSync reconciles this node's core with the peer set the management server hands out.
func (a *application) runSync(args []string) int {
	stdout, stderr := a.stdout, a.stderr
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	dryRun := flags.Bool("dry-run", false, "ask and report, but do not touch the core")
	check := flags.Bool("check", false, "report drift and exit 4 when the node differs from the server; changes nothing")
	verbose := flags.Bool("v", false, "print the peer set")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "uv sync: %v\n", err)
		return exitFailure
	}
	st, err := state.LoadInstalled(filepath.Join(cfg.Paths.StateDir, "state.json"))
	if err != nil {
		fmt.Fprintf(stderr, "uv sync: %v\n", err)
		return exitFailure
	}
	if st.Credentials == nil || st.Credentials.AgentKey == "" {
		fmt.Fprintln(stderr, "uv sync: this node is not enrolled; run `uv enroll` first")
		return exitFailure
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	remote := agent.Remote{BaseURL: cfg.MN.URL, CAFile: cfg.MN.CAFile}
	answer, err := remote.Fetch(ctx, st.Credentials.AgentKey)
	if err != nil {
		fmt.Fprintf(stderr, "uv sync: %v\n", err)
		return exitFailure
	}

	if *verbose {
		fmt.Fprintf(stdout, "self: %s (%s) state=%s\n", answer.Self.Domain, answer.Self.ID, answer.Self.State)
		for _, peer := range answer.Peers {
			marker := ""
			if peer.Degraded {
				marker = " (degraded)"
			}
			fmt.Fprintf(stdout, "peer: %s%s %s\n", peer.Domain, marker, peer.ID)
		}
	}

	if *dryRun || *check {
		// A check must answer the question without answering it by changing the answer: generate what
		// the core would get, compare, and leave the file alone.
		preview := agent.Applier{
			Binary:     cfg.Paths.XrayBinary(),
			ConfigPath: cfg.Paths.XrayConfig,
			Path:       "/" + st.Identity.XHTTPPath + "/",
			SelfUUID:   st.Identity.SelfUUID,
			SelfEmail:  "owner",
		}
		result, err := preview.Preview(answer, st.LocalUsers)
		if err != nil {
			fmt.Fprintf(stderr, "uv sync: %v\n", err)
			return exitFailure
		}
		if result.Unchanged {
			fmt.Fprintf(stdout, "in sync: %d client(s), %d peer(s)\n", result.Clients, len(answer.Peers))
			return exitOK
		}
		fmt.Fprintf(stdout, "drift: %d to add, %d to remove\n", len(result.Added), len(result.Removed))
		for _, id := range result.Added {
			fmt.Fprintf(stdout, "  + %s\n", id)
		}
		for _, id := range result.Removed {
			fmt.Fprintf(stdout, "  - %s\n", id)
		}
		if *check {
			return exitDrift
		}
		fmt.Fprintf(stdout, "would apply %d peer(s); nothing was changed\n", len(answer.Peers))
		return exitOK
	}

	applier := agent.Applier{
		Binary:     cfg.Paths.XrayBinary(),
		ConfigPath: cfg.Paths.XrayConfig,
		Path:       "/" + st.Identity.XHTTPPath + "/",
		SelfUUID:   st.Identity.SelfUUID,
		SelfEmail:  "owner",
		Runner:     execRunner{},
		Owner:      "root:termit",
		Unit:       service.XrayUnitName,
	}
	result, err := applier.Apply(ctx, answer, st.LocalUsers)
	if err != nil {
		fmt.Fprintf(stderr, "uv sync: %v\n", err)
		return exitFailure
	}

	st.Peers = peersOf(answer)
	st.Touch()
	if err := st.Save(filepath.Join(cfg.Paths.StateDir, "state.json")); err != nil {
		fmt.Fprintf(stderr, "uv sync: %v\n", err)
		return exitFailure
	}

	switch {
	case result.Unchanged:
		fmt.Fprintf(stdout, "already in sync: %d client(s), %d peer(s)\n", result.Clients, len(answer.Peers))
	default:
		fmt.Fprintf(stdout, "applied: %d client(s), %d peer(s)\n", result.Clients, len(answer.Peers))
		for _, id := range result.Added {
			fmt.Fprintf(stdout, "  + %s\n", id)
		}
		for _, id := range result.Removed {
			fmt.Fprintf(stdout, "  - %s\n", id)
		}
	}
	return exitOK
}

// peersOf stores what the server said, so `uv status` can report the peer set without a network call.
func peersOf(answer proto.SyncResponse) []state.Peer {
	out := make([]state.Peer, 0, len(answer.Peers))
	for _, peer := range answer.Peers {
		out = append(out, state.Peer{
			ID:       peer.ID,
			Domain:   peer.Domain,
			Label:    peer.Label,
			UUID:     peer.UUID,
			Path:     peer.Path,
			State:    peer.State,
			Degraded: peer.Degraded,
		})
	}
	return out
}
