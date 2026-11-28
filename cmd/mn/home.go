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
		fmt.Fprintln(stderr, "termit-mn home: use `home handoff --uv <id> --to <peer>`"+
			" or `home adopt --from <peer>`")
		return exitUsage
	}
	switch args[0] {
	case "handoff":
		return runHandoff(args[1:], stdout, stderr)
	case "adopt":
		return runAdopt(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "termit-mn home: unknown verb %q; it is `handoff` or `adopt`\n", args[0])
		return exitUsage
	}
}

// runAdopt takes over the nodes of a home that is gone for good. It is the rescue a dead machine needs: a
// handoff cannot happen without the old home, and the nodes keep their keys, so nobody visits them.
func runAdopt(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token, tokenFile := uvFlags("termit-mn home adopt", stderr)
	from := flags.String("from", "", "the peer that is gone, by its name in mesh.peers")
	uv := flags.String("uv", "", "take over only this node (id or domain)")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn home adopt: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *from == "" {
		fmt.Fprintln(stderr, "termit-mn home adopt: --from is required")
		return exitUsage
	}
	client, err := adminClient(*configPath, *socket, *token, *tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn home adopt: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var outcome admin.AdoptOutcome
	request := map[string]any{"peer": *from, "uv": *uv}
	if err := client.Call(callCtx, "home.adopt", request, &outcome); err != nil {
		fmt.Fprintf(stderr, "termit-mn home adopt: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "peer:      %s (gone)\n", outcome.Peer)
	fmt.Fprintf(stdout, "taken:     %d node(s)\n", outcome.Adopted)
	fmt.Fprintf(stdout, "new home:  %s\n", outcome.Home)
	for _, domain := range outcome.Nodes {
		fmt.Fprintf(stdout, "  %s\n", domain)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "The nodes keep their agent keys and pair credentials: point the mesh's name at this")
	fmt.Fprintln(stdout, "server and their next sync lands here. Remove the gone peer from mesh.peers, or every")
	fmt.Fprintf(stdout, "pull will keep failing. Nodes enrolled on it after cursor %d are unknown here and have\n",
		outcome.Cursor)
	fmt.Fprintln(stdout, "to enroll again.")
	return exitOK
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
	fmt.Fprintln(stdout, "The old key stopped working. On the node, with the key in a 0600 file:")
	fmt.Fprintln(stdout, "  termit-uv agentkey --install <file>")
	fmt.Fprintf(stdout, "  termit-uv init --mn %s\n", outcome.URL)
	fmt.Fprintln(stdout, "Until then the node cannot sync, and its peers keep the last state they were given.")
	return exitOK
}
