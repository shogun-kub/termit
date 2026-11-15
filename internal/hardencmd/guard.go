package hardencmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os/signal"
	"syscall"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/guard"
)

// guardSummary is the group's help page.
const guardSummary = `The ban engine: it reads the kernel's scan set and, when the component keeps one, the web server's
log; bans sources with the ladder and keeps the decisions in bans.json. It only ever adds or removes
elements inside the table the operator applied; it never applies, changes or removes the table itself.

  %s guard            the same as ` + "`guard status`" + `
  %s guard run        the loop the unit runs; --once runs one pass and exits
  %s guard status     whether the loop is running and what it has banned`

// guardHelp renders the group's help page for this component.
func (e *Env) guardHelp() string {
	name := e.Target.Name()
	return fmt.Sprintf(guardSummary, name, name, name)
}

// Guard is the group's dispatch. A bare `guard` is `guard status`.
func Guard(env *Env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return guardRun(env, args[1:])
		case "status":
			return guardStatus(env, args[1:])
		}
	}
	return guardStatus(env, args)
}

// guardRun is the unit's command. It refuses to run when the configuration says the guard is off: a loop
// that bans without being asked is exactly what the design forbids.
func guardRun(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("guard run", "Runs the ban engine until it is stopped. It reads security.guard from the configuration and\n"+
		"does nothing when security.guard.enabled is false.")
	configFlag := flags.String("config", "", "path to config.yaml")
	once := flags.Bool("once", false, "run one pass and exit; it is what a drill or a test wants")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "%s guard run: unexpected argument %q\n", name, flags.Arg(0))
		return ExitUsage
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s guard: %v\n", name, err)
		return ExitFailure
	}
	if !component.Security.Guard.Enabled {
		// Exit 0, not an error: the unit may still be installed while the operator has turned the guard
		// off, and a non-zero exit would restart it every thirty seconds forever.
		fmt.Fprintln(env.Stdout, "guard: security.guard.enabled is false, so the ban engine stays off")
		return ExitOK
	}
	tableName, _ := env.table()
	engine, err := guard.New(guardSettings(env, component, tableName), env.applier())
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s guard: %v\n", name, err)
		return ExitFailure
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *once {
		if err := engine.Cycle(ctx); err != nil {
			fmt.Fprintf(env.Stderr, "%s guard: %v\n", name, err)
			return ExitFailure
		}
		fmt.Fprintln(env.Stdout, "guard: one pass done")
		return ExitOK
	}
	slog.Default().Info("guard started", "table", tableName, "bans", component.Paths.Bans)
	if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(env.Stderr, "%s guard: %v\n", name, err)
		return ExitFailure
	}
	return ExitOK
}

// guardStatus reports whether the loop is running and what it decided.
func guardStatus(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("guard [status]", env.guardHelp())
	configFlag := flags.String("config", "", "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "%s guard: unexpected argument %q\n", name, flags.Arg(0))
		return ExitUsage
	}
	component, err := env.Target.Open(configPath(env, *configFlag))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s guard: %v\n", name, err)
		return ExitFailure
	}
	bans, err := firewall.LoadBans(component.Paths.Bans)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s guard: %v\n", name, err)
		return ExitFailure
	}
	now := env.Now()
	active := bans.Active(now)
	offenders := bans.Offenders(now)
	state := "off"
	if component.Security.Guard.Enabled {
		state = "on"
	}
	tableName, _ := env.table()
	if *jsonOut {
		report := map[string]any{
			"enabled":    component.Security.Guard.Enabled,
			"table":      tableName,
			"bans":       len(active),
			"offenders":  len(offenders),
			"access_log": component.AccessLog,
		}
		if err := writeJSON(env.Stdout, report); err != nil {
			fmt.Fprintf(env.Stderr, "%s guard: %v\n", name, err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(env.Stdout, "guard:      %s\n", state)
	if !component.Security.Guard.Enabled {
		return ExitOK
	}
	fmt.Fprintf(env.Stdout, "table:      inet %s (the firewall command owns it)\n", tableName)
	if component.AccessLog == "" {
		fmt.Fprintf(env.Stdout, "log:        none: this component keeps no access log, so only scans are counted\n")
	} else {
		fmt.Fprintf(env.Stdout, "log:        %s\n", component.AccessLog)
	}
	fmt.Fprintf(env.Stdout, "bans:       %d active, %d offenders\n", len(active), len(offenders))
	return ExitOK
}

// guardSettings wires the loop: the table, the record, the log and the two ladders it applies.
func guardSettings(env *Env, component *Component, table string) guard.Settings {
	return guard.Settings{
		Table:     table,
		BansPath:  component.Paths.Bans,
		AccessLog: component.AccessLog,
		Ladder:    component.Security.Ladder(),
		Failures: guard.Failures{
			Attempts: component.Security.Guard.Path404.Attempts,
			Window:   component.Security.Guard.Path404.Window.Std(),
		},
		Never: func(ctx context.Context) []netip.Prefix {
			if component.Never == nil {
				return nil
			}
			return component.Never(ctx)
		},
		Now: env.Now,
		Log: slog.Default(),
	}
}
