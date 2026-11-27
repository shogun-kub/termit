package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
)

// runEpoch is `termit-mn epoch …`: the pair-credential epoch of the whole mesh.
func runEpoch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "termit-mn epoch: use `epoch rotate`")
		return exitUsage
	}
	switch args[0] {
	case "rotate":
		return runEpochRotate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "termit-mn epoch: unknown verb %q; the only one is `rotate`\n", args[0])
		return exitUsage
	}
}

// runEpochRotate re-keys every pair credential in the mesh. It prints what moved and what to do about the
// window it opens: until each node has synced, its core still holds the old pair values.
func runEpochRotate(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token, tokenFile := uvFlags("termit-mn epoch rotate", stderr)
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn epoch rotate: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token, *tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn epoch rotate: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var outcome admin.EpochOutcome
	if err := client.Call(callCtx, "epoch.rotate", nil, &outcome); err != nil {
		fmt.Fprintf(stderr, "termit-mn epoch rotate: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "epoch:     %d → %d\n", outcome.From, outcome.To)
	fmt.Fprintf(stdout, "re-keyed:  %d node(s)\n", outcome.Nodes)
	fmt.Fprintf(stdout, "journal:   entry %d\n", outcome.Journal)
	if len(outcome.Peers) > 0 {
		fmt.Fprintf(stdout, "peers:     %v answered before the rotation\n", outcome.Peers)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Every peer pair now derives new UUIDs. Each node picks them up at its next sync — a mesh")
	fmt.Fprintln(stdout, "poll notices the change within one cycle — and until both ends of a pair have synced, that")
	fmt.Fprintln(stdout, "pair's traffic stops. Sync every node now rather than waiting for the daily run:")
	fmt.Fprintln(stdout, "  on each node: termit-uv sync")
	return exitOK
}
