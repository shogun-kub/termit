package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/version"
)

// scheduledStates are the states the scheduler probes; a removed node is out of the mesh and a check
// would only produce noise.
var scheduledStates = map[string]bool{
	"pending": true, "active": true, "unavailable": true, "blocked": true,
}

// runStatus is the one screen an operator opens first: what the server is, how the mesh is doing and
// when each node is looked at again. It reads only through the admin socket, like every other command.
func runStatus(args []string, stdout, stderr io.Writer) int {
	flags, configPath, socket, token := uvFlags("status", stderr)
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "mn status: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mn status: %v\n", err)
		return exitFailure
	}
	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn status: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var nodes []admin.NodeView
	if err := client.Call(callCtx, "uv.list", nil, &nodes); err != nil {
		fmt.Fprintf(stderr, "mn status: %v\n", err)
		return exitFailure
	}
	// A runtime override, if any, is what the scheduler actually uses; the file value is the fallback.
	var policy struct {
		Policy map[string]string `json:"policy"`
	}
	interval, note := cfg.Policy.CheckInterval, ""
	if err := client.Call(callCtx, "policy.show", nil, &policy); err == nil {
		if value := policy.Policy["check_interval"]; value != "" {
			if parsed, parseErr := time.ParseDuration(value); parseErr == nil {
				interval, note = parsed, " (runtime override)"
			}
		}
	}

	fmt.Fprintf(stdout, "server:   %s\n", cfg.Domain)
	fmt.Fprintf(stdout, "listen:   %s\n", cfg.Listen)
	fmt.Fprintf(stdout, "version:  %s\n", version.String("termit-mn"))
	fmt.Fprintf(stdout, "nodes:    %d (%s)\n", len(nodes), summarizeStates(nodes))
	fmt.Fprintf(stdout, "check:    every %s, times below are UTC%s\n", humanInterval(interval), note)
	fmt.Fprintln(stdout)

	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "DOMAIN\tSTATE\tFAILED\tLAST CHECK\tNEXT CHECK\tLAST SEEN")
	for _, node := range nodes {
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\t%s\n",
			node.Domain, node.State, node.FailedCycles,
			checkTime(node.LastCheckAt), checkTime(node.NextCheckAt), checkTime(node.LastSeenAt))
	}
	if err := table.Flush(); err != nil {
		fmt.Fprintf(stderr, "mn status: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// summarizeStates is the one-line mesh summary: only the states that occur, in lifecycle order.
func summarizeStates(nodes []admin.NodeView) string {
	order := []string{"active", "unavailable", "blocked", "pending", "removed"}
	counts := make(map[string]int, len(order))
	for _, node := range nodes {
		counts[node.State]++
	}
	parts := make([]string, 0, len(order))
	for _, state := range order {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// checkTime renders an optional timestamp for a table cell. UTC keeps the dashboard comparable with
// the logs, which are UTC too.
func checkTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "-"
	}
	return at.UTC().Format("2006-01-02 15:04")
}

// humanInterval is the dashboard form of a check interval: "24h" rather than "24h0m0s", because the
// dashboard is read by a person.
func humanInterval(d time.Duration) string {
	if d >= time.Hour {
		hours := int(d / time.Hour)
		if minutes := int(d % time.Hour / time.Minute); minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
}

// runServerCheck runs one cycle for every node the scheduler would probe. With a node argument it is
// the same command as `mn uv check now <node>`; without one it is the "check the whole mesh now"
// operation, which is what an operator reaches for after fixing DNS or a firewall.
func runServerCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "now" {
		fmt.Fprintln(stderr, "mn check: the only subcommand in this build is now")
		return exitUsage
	}
	flags, configPath, socket, token := uvFlags("check now", stderr)
	all := flags.Bool("all", false, "check every node the scheduler probes")
	if err := flags.Parse(flagsFirst(args[1:], map[string]bool{"-all": true, "--all": true})); err != nil {
		return exitUsage
	}
	switch {
	case flags.NArg() > 1:
		fmt.Fprintln(stderr, "mn check now: at most one node id or domain")
		return exitUsage
	case flags.NArg() == 1:
		if *all {
			fmt.Fprintln(stderr, "mn check now: either --all or one node, not both")
			return exitUsage
		}
		// One node is exactly `mn uv check now <node>`, including its output.
		return runCheck(args, stdout, stderr)
	}

	client, err := adminClient(*configPath, *socket, *token)
	if err != nil {
		fmt.Fprintf(stderr, "mn check now: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var nodes []admin.NodeView
	if err := client.Call(callCtx, "uv.list", nil, &nodes); err != nil {
		fmt.Fprintf(stderr, "mn check now: %v\n", err)
		return exitFailure
	}

	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "DOMAIN\tEVENT\tSTATE\tATTEMPTS\tREASON")
	checked, failed := 0, false
	for _, node := range nodes {
		if !scheduledStates[node.State] {
			continue
		}
		var result struct {
			Event    string `json:"event"`
			State    string `json:"state"`
			Reason   string `json:"reason"`
			Attempts int    `json:"attempts"`
		}
		if err := client.Call(callCtx, "check.now", map[string]string{"id": node.ID}, &result); err != nil {
			// A cycle the server could not run is not a node failure; keep going and report it.
			fmt.Fprintf(stderr, "mn check now: %s (%s): %v\n", node.Domain, node.ID, err)
			failed = true
			continue
		}
		checked++
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\n", node.Domain, result.Event, result.State, result.Attempts, result.Reason)
	}
	if err := table.Flush(); err != nil {
		fmt.Fprintf(stderr, "mn check now: %v\n", err)
		return exitFailure
	}
	if checked == 0 && !failed {
		fmt.Fprintln(stdout, "no nodes to check")
	}
	if failed {
		return exitFailure
	}
	return exitOK
}
