package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// firewallSummary is the group's help page. It says plainly what the rest of the binary promises: the
// table exists only because someone typed `on`.
const firewallSummary = `The host firewall: one nftables table, applied only when you say so.

  termit-uv firewall            the same as ` + "`firewall status`" + `
  termit-uv firewall on         render and apply the table; --dry-run prints it instead
  termit-uv firewall off        remove the table and leave the host exactly as it was
  termit-uv firewall confirm    keep what ` + "`on`" + ` applied (the rollback timer stands down)
  termit-uv firewall rollback   what the timer runs: remove the table unless it was confirmed
  termit-uv firewall refresh    fetch the network blocklist and load it into the table

` + "`on`" + ` arms a five-minute timer before it touches anything. If you do not confirm, the table is
removed — which is what makes it safe to run over the very connection it might cut. Nothing else in
termit-uv applies or removes it: not init, not sync, not the agent, not a timer.`

// jsonFirewall is the machine-readable firewall report.
type jsonFirewall struct {
	Table       string   `json:"table"`
	Applied     bool     `json:"applied"`
	Confirmed   bool     `json:"confirmed"`
	DefaultDeny bool     `json:"default_deny"`
	Ports       []int    `json:"ports,omitempty"`
	SSHFrom     []string `json:"ssh_from,omitempty"`
	Bans        int      `json:"bans"`
	Blocked     int      `json:"blocked"`
	Scans       int      `json:"scans"`
	Blocklist   int      `json:"blocklist"`
	BlocklistAt string   `json:"blocklist_fetched,omitempty"`
	ConfirmBy   string   `json:"confirm_by,omitempty"`
	Note        string   `json:"note,omitempty"`
}

func (a *application) runFirewall(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "on":
			return a.runFirewallOn(args[1:])
		case "off":
			return a.runFirewallOff(args[1:])
		case "confirm":
			return a.runFirewallConfirm(args[1:])
		case "rollback":
			return a.runFirewallRollback(args[1:])
		case "refresh":
			return a.runFirewallRefresh(args[1:])
		case "status":
			return a.runFirewallStatus(args[1:])
		}
	}
	return a.runFirewallStatus(args)
}

