package hardencmd

import (
	"context"
	"fmt"

	"github.com/shogun-kub/termit/internal/firewall"
)

// firewallRestore puts back the table a previous `on` applied. It is what the boot unit runs, and its
// whole judgement is the record: a confirmed apply comes back, an apply that was still waiting comes back
// with the rollback timer armed, and anything else stays gone. The configuration is not consulted — an
// operator who applied a table and confirmed it does not have to re-assert it after every reboot.
func firewallRestore(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("firewall restore", `Puts back the table the last `+"`firewall on`"+` applied, if it was confirmed. It is what the boot
unit runs; running it by hand is the way to check what a reboot would do.`)
	configFlag := flags.String("config", "", "path to config.yaml")
	dryRun := flags.Bool("dry-run", false, "say what would happen and change nothing")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "%s firewall restore: unexpected argument %q\n", name, flags.Arg(0))
		return ExitUsage
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall restore: %v\n", name, err)
		return ExitFailure
	}
	applied, err := firewall.LoadApplied(component.Paths.Firewall)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall restore: %v\n", name, err)
		return ExitFailure
	}
	now := env.Now()
	apply, armTimer := applied.Restore(now)
	if !apply {
		// Not an error: a host that never applied a table, or one whose operator took it down, is a
		// host the boot unit has nothing to do on.
		fmt.Fprintf(env.Stdout, "firewall: nothing to restore\n")
		return ExitOK
	}

	tableName, _ := env.table()
	if applied.Table != "" && applied.Table != tableName {
		fmt.Fprintf(env.Stderr, "%s firewall restore: the record is for table inet %s, this is %s; nothing restored\n",
			name, applied.Table, tableName)
		return ExitOK
	}
	prefixes, err := firewall.Prefixes(applied.SSHFrom)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall restore: %v\n", name, err)
		return ExitFailure
	}
	table := firewall.Table{
		Name:        tableName,
		Ports:       applied.Ports,
		UDPPorts:    applied.UDPPorts,
		SSHFrom:     prefixes,
		DefaultDeny: applied.DefaultDeny,
		Scan:        component.Security.Scan(),
	}
	ruleset, err := table.Render()
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall restore: %v\n", name, err)
		return ExitUsage
	}
	ctx := context.Background()
	present, err := env.applier().Present(ctx, tableName)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s firewall restore: %v\n", name, err)
		return ExitFailure
	}
	if present && !armTimer {
		fmt.Fprintf(env.Stdout, "firewall: table inet %s is already applied; nothing to do\n", tableName)
		return ExitOK
	}
	if *dryRun {
		fmt.Fprintf(env.Stdout, "would restore table inet %s (ports %s, %s)\n",
			tableName, portsWords(table.Ports), denyWords(table.DefaultDeny))
		if armTimer {
			fmt.Fprintf(env.Stdout, "would arm the rollback timer: it was applied at %s and never confirmed\n",
				applied.AppliedAt.Format(timeLayout))
		}
		return ExitOK
	}
	if !env.Root() {
		fmt.Fprintf(env.Stderr, "%s firewall restore: this changes the host's firewall, so it needs root\n", name)
		return ExitFailure
	}
	if armTimer {
		if err := env.Rollback(component).Arm(ctx); err != nil {
			fmt.Fprintf(env.Stderr, "%s firewall restore: %v\n", name, err)
			return ExitFailure
		}
	}
	if err := env.applier().Apply(ctx, ruleset); err != nil {
		if armTimer {
			_ = env.Rollback(component).Disarm(ctx)
		}
		fmt.Fprintf(env.Stderr, "%s firewall restore: %v\n", name, err)
		return ExitFailure
	}
	loadBlocklist(env, component, tableName)
	if armTimer {
		fmt.Fprintf(env.Stdout, "firewall: restored table inet %s and armed the rollback timer: it was applied at %s "+
			"and never confirmed\n", tableName, applied.AppliedAt.Format(timeLayout))
		return ExitOK
	}
	fmt.Fprintf(env.Stdout, "firewall: restored table inet %s, confirmed %s\n",
		tableName, humanSince(now, applied.ConfirmedAt))
	return ExitOK
}

// timeLayout is how the record's timestamps are printed: RFC 3339 without the monotonic clock noise.
const timeLayout = "2006-01-02T15:04:05Z07:00"
