package hardencmd

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// Firewall is the group's dispatch. A bare `firewall` is `firewall status`.
func Firewall(env *Env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "on":
			return firewallOn(env, args[1:])
		case "status":
			return firewallStatus(env, args[1:])
		case "off":
			return firewallOff(env, args[1:])
		case "confirm":
			return firewallConfirm(env, args[1:])
		case "rollback":
			return firewallRollback(env, args[1:])
		case "refresh":
			return firewallRefresh(env, args[1:])
		case "restore":
			return firewallRestore(env, args[1:])
		}
	}
	return firewallStatus(env, args)
}

// firewallSummary is the group's help page. It says plainly what the rest of the binary promises: the
// table exists only because someone typed `on`.
const firewallSummary = `The host firewall: one nftables table, applied only when you say so.

  %s firewall            the same as ` + "`firewall status`" + `
  %s firewall on         render and apply the table; --dry-run prints it instead
  %s firewall off        remove the table and leave the host exactly as it was
  %s firewall confirm    keep what ` + "`on`" + ` applied (the rollback timer stands down)
  %s firewall rollback   what the timer runs: remove the table unless it was confirmed
  %s firewall refresh    fetch the network blocklist and load it into the table
  %s firewall restore    put back the table the last on applied, if it was confirmed (the boot unit)

` + "`on`" + ` arms a five-minute timer before it touches anything. If you do not confirm, the table is
removed — which is what makes it safe to run over the very connection it might cut. Nothing else in
Termit applies or removes it: not init, not sync, not an agent, not a timer.`

// summary renders the group's help page for this component.
func (e *Env) summary() string {
	name := e.Target.Name()
	return fmt.Sprintf(firewallSummary, name, name, name, name, name, name, name)
}

