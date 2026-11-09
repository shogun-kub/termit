package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/state"
)

// jsonStatus is the machine-readable half of `uv status`. It carries fingerprints, never secrets.
type jsonStatus struct {
	Domain       string       `json:"domain"`
	Installed    bool         `json:"installed"`
	CreatedAt    time.Time    `json:"created_at"`
	Identity     string       `json:"identity_fingerprint"`
	ProxyPath    string       `json:"proxy_path_fingerprint"`
	Decoy        string       `json:"decoy_template"`
	DecoySeed    string       `json:"decoy_seed_fingerprint"`
	XrayVersion  string       `json:"xray_version"`
	XrayPath     string       `json:"xray_path"`
	XraySHA256   string       `json:"xray_sha256"`
	Certificate  string       `json:"certificate,omitempty"`
	LastRun      string       `json:"last_run,omitempty"`
	LastRunAt    *time.Time   `json:"last_run_at,omitempty"`
	LastRunSteps []stepStatus `json:"last_run_steps,omitempty"`
}

// stepStatus is one journaled step.
type stepStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (a *application) runStatus(args []string) int {
	flags := a.newFlagSet("status", `Shows the node's domain, identity fingerprints, installed core and the result of
the last install run. Secrets are shown as sha256 prefixes only.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the status as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv status: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", *configFlag)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "uv status: %v\n", err)
		return exitFailure
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if errors.Is(err, state.ErrNotInstalled) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no state at %s\n", cfg.Paths.StatePath())
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "uv status: %v\n", err)
		return exitFailure
	}
	journal, err := state.LoadJournal(cfg.Paths.JournalPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "uv status: %v\n", err)
		return exitFailure
	}

	out := jsonStatus{
		Domain:      node.Domain,
		Installed:   node.Installed(),
		CreatedAt:   node.CreatedAt,
		Identity:    state.Fingerprint(node.Identity.SelfUUID),
		ProxyPath:   state.Fingerprint(node.Identity.XHTTPPath),
		Decoy:       node.Decoy.Template,
		DecoySeed:   state.Fingerprint(node.Decoy.Seed),
		XrayVersion: node.Xray.Version,
		XrayPath:    node.Xray.Path,
		XraySHA256:  node.Xray.SHA256,
	}
	if node.Certificate != nil {
		out.Certificate = "expires " + node.Certificate.NotAfter.Format(time.RFC3339)
	}
	if last := journal.Last(); last != nil {
		out.LastRun = last.Result
		out.LastRunAt = &last.StartedAt
		for _, step := range last.Steps {
			out.LastRunSteps = append(out.LastRunSteps, stepStatus{Name: step.Name, Status: step.Status})
		}
	}

	if *jsonOut {
		body, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "uv status: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, string(body))
		return exitOK
	}

	fmt.Fprintf(a.stdout, "domain:       %s\n", node.Domain)
	fmt.Fprintf(a.stdout, "installed:    %s\n", node.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(a.stdout, "identity:     %s\n", orMissing(out.Identity))
	fmt.Fprintf(a.stdout, "proxy path:   %s\n", orMissing(out.ProxyPath))
	fmt.Fprintf(a.stdout, "decoy:        %s\n", describeDecoy(node.Decoy))
	if len(node.LocalUsers) > 0 {
		labels := make([]string, 0, len(node.LocalUsers))
		for _, user := range node.LocalUsers {
			labels = append(labels, user.Label)
		}
		fmt.Fprintf(a.stdout, "local users:  %d (%s)\n", len(node.LocalUsers), strings.Join(labels, ", "))
	}
	if node.Xray.Version != "" {
		fmt.Fprintf(a.stdout, "core:         xray %s at %s\n", node.Xray.Version, node.Xray.Path)
	} else {
		fmt.Fprintln(a.stdout, "core:         not installed")
	}
	if out.Certificate != "" {
		fmt.Fprintf(a.stdout, "certificate:  %s\n", out.Certificate)
	} else {
		fmt.Fprintln(a.stdout, "certificate:  not issued")
	}
	if last := journal.Last(); last != nil {
		fmt.Fprintf(a.stdout, "last run:     %s, %s\n", last.Result, last.StartedAt.Format(time.RFC3339))
		steps := make([]string, 0, len(last.Steps))
		for _, step := range last.Steps {
			steps = append(steps, step.Name+" "+step.Status)
		}
		fmt.Fprintf(a.stdout, "steps:        %s\n", strings.Join(steps, ", "))
	}
	fmt.Fprintf(a.stdout, "state:        %s\n", cfg.Paths.StatePath())
	return exitOK
}

func describeDecoy(decoy state.Decoy) string {
	if decoy.Template == "" {
		return "not generated"
	}
	if decoy.Seed == "" {
		return decoy.Template
	}
	return fmt.Sprintf("%s (seed %s)", decoy.Template, state.Fingerprint(decoy.Seed))
}

func orMissing(value string) string {
	if value == "" {
		return "not generated"
	}
	return value
}
