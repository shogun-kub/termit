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
	flags, configPath, socket, token, tokenFile := uvFlags("termit-mn status", stderr)
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn status: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn status: %v\n", err)
		return exitFailure
	}
	client, err := adminClient(*configPath, *socket, *token, *tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn status: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var nodes []admin.NodeView
	if err := client.Call(callCtx, "uv.list", nil, &nodes); err != nil {
		fmt.Fprintf(stderr, "termit-mn status: %v\n", err)
		return exitFailure
	}
	// The server block is best effort: an older server does not have the operation, and a status that
	// refuses to print the node table over one missing line would be worse than the missing line.
	var server admin.ServerState
	hasServer := client.Call(callCtx, "server.state", nil, &server) == nil
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
	fmt.Fprintf(stdout, "api:      %s\n", cfg.APIPrefix)
	fmt.Fprintf(stdout, "decoy:    %s\n", cfg.Decoy.Template)
	fmt.Fprintf(stdout, "version:  %s\n", version.String("termit-mn"))
	if hasServer {
		if !server.StartedAt.IsZero() {
			fmt.Fprintf(stdout, "uptime:   %s (started %s)\n",
				humanUptime(time.Duration(server.UptimeSeconds)*time.Second), checkTime(&server.StartedAt))
		}
		fmt.Fprintf(stdout, "database: %s (%s, schema %d)\n",
			server.Database.Path, humanBytes(server.Database.Bytes), server.SchemaVersion)
		fmt.Fprintf(stdout, "prober:   %s\n", describeProber(server.Prober.Up, server.Prober.Detail))
		fmt.Fprintf(stdout, "queue:    %s\n", describeQueue(server))
		if server.Certificate != nil {
			fmt.Fprintf(stdout, "cert:     %s (in %d days)\n",
				server.Certificate.NotAfter.UTC().Format(time.RFC3339), server.Certificate.DaysUntil)
		}
	}
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
		fmt.Fprintf(stderr, "termit-mn status: %v\n", err)
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

// humanUptime renders a process's age in the two units that matter at that scale.
func humanUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	minutes := int(d % time.Hour / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// humanBytes is a file size an operator can read without counting digits. Binary units, because the
// number comes from the filesystem.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

// describeProber says whether checks can run. "0 due" over a broken prober is the failure this line
// exists to prevent: it is the difference between "nothing to do" and "nothing is being done".
func describeProber(up bool, detail string) string {
	if up {
		if detail == "" {
			return "ready"
		}
		return "ready (" + detail + ")"
	}
	if detail == "" {
		detail = "no probe worker"
	}
	return "unavailable — every check reports unknown (" + detail + ")"
}

// describeQueue renders the scheduler's backlog: how many nodes are overdue, how late the oldest one
// is, and when the next check is due.
func describeQueue(server admin.ServerState) string {
	next := "nothing scheduled"
	if server.Queue.NextDueAt != nil {
		next = "next " + checkTime(server.Queue.NextDueAt) + " UTC"
	}
	switch {
	case server.Queue.Due == 0:
		return "0 due, " + next
	case server.Queue.OldestDueAt == nil:
		// Due but never scheduled: freshly enrolled nodes waiting for their first cycle.
		return fmt.Sprintf("%d due (never checked), %s", server.Queue.Due, next)
	default:
		return fmt.Sprintf("%d due, oldest %s late, %s",
			server.Queue.Due, humanUptime(time.Duration(server.Queue.OldestDueSeconds)*time.Second), next)
	}
}

// runServerCheck runs one cycle for every node the scheduler would probe. With a node argument it is
// the same command as `termit-mn uv check now <node>`; without one it is the "check the whole mesh now"
// operation, which is what an operator reaches for after fixing DNS or a firewall.
func runServerCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "now" {
		fmt.Fprintln(stderr, "termit-mn check: the only subcommand in this build is now")
		return exitUsage
	}
	flags, configPath, socket, token, tokenFile := uvFlags("termit-mn check now", stderr)
	all := flags.Bool("all", false, "check every node the scheduler probes")
	if err := flags.Parse(flagsFirst(args[1:], map[string]bool{"-all": true, "--all": true})); err != nil {
		return flagExit(err)
	}
	switch {
	case flags.NArg() > 1:
		fmt.Fprintln(stderr, "termit-mn check now: at most one node id or domain")
		return exitUsage
	case flags.NArg() == 1:
		if *all {
			fmt.Fprintln(stderr, "termit-mn check now: either --all or one node, not both")
			return exitUsage
		}
		// One node is exactly `termit-mn uv check now <node>`, including its output.
		return runCheck(args, stdout, stderr)
	}

	client, err := adminClient(*configPath, *socket, *token, *tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn check now: %v\n", err)
		return exitFailure
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var nodes []admin.NodeView
	if err := client.Call(callCtx, "uv.list", nil, &nodes); err != nil {
		fmt.Fprintf(stderr, "termit-mn check now: %v\n", err)
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
			fmt.Fprintf(stderr, "termit-mn check now: %s (%s): %v\n", node.Domain, node.ID, err)
			failed = true
			continue
		}
		checked++
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\n", node.Domain, result.Event, result.State, result.Attempts, result.Reason)
	}
	if err := table.Flush(); err != nil {
		fmt.Fprintf(stderr, "termit-mn check now: %v\n", err)
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
