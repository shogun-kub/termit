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
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

func (a *application) runDoctor(args []string) int {
	flags := a.newFlagSet("doctor", `Checks the host and the node. For the host: root, Debian release,
systemd, architecture, clock, free :80/:443, DNS, the ACME directory, TUN, disk and memory. For an
installed node: state, services, certificate, decoy and proxy config. It changes nothing.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv doctor: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	checks := a.diagnose(*configFlag)
	ok := true
	for _, check := range checks {
		if check.Status == install.StatusFail {
			ok = false
		}
	}

	if *jsonOut {
		out := make([]jsonCheck, 0, len(checks))
		for _, check := range checks {
			out = append(out, jsonCheck{Name: check.Name, Status: string(check.Status), Detail: check.Detail, Hint: check.Hint})
		}
		body, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "uv doctor: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, string(body))
	} else {
		a.printChecks(checks)
		fmt.Fprintf(a.stdout, "\n%s\n", summarizeChecks(checks))
	}

	if !ok {
		return exitFailure
	}
	return exitOK
}

// diagnose runs the configuration check and the host checks. A broken configuration is reported as a
// failed check instead of stopping the run, because doctor is what the operator runs when something
// is wrong.
func (a *application) diagnose(path string) []install.Check {
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
	})
	checks := append([]install.Check{configCheck}, report.Checks...)
	// The host half says whether this machine can run a node; the node half says whether it is
	// working. doctor is what an operator reaches for when a node misbehaves, so it answers both.
	checks = append(checks, install.Diagnose(ctx, install.DiagnoseOptions{
		Config: cfg,
		Exec:   a.exec,
		Now:    a.now,
	})...)
	return checks
}
