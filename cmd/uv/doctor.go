package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
)

// jsonCheck is the JSON shape of a preflight check.
type jsonCheck struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Group names, in the order an operator reads them: the machine, the node on it, the tunnel the node
// sends its proxied traffic through.
const (
	groupHost   = "host"
	groupNode   = "node"
	groupTunnel = "tunnel"
)

// checkGroups is the report split the way it is read. Failures and warnings stay where they were found, so
// a bad tunnel does not read like a broken host.
type checkGroups struct {
	Host   []install.Check
	Node   []install.Check
	Tunnel []install.Check
}

func (g checkGroups) all() []install.Check {
	return append(append(append([]install.Check{}, g.Host...), g.Node...), g.Tunnel...)
}

func (a *application) runDoctor(args []string) int {
	flags := a.newFlagSet("doctor", `Checks the host, the node and its tunnel. For the host: root, Debian
release, systemd, architecture, clock, free :80/:443, DNS, the ACME directory, TUN, disk and memory. For an
installed node: state, services, certificate, decoy and proxy config. For the tunnel: the WARP transport.
It changes nothing.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv doctor: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	groups := a.diagnose(*configFlag)
	checks := groups.all()

	if *jsonOut {
		out := make([]jsonCheck, 0, len(checks))
		for _, check := range checks {
			out = append(out, jsonCheck{Name: check.Name, Group: checkGroup(check.Name), Status: string(check.Status),
				Detail: check.Detail, Hint: check.Hint})
		}
		body, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv doctor: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, string(body))
	} else {
		a.printGroups(groups)
		fmt.Fprintf(a.stdout, "%s\n", verdict(checks))
	}

	for _, check := range checks {
		if check.Status == install.StatusFail {
			return exitFailure
		}
	}
	return exitOK
}

// diagnose runs the configuration check and the host checks, then the node's, and sorts them into the
// three groups the report draws. A broken configuration is reported as a failed check instead of stopping
// the run, because doctor is what the operator runs when something is wrong.
func (a *application) diagnose(path string) checkGroups {
	cfg := config.Default()
	configCheck := install.Check{Name: "config", Status: install.StatusSkip, Detail: "no configuration at " + path}
	if _, err := os.Stat(path); err == nil {
		loaded, loadErr := config.Load(path)
		if loadErr != nil {
			configCheck = install.Check{Name: "config", Status: install.StatusFail, Detail: loadErr.Error()}
		} else {
			cfg = loaded
			configCheck = install.Check{Name: "config", Status: install.StatusOK, Detail: path}
		}
	}

	ctx, cancel := a.context()
	defer cancel()
	report := install.RunPreflight(ctx, install.PreflightOptions{
		Config: cfg,
		Exec:   a.exec,
		HTTP:   a.http,
		Arch:   runtime.GOARCH,
		Now:    a.now,
		EUID:   a.euid,
	})
	nodeChecks := append([]install.Check{configCheck}, install.Diagnose(ctx, install.DiagnoseOptions{
		Config: cfg,
		Exec:   a.exec,
		Now:    a.now,
	})...)

	var groups checkGroups
	for _, check := range report.Checks {
		switch checkGroup(check.Name) {
		case groupTunnel:
			groups.Tunnel = append(groups.Tunnel, check)
		case groupNode:
			groups.Node = append(groups.Node, check)
		default:
			groups.Host = append(groups.Host, check)
		}
	}
	for _, check := range nodeChecks {
		if checkGroup(check.Name) == groupTunnel {
			groups.Tunnel = append(groups.Tunnel, check)
			continue
		}
		groups.Node = append(groups.Node, check)
	}
	return groups
}

// checkGroup says which of the three screens a check belongs to. Anything the list does not name is a fact
// about the machine, which is what most of the preflight is.
func checkGroup(name string) string {
	switch name {
	case "config", "state", "support", "services", "certificate", "decoy", "proxy config", "firewall":
		return groupNode
	case "transport", "warp", "warp-egress":
		return groupTunnel
	default:
		return groupHost
	}
}

// printGroups draws the report: every group with its own tally, so the reader can stop at the one that is
// not clean.
func (a *application) printGroups(groups checkGroups) {
	for _, group := range []struct {
		name   string
		checks []install.Check
	}{
		{groupHost, groups.Host},
		{groupNode, groups.Node},
		{groupTunnel, groups.Tunnel},
	} {
		if len(group.checks) == 0 {
			continue
		}
		fmt.Fprintf(a.stdout, "%s\n", group.name)
		a.printChecks(group.checks)
		fmt.Fprintf(a.stdout, "  %s\n\n", summarizeChecks(group.checks))
	}
}

// verdict is the one line the reader takes away: any failure is a problem, warnings alone are a working
// node whose caveats are worth reading, and nothing else is ok.
func verdict(checks []install.Check) string {
	counts := map[install.CheckStatus]int{}
	for _, check := range checks {
		counts[check.Status]++
	}
	switch {
	case counts[install.StatusFail] > 0:
		return fmt.Sprintf("problems found (%s)", summarizeChecks(checks))
	case counts[install.StatusWarn] > 0:
		return fmt.Sprintf("ok with warnings (%s)", summarizeChecks(checks))
	default:
		return "ok"
	}
}
