package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/hardencmd"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
	"github.com/shogun-kub/termit/internal/version"
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

	Enrolled     bool       `json:"enrolled"`
	EnrolledAt   *time.Time `json:"enrolled_at,omitempty"`
	Subscription string     `json:"subscription_url,omitempty"`
	// OptionsPage is the owner's private page of client links, when one has been rendered.
	OptionsPage   string     `json:"options_page,omitempty"`
	Peers         []jsonPeer `json:"peers,omitempty"`
	LastSyncAt    *time.Time `json:"last_sync_at,omitempty"`
	NextSyncAt    *time.Time `json:"next_sync_at,omitempty"`
	LastSyncState string     `json:"last_sync_result,omitempty"`
	ServerVersion string     `json:"server_version,omitempty"`
	// MinAgentVersion is the oldest build the server declared it supports. The text output says whether
	// this build is below it; the JSON gives the number and leaves the comparison to the reader.
	MinAgentVersion string         `json:"min_agent_version,omitempty"`
	SyncFailures    int            `json:"sync_failures,omitempty"`
	SelfCheck       *jsonSelfCheck `json:"self_check,omitempty"`
	Warp            jsonWarpStatus `json:"warp"`
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
	if node.Options.Token != "" {
		out.OptionsPage = "https://" + cfg.Domain + "/" + node.Options.Token + "/"
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
	if node.ServerVersion != "" {
		out.ServerVersion = node.ServerVersion
	}
	out.MinAgentVersion = node.MinAgentVersion
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
	if out.OptionsPage != "" {
		fmt.Fprintf(a.stdout, "options:      %s\n", out.OptionsPage)
	}
	if out.ServerVersion != "" {
		fmt.Fprintf(a.stdout, "server:       %s\n", out.ServerVersion)
	}
	if out.MinAgentVersion != "" {
		fmt.Fprintf(a.stdout, "support:      %s\n", describeSupport(version.Version, out.MinAgentVersion))
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
	if bans, err := firewall.LoadBans(cfg.Paths.BansPath()); err == nil {
		// The guard's record, not the kernel's: it is what survives a reboot, and the guard refills the
		// kernel from it.
		fmt.Fprintf(a.stdout, "bans:         %s\n", hardencmd.DescribeBans(bans, a.now()))
	}
	// The firewall line appears only when it is the operator's business: the configuration asks for it,
	// or someone applied a table once. A node nobody hardened says nothing about it.
	if line := a.describeFirewallLine(cfg); line != "" {
		fmt.Fprintf(a.stdout, "firewall:     %s\n", line)
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
	// rather than the "on" a converged self-check item would otherwise suggest. It is a fact about the
	// proxy transport only: the tunnel inside the core needs no device.
	if !cfg.Warp.UsesWireGuard() {
		if err := (warp.Host{}).Check(); err != nil {
			out.State = "unavailable"
			out.Detail = err.Error()
			return out
		}
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

// describeSupport answers the one question the floor raises: is this node older than its server still
// supports? A build that cannot be ordered against a release (a development build) says so instead.
func describeSupport(build, minimum string) string {
	below, comparable := version.Below(build, minimum)
	switch {
	case !comparable:
		return minimum + " (this build cannot be compared with a release)"
	case below:
		return "below the server's minimum " + minimum + " — run `termit-uv upgrade`"
	}
	return "meets the server's minimum " + minimum
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

// describeFirewallLine is the `status` view of the host firewall: the record in writing, checked against
// the kernel. It says nothing at all on a node whose configuration never asked for a table and where none
// was ever applied — that is the common case and it does not need a line.
func (a *application) describeFirewallLine(cfg *config.Config) string {
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil {
		return "the record is unreadable: " + err.Error()
	}
	if !cfg.Security.Firewall.Enabled && applied == nil {
		return ""
	}
	present, err := a.nftApplier().Present(context.Background(), firewall.NodeTable)
	if err != nil {
		// Not root, or no nft: the record is still worth reporting, and "unknown" is honest about what
		// could not be checked.
		if applied != nil && applied.Confirmed() {
			return "unknown (the kernel cannot be asked: " + err.Error() + ")"
		}
		return "unknown (the kernel cannot be asked: " + err.Error() + ")"
	}
	now := a.now()
	switch {
	case present && applied != nil && applied.Pending(now) && now.Before(applied.Deadline):
		return fmt.Sprintf("on, not confirmed — run `termit-uv firewall confirm` within %s",
			applied.Deadline.Sub(now).Round(time.Second))
	case present && applied != nil && applied.Confirmed():
		return "on, confirmed"
	case present:
		return "unknown (a table is applied but nothing recorded it; `termit-uv firewall off` then `on` puts it back in writing)"
	case cfg.Security.Firewall.Enabled:
		return "off (the configuration asks for it: run `termit-uv firewall on`)"
	case applied != nil && !applied.RemovedAt.IsZero():
		return "off since " + humanAge(now, applied.RemovedAt)
	}
	return "off (it was applied once and the kernel has no table: `termit-uv firewall on` puts it back)"
}

// humanAge renders an age the way a person reads it: 40s, 12m, 2h, 2h30m.
func humanAge(now, then time.Time) string {
	delta := now.Sub(then)
	if delta < 0 {
		return "in " + ageSpan(-delta)
	}
	return ageSpan(delta) + " ago"
}

// ageSpan is the age without "ago". A trailing zero minute is dropped, because "2h0m" reads like a
// timestamp rather than a duration.
func ageSpan(delta time.Duration) string {
	if delta < time.Minute {
		return delta.Round(time.Second).String()
	}
	delta = delta.Round(time.Minute)
	hours, minutes := int(delta/time.Hour), int(delta%time.Hour/time.Minute)
	switch {
	case hours == 0:
		return fmt.Sprintf("%dm", minutes)
	case minutes == 0:
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, minutes)
}
