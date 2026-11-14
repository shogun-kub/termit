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
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
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
		fmt.Fprintf(stderr, "termit-uv sync: %v\n", err)
		return exitFailure
	}
	st, err := state.LoadInstalled(filepath.Join(cfg.Paths.StateDir, "state.json"))
	if err != nil {
		fmt.Fprintf(stderr, "termit-uv sync: %v\n", err)
		return exitFailure
	}
	if st.Credentials == nil || st.Credentials.AgentKey == "" {
		fmt.Fprintln(stderr, "termit-uv sync: this node is not enrolled; run `termit-uv enroll` first")
		return exitFailure
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// One reconciliation at a time: the agent service runs the same cycle, and two writers must not
	// interleave a staged config with the other's rename.
	unlock, err := state.Lock(cfg.Paths.StatePath() + ".lock")
	if err != nil {
		fmt.Fprintf(stderr, "termit-uv sync: %v\n", err)
		return exitFailure
	}
	defer unlock()

	remote := agent.Remote{BaseURL: cfg.MN.URL, CAFile: cfg.MN.CAFile, CertPin: agent.CertFingerprint(install.LeafCertPath(cfg))}
	answer, err := remote.Fetch(ctx, st.Credentials.AgentKey, proto.SyncRequest{AgentVersion: version.Agent("termit-uv")})
	if err != nil {
		fmt.Fprintf(stderr, "termit-uv sync: %v\n", err)
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
		// the core would get, compare it with the file and with the running client set, and leave both
		// alone.
		result, err := a.applier(cfg, st).Preview(ctx, answer, st.LocalUsers)
		if err != nil {
			fmt.Fprintf(stderr, "termit-uv sync: %v\n", err)
			return exitFailure
		}
		if result.Unchanged {
			fmt.Fprintf(stdout, "in sync: %d client(s), %d peer(s)\n", result.Clients, len(answer.Peers))
			return exitOK
		}
		fmt.Fprintf(stdout, "drift: %d to add, %d to remove\n", len(result.Added), len(result.Removed))
		if result.HotNote != "" {
			fmt.Fprintf(stdout, "  note: %s\n", result.HotNote)
		}
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

	applier := a.applier(cfg, st)
	result, err := applier.Apply(ctx, answer, st.LocalUsers)
	if err != nil {
		fmt.Fprintf(stderr, "termit-uv sync: %v\n", err)
		return exitFailure
	}

	st.Peers = agent.PeersFrom(answer)
	agent.RememberSubscription(st, answer)
	now := a.now().UTC()
	st.Schedule.LastSyncAt = now
	st.Schedule.LastResult = "ok"
	st.Schedule.Failures = 0
	enrolledAt := time.Time{}
	if st.Credentials != nil {
		enrolledAt = st.Credentials.EnrolledAt
	}
	st.Schedule.NextSyncAt = agent.NextDue(now, answer, enrolledAt, st.Identity.SelfUUID)
	st.Touch()
	if err := st.Save(filepath.Join(cfg.Paths.StateDir, "state.json")); err != nil {
		fmt.Fprintf(stderr, "termit-uv sync: %v\n", err)
		return exitFailure
	}

	switch {
	case result.Unchanged && len(result.Added) == 0 && len(result.Removed) == 0:
		fmt.Fprintf(stdout, "already in sync: %d client(s), %d peer(s)\n", result.Clients, len(answer.Peers))
	default:
		mode := "restart"
		if result.Hot {
			mode = "hot API"
		}
		fmt.Fprintf(stdout, "applied: %d client(s), %d peer(s) via %s (canary=%v)\n",
			result.Clients, len(answer.Peers), mode, result.CanaryRan)
		if result.HotNote != "" {
			fmt.Fprintf(stdout, "  why not hot: %s\n", result.HotNote)
		}
		for _, id := range result.Added {
			fmt.Fprintf(stdout, "  + %s\n", id)
		}
		for _, id := range result.Removed {
			fmt.Fprintf(stdout, "  - %s\n", id)
		}
	}
	fmt.Fprintf(stdout, "next sync: %s\n", st.Schedule.NextSyncAt.Format(time.RFC3339))
	return exitOK
}
