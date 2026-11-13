package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/netcounters"
)

// runNet reports what this node's own kernel says about the connections it serves. It opens no
// sockets and contacts no host: the counters answer the same question, and a node must not phone a
// third party to find out whether its connections work.
func (a *application) runNet(args []string) int {
	flags := a.newFlagSet("net", `Samples the kernel's TCP counters and reports how connections to this
node behave. It separates "this node dropped the connection" from "the answer never reached the
peer", which is the difference between a broken node and a broken path. Nothing is changed, and no
host is contacted.`)
	seconds := flags.Int("seconds", 30, "how long to sample, in seconds (5-300)")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv net: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *seconds < 5 || *seconds > 300 {
		fmt.Fprintln(a.stderr, "termit-uv net: --seconds must be between 5 and 300")
		return exitUsage
	}

	before, err := netcounters.Read("/proc")
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv net: %v\n", err)
		return exitFailure
	}
	started := a.now()
	time.Sleep(time.Duration(*seconds) * time.Second)
	after, err := netcounters.Read("/proc")
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv net: %v\n", err)
		return exitFailure
	}
	delta := netcounters.Difference(before, after, a.now().Sub(started))

	verdict, detail, hint := netcounters.Judge(delta)
	checks := []install.Check{{
		Name:   "inbound",
		Status: statusOf(verdict),
		Detail: detail,
		Hint:   hint,
	}}
	// The raw counters are useful next to the verdict, because they are what an operator pastes
	// into a ticket to a provider.
	checks = append(checks, install.Check{
		Name:   "counters",
		Status: install.StatusOK,
		Detail: fmt.Sprintf("listen drops +%d, SYNs retransmitted +%d, RTOs +%d, "+
			"inbound completed +%d, outbound started +%d over %s",
			delta.ListenDrops, delta.SynRetrans, delta.Timeouts,
			delta.PassiveOpens, delta.ActiveOpens, delta.Elapsed.Round(time.Second)),
	})

	if *jsonOut {
		out := make([]jsonCheck, 0, len(checks))
		for _, check := range checks {
			out = append(out, jsonCheck{Name: check.Name, Status: string(check.Status), Detail: check.Detail, Hint: check.Hint})
		}
		body, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv net: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, string(body))
	} else {
		a.printChecks(checks)
		fmt.Fprintf(a.stdout, "\n%s\n", summarizeChecks(checks))
	}

	if verdict == netcounters.VerdictFail {
		return exitFailure
	}
	return exitOK
}

// statusOf maps a verdict to the status a check reports. An unmeasured sample is a skip, never a
// pass: silence must not look like health.
func statusOf(verdict netcounters.Verdict) install.CheckStatus {
	switch verdict {
	case netcounters.VerdictOK:
		return install.StatusOK
	case netcounters.VerdictWarn:
		return install.StatusWarn
	case netcounters.VerdictFail:
		return install.StatusFail
	default:
		return install.StatusSkip
	}
}