// runFirewallOn renders the table, arms the rollback timer, applies the table and records what it did.
// The order matters: the timer is armed before the table exists, so a firewall that cannot be undone
// is never installed; and the record is written after the apply, so the timer's rollback is a no-op
// when the apply failed.
func (a *application) runFirewallOn(args []string) int {
	flags := a.newFlagSet("firewall on", `Renders this node's table and applies it with nft. The whole ruleset is one atomic
transaction, so a failure leaves the previous rules in force. A five-minute rollback timer is armed
first: if you cannot reach the node to confirm, the table removes itself.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	dryRun := flags.Bool("dry-run", false, "print the ruleset and change nothing")
	sshFrom := flags.String("ssh-from", "", "networks allowed to reach port 22, comma separated (overrides security.firewall.ssh_from)")
	defaultDeny := flags.Bool("default-deny", true, "drop inbound traffic the rules did not accept (overrides security.firewall.default_deny)")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv firewall on: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })

	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall on: %v\n", err)
		return exitFailure
	}
	table, err := cfg.Security.Table(firewall.NodePorts)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall on: %v\n", err)
		return exitFailure
	}
	if given["ssh-from"] {
		prefixes, err := firewall.Prefixes(splitList(*sshFrom))
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv firewall on: --ssh-from: %v\n", err)
			return exitUsage
		}
		table.SSHFrom = prefixes
	}
	if given["default-deny"] {
		table.DefaultDeny = *defaultDeny
	}
	ruleset, err := table.Render()
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall on: %v\n", err)
		return exitUsage
	}

	applier := a.nftApplier()
	if *dryRun {
		fmt.Fprint(a.stdout, string(ruleset))
		if a.isRoot() {
			if err := applier.Check(context.Background(), ruleset); err != nil {
				fmt.Fprintf(a.stderr, "termit-uv firewall on: %v\n", err)
				return exitFailure
			}
			fmt.Fprintln(a.stderr, "the ruleset parses; nothing was applied")
		} else {
			fmt.Fprintln(a.stderr, "not verified with nft: this is not root, so the kernel cannot be asked")
		}
		return exitOK
	}
	if !a.isRoot() {
		fmt.Fprintln(a.stderr, "termit-uv firewall on: this changes the host's firewall, so it needs root")
		return exitFailure
	}

	ctx := context.Background()
	if err := a.armRollback(ctx, cfg, *configFlag); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall on: %v\n", err)
		return exitFailure
	}
	if err := applier.Apply(ctx, ruleset); err != nil {
		// Nothing changed: nft commits the batch or nothing. The timer has nothing to undo.
		_ = a.disarmRollback(ctx, cfg)
		fmt.Fprintf(a.stderr, "termit-uv firewall on: %v\n", err)
		return exitFailure
	}
	now := a.now()
	applied := &firewall.Applied{
		Table:       table.Name,
		Ports:       table.Ports,
		SSHFrom:     prefixStrings(table.SSHFrom),
		DefaultDeny: table.DefaultDeny,
		AppliedAt:   now.UTC(),
		Deadline:    now.Add(service.ConfirmWindow).UTC(),
	}
	if err := firewall.SaveApplied(cfg.Paths.FirewallPath(), applied); err != nil {
		// Without the record the timer cannot know what to undo, so undo it now.
		_ = applier.Remove(ctx, table.Name)
		_ = a.disarmRollback(ctx, cfg)
		fmt.Fprintf(a.stderr, "termit-uv firewall on: %v\n", err)
		return exitFailure
	}

	fmt.Fprintf(a.stdout, "firewall: on, table inet %s, %s, ports %s\n",
		table.Name, denyWords(table.DefaultDeny), portsWords(table.Ports))
	// The table starts with empty sets, so the list in force goes back in before the operator walks away.
	a.loadBlocklistIntoKernel(cfg)
	if len(table.SSHFrom) > 0 {
		fmt.Fprintf(a.stdout, "ssh:      port %d from %s\n", firewall.SSHPort, strings.Join(prefixStrings(table.SSHFrom), ", "))
	}
	fmt.Fprintf(a.stdout, "confirm:  run `termit-uv firewall confirm` within %s, or the table is removed\n",
		service.ConfirmWindow)
	fmt.Fprintf(a.stdout, "now:      check that SSH and 443 still answer before you close this session\n")
	return exitOK
}

// runFirewallOff removes the table and disarms the rollback timer. It is the way back from every
// mistake, so it does not ask.
func (a *application) runFirewallOff(args []string) int {
	flags := a.newFlagSet("firewall off", "Removes the whole table and the rollback timer. The host is left as it was before `on`.")
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	dryRun := flags.Bool("dry-run", false, "say what would be removed and change nothing")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall off: %v\n", err)
		return exitFailure
	}
	if *dryRun {
		fmt.Fprintf(a.stdout, "would remove table inet %s and the rollback timer\n", firewall.NodeTable)
		return exitOK
	}
	if !a.isRoot() {
		fmt.Fprintln(a.stderr, "termit-uv firewall off: this changes the host's firewall, so it needs root")
		return exitFailure
	}
	ctx := context.Background()
	if err := a.nftApplier().Remove(ctx, firewall.NodeTable); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall off: %v\n", err)
		return exitFailure
	}
	if err := a.disarmRollback(ctx, cfg); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall off: %v\n", err)
		return exitFailure
	}
	if err := a.recordRemoved(cfg, a.now()); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall off: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "firewall: off, table inet %s removed and the rollback timer stopped\n", firewall.NodeTable)
	return exitOK
}

// runFirewallConfirm keeps what `on` applied by disarming the timer. It changes no rule, so it works
// from the session the firewall just kept alive.
func (a *application) runFirewallConfirm(args []string) int {
	flags := a.newFlagSet("firewall confirm", "Keeps the rules `firewall on` applied by standing the rollback timer down. Changes no rule.")
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall confirm: %v\n", err)
		return exitFailure
	}
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall confirm: %v\n", err)
		return exitFailure
	}
	now := a.now()
	if !applied.Pending(now) {
		if applied != nil && applied.Confirmed() {
			fmt.Fprintln(a.stdout, "firewall: already confirmed; the rules stay")
			return exitOK
		}
		fmt.Fprintln(a.stderr, "termit-uv firewall confirm: no applied firewall to confirm; run `termit-uv firewall on`")
		return exitFailure
	}
	ctx := context.Background()
	if err := a.disarmRollback(ctx, cfg); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall confirm: %v\n", err)
		return exitFailure
	}
	applied.ConfirmedAt = now.UTC()
	applied.Deadline = time.Time{}
	if err := firewall.SaveApplied(cfg.Paths.FirewallPath(), applied); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall confirm: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "firewall: confirmed, table inet %s stays\n", applied.Table)
	return exitOK
}

// runFirewallRollback is what the timer runs. It removes the table unless the apply was confirmed, and
// says so on stderr, which lands in the journal of the oneshot.
func (a *application) runFirewallRollback(args []string) int {
	flags := a.newFlagSet("firewall rollback", "Runs from the rollback timer: removes the table unless `firewall confirm` ran. Not for interactive use.")
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall rollback: %v\n", err)
		return exitFailure
	}
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall rollback: %v\n", err)
		return exitFailure
	}
	if !applied.Pending(a.now()) {
		// Confirmed, already removed, or never applied: nothing to undo.
		_ = a.disarmRollback(context.Background(), cfg)
		return exitOK
	}
	ctx := context.Background()
	if err := a.nftApplier().Remove(ctx, firewall.NodeTable); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall rollback: %v\n", err)
		return exitFailure
	}
	_ = a.disarmRollback(ctx, cfg)
	if err := a.recordRemoved(cfg, a.now()); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall rollback: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stderr, "firewall: removed table inet %s — it was applied at %s and never confirmed\n",
		applied.Table, applied.AppliedAt.Format(time.RFC3339))
	return exitOK
}

// runFirewallStatus reports what is on the host, what was applied, and where the configuration
// disagrees with either.
func (a *application) runFirewallStatus(args []string) int {
	flags := a.newFlagSet("firewall [status]", firewallSummary)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv firewall: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall: %v\n", err)
		return exitFailure
	}
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall: %v\n", err)
		return exitFailure
	}
	applier := a.nftApplier()
	ctx := context.Background()
	present, err := applier.Present(ctx, firewall.NodeTable)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall: %v\n", err)
		return exitFailure
	}
	counts, err := applier.Counts(ctx, firewall.NodeTable)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv firewall: %v\n", err)
		return exitFailure
	}
	now := a.now()

	report := jsonFirewall{
		Table:   firewall.NodeTable,
		Applied: present,
		Bans:    counts["banned4"] + counts["banned6"],
		Blocked: counts["blocked4"] + counts["blocked6"],
		Scans:   counts["scan4"] + counts["scan6"],
	}
	if applied != nil {
		report.Ports = applied.Ports
		report.SSHFrom = applied.SSHFrom
	}
	if present && applied != nil {
		report.DefaultDeny = applied.DefaultDeny
	}
	report.Confirmed = applied.Confirmed()
	if applied.Pending(now) && now.Before(applied.Deadline) {
		report.ConfirmBy = applied.Deadline.Format(time.RFC3339)
	}
	// The list in force, from the file rather than the kernel: the kernel's sets are a copy that a
	// reboot or a failed load can empty, and the file is what the next refresh restores.
	if prefixes, fetchedAt, err := firewall.LoadBlocklist(cfg.Paths.BlocklistPath()); err == nil {
		report.Blocklist = len(prefixes)
		if !fetchedAt.IsZero() {
			report.BlocklistAt = humanSince(now, fetchedAt)
		}
	}
	var notes []string
	switch {
	case present && applied == nil:
		notes = append(notes, "a table is applied but nothing here recorded it; `firewall off` then `on` puts it back in writing")
	case present && !cfg.Security.Firewall.Enabled:
		notes = append(notes, "security.firewall.enabled is off in the configuration, but a table is applied")
	case !present && cfg.Security.Firewall.Enabled:
		notes = append(notes, "the configuration asks for the firewall, but no table is applied: run `termit-uv firewall on`")
	}
	if present && applied != nil && applied.DefaultDeny != cfg.Security.Firewall.DenyOthers() {
		notes = append(notes, "default_deny differs from the configuration; `firewall on` applies what the configuration says")
	}
	report.Note = strings.Join(notes, "; ")

	if *jsonOut {
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv firewall: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, string(body))
		return exitOK
	}
	fmt.Fprintf(a.stdout, "firewall:   %s\n", describeFirewall(report, applied, now))
	if present {
		deny := "default deny"
		if !report.DefaultDeny {
			deny = "other inbound allowed"
		}
		fmt.Fprintf(a.stdout, "table:      inet %s, %s, ports %s\n", firewall.NodeTable, deny, portsWords(report.Ports))
		if len(report.SSHFrom) > 0 {
			fmt.Fprintf(a.stdout, "ssh:        port %d from %s\n", firewall.SSHPort, strings.Join(report.SSHFrom, ", "))
		} else {
			fmt.Fprintf(a.stdout, "ssh:        port %d from anywhere\n", firewall.SSHPort)
		}
		fmt.Fprintf(a.stdout, "elements:   %s\n", firewall.Describe(counts))
	}
	if report.Blocklist > 0 {
		line := fmt.Sprintf("blocklist:  %d networks", report.Blocklist)
		if report.BlocklistAt != "" {
			line += ", fetched " + report.BlocklistAt
		}
		if len(counts) > 0 && counts["blocked4"]+counts["blocked6"] == 0 {
			line += " (not in the kernel: `termit-uv firewall refresh`)"
		}
		fmt.Fprintf(a.stdout, "%s\n", line)
	}
	if report.Note != "" {
		fmt.Fprintf(a.stdout, "note:       %s\n", report.Note)
	}
	return exitOK
}

// describeFirewall renders the one-line state, including how long is left to confirm.
func describeFirewall(report jsonFirewall, applied *firewall.Applied, now time.Time) string {
	switch {
	case !report.Applied:
		if applied != nil && !applied.RemovedAt.IsZero() {
			return fmt.Sprintf("off since %s", humanSince(now, applied.RemovedAt))
		}
		return "off"
	case report.Confirmed:
		return fmt.Sprintf("on, confirmed %s", humanSince(now, applied.ConfirmedAt))
	case report.ConfirmBy != "":
		left := applied.Deadline.Sub(now)
		if left < 0 {
			left = 0
		}
		return fmt.Sprintf("on, not confirmed — the table is removed in %s unless you run `termit-uv firewall confirm`",
			left.Round(time.Second))
	}
	return "on, not confirmed, and the rollback timer is overdue: run `termit-uv firewall confirm` to keep it"
}

// armRollback writes the rollback units and starts the timer.
func (a *application) armRollback(ctx context.Context, cfg *config.Config, configPath string) error {
	binary, err := a.executablePath()
	if err != nil {
		return err
	}
	oneshot, err := service.FirewallRollback(binary, configPath)
	if err != nil {
		return err
	}
	timer, err := service.FirewallRollbackTimer(service.ConfirmWindow)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		filepath.Join(cfg.Paths.UnitDir, service.FirewallRollbackUnitName):  oneshot,
		filepath.Join(cfg.Paths.UnitDir, service.FirewallRollbackTimerName): timer,
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(name), err)
		}
		if err := state.WriteFileAtomic(name, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if _, err := a.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	// restart, not start, and never enable: the timer exists only while an apply is unconfirmed, and
	// re-arming has to reset the window rather than be ignored because it is already running.
	if _, err := a.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"restart", service.FirewallRollbackTimerName}}); err != nil {
		return fmt.Errorf("start %s: %w", service.FirewallRollbackTimerName, err)
	}
	return nil
}

// disarmRollback stops the timer and removes both units. It is best effort by design: it is the way
// out of a firewall, so a host without the units must not fail it.
func (a *application) disarmRollback(ctx context.Context, cfg *config.Config) error {
	_, _ = a.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"disable", "--now", service.FirewallRollbackTimerName}})
	removed := false
	for _, name := range []string{service.FirewallRollbackTimerName, service.FirewallRollbackUnitName} {
		err := os.Remove(filepath.Join(cfg.Paths.UnitDir, name))
		switch {
		case err == nil:
			removed = true
		case errors.Is(err, os.ErrNotExist):
		default:
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	if removed {
		if _, err := a.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w", err)
		}
	}
	return nil
}

// recordRemoved marks the record so `status` says when the table went away.
func (a *application) recordRemoved(cfg *config.Config, now time.Time) error {
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil {
		return err
	}
	if applied == nil {
		return nil
	}
	applied.RemovedAt = now.UTC()
	applied.Deadline = time.Time{}
	return firewall.SaveApplied(cfg.Paths.FirewallPath(), applied)
}

// nftApplier wires the firewall package to this host's executor.
func (a *application) nftApplier() firewall.Applier {
	return firewall.Applier{Runner: nftRunner{exec: a.exec}}
}

// nftRunner adapts the installer's executor to the firewall package's smaller interface.
type nftRunner struct{ exec install.Executor }

func (r nftRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, error) {
	res, err := r.exec.Run(ctx, install.Cmd{Name: name, Args: args, Stdin: stdin})
	return res.Stdout, err
}

// isRoot answers "may this process change the host's firewall".
func (a *application) isRoot() bool {
	euid := a.euid
	if euid == nil {
		euid = os.Geteuid
	}
	return euid() == 0
}

// executablePath is the binary the generated units run.
func (a *application) executablePath() (string, error) {
	if a.executable != "" {
		return a.executable, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	return path, nil
}

// prefixStrings renders networks the way the configuration writes them.
func prefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, prefix.String())
	}
	return out
}

// denyWords and portsWords render the two facts people ask about in a status line.
func denyWords(deny bool) string {
	if deny {
		return "default deny"
	}
	return "other inbound allowed"
}

func portsWords(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, fmt.Sprintf("%d", port))
	}
	if len(parts) == 0 {
		return "none recorded"
	}
	return strings.Join(parts, ", ")
}

// humanSince renders an age the way a person reads it.
func humanSince(now, then time.Time) string {
	delta := now.Sub(then).Round(time.Second)
	switch {
	case delta < 0:
		return "in " + (-delta).String()
	case delta < time.Minute:
		return fmt.Sprintf("%s ago", delta)
	default:
		return fmt.Sprintf("%s ago", delta.Round(time.Minute))
	}
}
