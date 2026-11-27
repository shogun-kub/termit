package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
)

// runHome is `termit-mn home …`: the operations that move a node between the servers of a mesh.
func runHome(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "termit-mn home: use `home handoff --uv <id> --to <peer>`")
		return exitUsage
	}
	switch args[0] {
	case "handoff":
		return runHandoff(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "termit-mn home: unknown verb %q; the only one is `handoff`\n", args[0])
		return exitUsage
	}
}

// runHandoff moves one node to another server in the mesh and prints the agent key the node needs next.
func runHandoff(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token, tokenFile := uvFlags("termit-mn home handoff", stderr)
	uv := flags.String("uv", "", "the node to hand over: its id or domain")
	to := flags.String("to", "", "the peer that takes it, by its name in mesh.peers")
	force := flags.Bool("force", false, "hand over even though the peer is far behind")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn home handoff: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *uv == "" || *to == "" {
		fmt.Fprintln(stderr, "termit-mn home handoff: --uv and --to are required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token, *tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn home handoff: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var outcome admin.HandoffOutcome
	request := map[string]any{"uv": *uv, "peer": *to, "force": *force}
	if err := client.Call(callCtx, "home.handoff", request, &outcome); err != nil {
		fmt.Fprintf(stderr, "termit-mn home handoff: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "node:      %s\n", outcome.UV)
	fmt.Fprintf(stdout, "new home:  %s (%s, term %d)\n", outcome.Peer, outcome.Home, outcome.Term)
	fmt.Fprintf(stdout, "agent key: %s\n", outcome.AgentKey)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "The old key stopped working. Install the new one on the node, point its mn.url at the new")
	fmt.Fprintln(stdout, "home, and restart the agent: `termit-uv init --relocate --agent-key-file <file>`.")
	fmt.Fprintln(stdout, "Until then the node cannot sync, and its peers keep the last state they were given.")
	return exitOK
}