// jsonFirewall is the machine-readable firewall report.
type jsonFirewall struct {
	Table       string   `json:"table"`
	Applied     bool     `json:"applied"`
	Confirmed   bool     `json:"confirmed"`
	DefaultDeny bool     `json:"default_deny"`
	Ports       []int    `json:"ports,omitempty"`
	UDPPorts    []int    `json:"udp_ports,omitempty"`
	SSHFrom     []string `json:"ssh_from,omitempty"`
	// Drift names the fields where the configuration disagrees with the record the last `on` wrote:
	// an empty list is a table that still answers for the node as it is configured.
	Drift       []string `json:"drift,omitempty"`
	Bans        int      `json:"bans"`
	Blocked     int      `json:"blocked"`
	Scans       int      `json:"scans"`
	Blocklist   int      `json:"blocklist"`
	BlocklistAt string   `json:"blocklist_fetched,omitempty"`
	ConfirmBy   string   `json:"confirm_by,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// firewallOn renders the table and applies it. The rollback timer is armed first: the whole point is
// that an apply nobody could confirm undoes itself.
func firewallOn(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("firewall on", `Renders this host's table and applies it with nft. The whole ruleset is one atomic
transaction, so a failure leaves the previous rules in force. A five-minute rollback timer is armed
first: if you cannot reach the host to confirm, the table removes itself.`)
	configFlag := flags.String("config", "", "path to config.yaml")
	dryRun := flags.Bool("dry-run", false, "print the ruleset and change nothing")
	sshFrom := flags.String("ssh-from", "", "networks allowed to reach port 22, comma separated (overrides security.firewall.ssh_from)")
	defaultDeny := flags.Bool("default-deny", true, "drop inbound traffic the rules did not accept (overrides security.firewall.default_deny)")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "%s firewall on: unexpected argument %q\n", name, flags.Arg(0))
		return ExitUsage
	}
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })

	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall on: %v\n", name, err)
		return ExitFailure
	}
	tableName, ports := env.table()
	table, err := component.Security.Table(tableName, ports)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall on: %v\n", name, err)
		return ExitFailure
	}
	if given["ssh-from"] {
		prefixes, err := firewall.Prefixes(splitList(*sshFrom))
		if err != nil {
			fmt.Fprintf(env.Stderr, "%s firewall on: --ssh-from: %v\n", name, err)
			return ExitUsage
		}
		table.SSHFrom = prefixes
	}
	if given["default-deny"] {
		table.DefaultDeny = *defaultDeny
	}
	ruleset, err := table.Render()
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall on: %v\n", name, err)
		return ExitUsage
	}

	applier := env.applier()
	ctx := context.Background()
	if *dryRun {
		fmt.Fprint(env.Stdout, string(ruleset))
		if env.Root() {
			if err := applier.Check(ctx, ruleset); err != nil {
				fmt.Fprintf(env.Stderr, "%s firewall on: %v\n", name, err)
				return ExitFailure
			}
			fmt.Fprintln(env.Stderr, "the ruleset parses; nothing was applied")
		} else {
			fmt.Fprintln(env.Stderr, "not verified with nft: this is not root, so the kernel cannot be asked")
		}
		return ExitOK
	}
	if !env.Root() {
		fmt.Fprintf(env.Stderr, "%s firewall on: this changes the host's firewall, so it needs root\n", name)
		return ExitFailure
	}

	if err := env.Rollback(component).Arm(ctx); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall on: %v\n", name, err)
		return ExitFailure
	}
	if err := applier.Apply(ctx, ruleset); err != nil {
		// Nothing changed: nft commits the batch or nothing. The timer has nothing to undo.
		_ = env.Rollback(component).Disarm(ctx)
		fmt.Fprintf(env.Stderr, "%s firewall on: %v\n", name, err)
		return ExitFailure
	}
	now := env.Now()
	applied := &firewall.Applied{
		Table:       table.Name,
		Ports:       table.Ports,
		UDPPorts:    table.UDPPorts,
		SSHFrom:     prefixStrings(table.SSHFrom),
		DefaultDeny: table.DefaultDeny,
		AppliedAt:   now.UTC(),
		Deadline:    now.Add(firewall.ConfirmWindow).UTC(),
	}
	if err := firewall.SaveApplied(component.Paths.Firewall, applied); err != nil {
		// Without the record the timer cannot know what to undo, so undo it now.
		_ = applier.Remove(ctx, table.Name)
		_ = env.Rollback(component).Disarm(ctx)
		fmt.Fprintf(env.Stderr, "%s firewall on: %v\n", name, err)
		return ExitFailure
	}

	fmt.Fprintf(env.Stdout, "firewall: on, table inet %s, %s, ports %s\n",
		table.Name, denyWords(table.DefaultDeny), portsWords(table.Ports))
	// The table starts with empty sets, so the list in force goes back in before the operator walks away.
	loadBlocklist(env, component, table.Name)
	if len(table.SSHFrom) > 0 {
		fmt.Fprintf(env.Stdout, "ssh:      port %d from %s\n", firewall.SSHPort, strings.Join(prefixStrings(table.SSHFrom), ", "))
	}
	fmt.Fprintf(env.Stdout, "confirm:  run `%s firewall confirm` within %s, or the table is removed\n",
		name, firewall.ConfirmWindow)
	fmt.Fprintf(env.Stdout, "now:      check that SSH and 443 still answer before you close this session\n")
	return ExitOK
}

// firewallOff removes the table and disarms the rollback timer. It is the way back from every mistake,
// so it does not ask.
func firewallOff(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("firewall off", "Removes the whole table and the rollback timer. The host is left as it was before `on`.")
	configFlag := flags.String("config", "", "path to config.yaml")
	dryRun := flags.Bool("dry-run", false, "say what would be removed and change nothing")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall off: %v\n", name, err)
		return ExitFailure
	}
	tableName, _ := env.table()
	if *dryRun {
		fmt.Fprintf(env.Stdout, "would remove table inet %s and the rollback timer\n", tableName)
		return ExitOK
	}
	if !env.Root() {
		fmt.Fprintf(env.Stderr, "%s firewall off: this changes the host's firewall, so it needs root\n", name)
		return ExitFailure
	}
	ctx := context.Background()
	if err := env.applier().Remove(ctx, tableName); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall off: %v\n", name, err)
		return ExitFailure
	}
	if err := env.Rollback(component).Disarm(ctx); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall off: %v\n", name, err)
		return ExitFailure
	}
	if err := recordRemoved(component, env.Now()); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall off: %v\n", name, err)
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "firewall: off, table inet %s removed and the rollback timer stopped\n", tableName)
	return ExitOK
}

// firewallConfirm keeps what `on` applied by disarming the timer. It changes no rule, so it works from
// the session the firewall just kept alive.
func firewallConfirm(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("firewall confirm", "Keeps the rules `firewall on` applied by standing the rollback timer down. Changes no rule.")
	configFlag := flags.String("config", "", "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall confirm: %v\n", name, err)
		return ExitFailure
	}
	applied, err := firewall.LoadApplied(component.Paths.Firewall)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall confirm: %v\n", name, err)
		return ExitFailure
	}
	now := env.Now()
	if !applied.Pending(now) {
		if applied != nil && applied.Confirmed() {
			fmt.Fprintln(env.Stdout, "firewall: already confirmed; the rules stay")
			return ExitOK
		}
		fmt.Fprintf(env.Stderr, "%s firewall confirm: no applied firewall to confirm; run `%s firewall on`\n", name, name)
		return ExitFailure
	}
	ctx := context.Background()
	if err := env.Rollback(component).Disarm(ctx); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall confirm: %v\n", name, err)
		return ExitFailure
	}
	applied.ConfirmedAt = now.UTC()
	applied.Deadline = time.Time{}
	if err := firewall.SaveApplied(component.Paths.Firewall, applied); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall confirm: %v\n", name, err)
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "firewall: confirmed, table inet %s stays\n", applied.Table)
	return ExitOK
}

// firewallRollback is what the timer runs. It removes the table unless the apply was confirmed, and
// says so on stderr, which lands in the journal of the oneshot.
func firewallRollback(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("firewall rollback", "Runs from the rollback timer: removes the table unless `firewall confirm` ran. Not for interactive use.")
	configFlag := flags.String("config", "", "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall rollback: %v\n", name, err)
		return ExitFailure
	}
	applied, err := firewall.LoadApplied(component.Paths.Firewall)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall rollback: %v\n", name, err)
		return ExitFailure
	}
	if !applied.Pending(env.Now()) {
		// Confirmed, already removed, or never applied: nothing to undo.
		_ = env.Rollback(component).Disarm(context.Background())
		return ExitOK
	}
	tableName, _ := env.table()
	ctx := context.Background()
	if err := env.applier().Remove(ctx, tableName); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall rollback: %v\n", name, err)
		return ExitFailure
	}
	_ = env.Rollback(component).Disarm(ctx)
	if err := recordRemoved(component, env.Now()); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall rollback: %v\n", name, err)
		return ExitFailure
	}
	fmt.Fprintf(env.Stderr, "firewall: removed table inet %s — it was applied at %s and never confirmed\n",
		applied.Table, applied.AppliedAt.Format(time.RFC3339))
	return ExitOK
}

// firewallStatus reports what is on the host, what was applied, and where the configuration disagrees
// with either.
func firewallStatus(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("firewall [status]", env.summary())
	configFlag := flags.String("config", "", "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "%s firewall: unexpected argument %q\n", name, flags.Arg(0))
		return ExitUsage
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall: %v\n", name, err)
		return ExitFailure
	}
	tableName, ports := env.table()
	applied, err := firewall.LoadApplied(component.Paths.Firewall)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall: %v\n", name, err)
		return ExitFailure
	}
	applier := env.applier()
	ctx := context.Background()
	present, err := applier.Present(ctx, tableName)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall: %v\n", name, err)
		return ExitFailure
	}
	counts, err := applier.Counts(ctx, tableName)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall: %v\n", name, err)
		return ExitFailure
	}
	now := env.Now()

	report := jsonFirewall{
		Table:   tableName,
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
		report.UDPPorts = applied.UDPPorts
		// What the configuration would render now: a field that differs is a table the operator
		// confirmed no longer answering for the node as it is configured.
		if want, err := component.Security.Table(tableName, ports); err == nil {
			report.Drift = firewall.Drift(applied, want)
		}
	}
	report.Confirmed = applied.Confirmed()
	if applied.Pending(now) && now.Before(applied.Deadline) {
		report.ConfirmBy = applied.Deadline.Format(time.RFC3339)
	}
	// The list in force, from the file rather than the kernel: the kernel's sets are a copy that a
	// reboot or a failed load can empty, and the file is what the next refresh restores.
	if prefixes, fetchedAt, err := firewall.LoadBlocklist(component.Paths.Blocklist); err == nil {
		report.Blocklist = len(prefixes)
		if !fetchedAt.IsZero() {
			report.BlocklistAt = humanSince(now, fetchedAt)
		}
	}
	var notes []string
	switch {
	case present && applied == nil:
		notes = append(notes, "a table is applied but nothing here recorded it; `firewall off` then `on` puts it back in writing")
	case present && !component.Security.Firewall.Enabled:
		notes = append(notes, "security.firewall.enabled is off in the configuration, but a table is applied")
	case !present && component.Security.Firewall.Enabled:
		notes = append(notes, "the configuration asks for the firewall, but no table is applied: run `"+name+" firewall on`")
	}
	if len(report.Drift) > 0 {
		notes = append(notes, fmt.Sprintf("the configuration changed after this table was applied (%s differ); "+
			"`%s firewall on` applies what the configuration says", strings.Join(report.Drift, ", "), name))
	}
	report.Note = strings.Join(notes, "; ")

	if *jsonOut {
		if err := writeJSON(env.Stdout, report); err != nil {
			fmt.Fprintf(env.Stderr, "%s firewall: %v\n", name, err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(env.Stdout, "firewall:   %s\n", describeFirewall(report, applied, now))
	if present {
		deny := "default deny"
		if !report.DefaultDeny {
			deny = "other inbound allowed"
		}
		fmt.Fprintf(env.Stdout, "table:      inet %s, %s, ports %s\n", tableName, deny, portsWords(report.Ports))
		fmt.Fprintf(env.Stdout, "udp:        %s\n", portsWords(report.UDPPorts))
		if len(report.SSHFrom) > 0 {
			fmt.Fprintf(env.Stdout, "ssh:        port %d from %s\n", firewall.SSHPort, strings.Join(report.SSHFrom, ", "))
		} else {
			fmt.Fprintf(env.Stdout, "ssh:        port %d from anywhere\n", firewall.SSHPort)
		}
		fmt.Fprintf(env.Stdout, "elements:   %s\n", firewall.Describe(counts))
	}
	if report.Blocklist > 0 {
		line := fmt.Sprintf("blocklist:  %d networks", report.Blocklist)
		if report.BlocklistAt != "" {
			line += ", fetched " + report.BlocklistAt
		}
		if len(counts) > 0 && counts["blocked4"]+counts["blocked6"] == 0 {
			line += " (not in the kernel: `" + name + " firewall refresh`)"
		}
		fmt.Fprintf(env.Stdout, "%s\n", line)
	}
	if report.Note != "" {
		fmt.Fprintf(env.Stdout, "note:       %s\n", report.Note)
	}
	return ExitOK
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
		return fmt.Sprintf("on, not confirmed — the table is removed in %s unless you run `firewall confirm`",
			left.Round(time.Second))
	}
	return "on, not confirmed, and the rollback timer is overdue: run `firewall confirm` to keep it"
}

// recordRemoved marks the record so `status` says when the table went away.
func recordRemoved(component *Component, now time.Time) error {
	applied, err := firewall.LoadApplied(component.Paths.Firewall)
	if err != nil {
		return err
	}
	if applied == nil {
		return nil
	}
	applied.RemovedAt = now.UTC()
	applied.Deadline = time.Time{}
	return firewall.SaveApplied(component.Paths.Firewall, applied)
}

// loadBlocklist restores the list in force into a freshly applied table. A failure is reported and not
// fatal: the table works without it, and the next refresh repairs it.
func loadBlocklist(env *Env, component *Component, table string) {
	prefixes, _, err := firewall.LoadBlocklist(component.Paths.Blocklist)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall: the blocklist was not loaded: %v\n", env.Target.Name(), err)
		return
	}
	if len(prefixes) == 0 {
		return
	}
	if err := env.applier().ReplaceBlocked(context.Background(), table, prefixes); err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall: the blocklist was not loaded: %v\n", env.Target.Name(), err)
		return
	}
	fmt.Fprintf(env.Stdout, "blocklist: %d networks loaded\n", len(prefixes))
}
