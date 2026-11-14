package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// jsonStatus is the machine-readable half of `termit-uv status`. It carries fingerprints, never secrets.
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

	Enrolled      bool           `json:"enrolled"`
	EnrolledAt    *time.Time     `json:"enrolled_at,omitempty"`
	Subscription  string         `json:"subscription_url,omitempty"`
	Peers         []jsonPeer     `json:"peers,omitempty"`
	LastSyncAt    *time.Time     `json:"last_sync_at,omitempty"`
	NextSyncAt    *time.Time     `json:"next_sync_at,omitempty"`
	LastSyncState string         `json:"last_sync_result,omitempty"`
	SyncFailures  int            `json:"sync_failures,omitempty"`
	SelfCheck     *jsonSelfCheck `json:"self_check,omitempty"`
	Warp          jsonWarpStatus `json:"warp"`
}

// jsonWarpStatus is the egress as the last self-check saw it. It is a report, not a live probe:
// `termit-uv warp status` is the command that fetches through the tunnel.
type jsonWarpStatus struct {
	Mode   string `json:"mode"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// jsonPeer is one peer as the server last described it. The pair credential is not printed.
type jsonPeer struct {
	ID       string `json:"id"`
	Domain   string `json:"domain"`
	State    string `json:"state"`
	Degraded bool   `json:"degraded,omitempty"`
}

// jsonSelfCheck is the last local check, with the item names that failed or could not run.
type jsonSelfCheck struct {
	At         time.Time           `json:"at"`
	OK         bool                `json:"ok"`
	ErrorClass string              `json:"error_class,omitempty"`
	Items      []jsonSelfCheckItem `json:"items,omitempty"`
}

type jsonSelfCheckItem struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Unknown bool   `json:"unknown,omitempty"`
	Detail  string `json:"detail,omitempty"`
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
		fmt.Fprintf(a.stderr, "termit-uv status: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", *configFlag)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv status: %v\n", err)
		return exitFailure
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if errors.Is(err, state.ErrNotInstalled) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no state at %s\n", cfg.Paths.StatePath())
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv status: %v\n", err)
		return exitFailure
	}
	journal, err := state.LoadJournal(cfg.Paths.JournalPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv status: %v\n", err)
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
		out.Certificate = describeCertificate(node.Certificate.NotAfter, a.now())
	}
	if node.Credentials != nil && node.Credentials.AgentKey != "" {
		out.Enrolled = true
		out.Subscription = node.Credentials.SubscriptionURL
		if !node.Credentials.EnrolledAt.IsZero() {
			enrolledAt := node.Credentials.EnrolledAt
			out.EnrolledAt = &enrolledAt
		}
	}
	for _, peer := range node.Peers {
		out.Peers = append(out.Peers, jsonPeer{
			ID: peer.ID, Domain: peer.Domain, State: peer.State, Degraded: peer.Degraded,
		})
	}
	if !node.Schedule.LastSyncAt.IsZero() {
		last := node.Schedule.LastSyncAt
		out.LastSyncAt = &last
	}
	if !node.Schedule.NextSyncAt.IsZero() {
		next := node.Schedule.NextSyncAt
		out.NextSyncAt = &next
	}
	out.LastSyncState = node.Schedule.LastResult
	out.SyncFailures = node.Schedule.Failures
	if node.LastSelfCheck != nil {
		self := &jsonSelfCheck{
			At:         node.LastSelfCheck.At,
			OK:         node.LastSelfCheck.OK,
			ErrorClass: node.LastSelfCheck.ErrorClass(),
		}
		for _, item := range node.LastSelfCheck.Items {
			self.Items = append(self.Items, jsonSelfCheckItem{
				Name: item.Name, OK: item.OK, Unknown: item.Unknown, Detail: item.Detail,
			})
		}
		out.SelfCheck = self
	}
	out.Warp = describeWarp(cfg, node.LastSelfCheck)
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
			fmt.Fprintf(a.stderr, "termit-uv status: %v\n", err)
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
	if out.Enrolled {
		enrolled := "enrolled"
		if out.EnrolledAt != nil {
			enrolled += " " + out.EnrolledAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(a.stdout, "management:   %s\n", enrolled)
		if out.Subscription != "" {
			fmt.Fprintf(a.stdout, "subscribe:    %s\n", out.Subscription)
		}
	} else {
		fmt.Fprintln(a.stdout, "management:   not enrolled")
	}
	if len(node.Peers) > 0 {
		domains := make([]string, 0, len(node.Peers))
		for _, peer := range node.Peers {
			label := peer.Domain
			if peer.Degraded {
				label += " (degraded)"
			}
			domains = append(domains, label)
		}
		fmt.Fprintf(a.stdout, "peers:        %d (%s)\n", len(node.Peers), strings.Join(domains, ", "))
	} else if out.Enrolled {
		fmt.Fprintln(a.stdout, "peers:        none")
	}
	if out.LastSyncAt != nil {
		fmt.Fprintf(a.stdout, "last sync:    %s %s\n", orDefault(out.LastSyncState, "unknown"),
			out.LastSyncAt.UTC().Format(time.RFC3339))
	}
	if out.NextSyncAt != nil {
		fmt.Fprintf(a.stdout, "next sync:    %s\n", out.NextSyncAt.UTC().Format(time.RFC3339))
	}
	if out.Warp.Mode != "" {
		// "off, off" is noise; a mode that is its own state gets one word.
		line := out.Warp.Mode
		if out.Warp.State != out.Warp.Mode {
			line += ", " + out.Warp.State
		}
		if out.Warp.Detail != "" {
			line += " (" + out.Warp.Detail + ")"
		}
		fmt.Fprintf(a.stdout, "warp:         %s\n", line)
	}
	if out.SelfCheck != nil {
		state := "ok"
		if !out.SelfCheck.OK {
			state = "failed (" + out.SelfCheck.ErrorClass + ")"
		}
		fmt.Fprintf(a.stdout, "self-check:   %s %s\n", state, out.SelfCheck.At.UTC().Format(time.RFC3339))
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

// describeWarp says where proxied traffic leaves. WARP is on by default, so "direct" is the line that
// tells an operator the address the node presents is its own.
func describeWarp(cfg *config.Config, report *state.SelfCheckReport) jsonWarpStatus {
	out := jsonWarpStatus{Mode: cfg.Warp.Mode}
	if !cfg.Warp.UsesWarp() {
		out.State = "off"
		return out
	}
	// The host fact comes first: a container without a tun device reports an honest "unavailable"
	// rather than the "on" a converged self-check item would otherwise suggest.
	if err := (warp.Host{}).Check(); err != nil {
		out.State = "unavailable"
		out.Detail = err.Error()
		return out
	}
	out.State = "unknown"
	if report == nil {
		out.Detail = "no self-check has run yet"
		return out
	}
	for _, item := range report.Items {
		if item.Name != "warp" {
			continue
		}
		out.Detail = item.Detail
		switch {
		case item.Unknown:
			out.State = "unknown"
		case item.OK:
			out.State = "on"
		case cfg.Warp.FailOpenEnabled():
			out.State = "direct"
		default:
			out.State = "blocked"
		}
	}
	return out
}

// describeCertificate says when the certificate expires and how long that is, so an operator does not
// have to subtract dates to notice a renewal that stopped working.
func describeCertificate(notAfter, now time.Time) string {
	left := notAfter.Sub(now)
	days := int(left.Hours() / 24)
	state := "expires " + notAfter.UTC().Format(time.RFC3339)
	if days >= 0 {
		return fmt.Sprintf("%s (in %d days)", state, days)
	}
	return fmt.Sprintf("%s (expired %d days ago)", state, -days)
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
