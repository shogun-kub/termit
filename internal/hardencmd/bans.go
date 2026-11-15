package hardencmd

import (
	"context"
	"fmt"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// bansSummary is the group's help page.
const bansSummary = `The ban list the guard keeps in bans.json, and the operator's own hand.

  %s bans            the same as ` + "`bans list`" + `
  %s bans list       what is banned now, and why
  %s bans offenders  sources the ladder ran out of rungs for; the decision is yours
  %s bans block IP   ban a source permanently, by hand
  %s bans unban IP   lift a ban now; the ladder remembers the offence
  %s bans unblock IP lift your own permanent block

Only ` + "`block`" + ` is permanent, and only you can do it. Everything else expires on its own; ` + "`unban`" + `
keeps the offence on record, so a repeat earns the longer ban. The guard re-applies these decisions to
the kernel, so a ban survives a reboot even though the kernel's sets do not.`

// bansHelp renders the group's help page for this component.
func (e *Env) bansHelp() string {
	name := e.Target.Name()
	return fmt.Sprintf(bansSummary, name, name, name, name, name, name)
}

// jsonBan is one row of the machine-readable list.
type jsonBan struct {
	IP       string `json:"ip"`
	Signal   string `json:"signal"`
	Until    string `json:"until,omitempty"`
	Left     string `json:"left,omitempty"`
	Count    int    `json:"count,omitempty"`
	Offender bool   `json:"offender,omitempty"`
	Active   bool   `json:"active"`
}

// Bans is the group's dispatch. A bare `bans` is `bans list`.
func Bans(env *Env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return bansList(env, args[1:])
		case "offenders":
			return bansOffenders(env, args[1:])
		case "block":
			return bansBlock(env, args[1:])
		case "unban":
			return bansLift(env, args[1:], "unban")
		case "unblock":
			return bansLift(env, args[1:], "unblock")
		}
	}
	return bansList(env, args)
}

// bansList prints what is banned now.
func bansList(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("bans [list]", env.bansHelp())
	configFlag := flags.String("config", "", "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the list as JSON")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(env.Stderr, "%s bans: unexpected argument %q\n", name, flags.Arg(0))
		return ExitUsage
	}
	banList, code := loadBans(env, *configFlag)
	if code != ExitOK {
		return code
	}
	now := env.Now()
	active := banList.Active(now)
	if *jsonOut {
		rows := make([]jsonBan, 0, len(active))
		for _, ban := range active {
			rows = append(rows, banRow(ban, now, true))
		}
		if err := writeJSON(env.Stdout, rows); err != nil {
			fmt.Fprintf(env.Stderr, "%s bans: %v\n", name, err)
			return ExitFailure
		}
		return ExitOK
	}
	if len(active) == 0 {
		fmt.Fprintln(env.Stdout, "bans:       none active")
		return ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-20s %-9s %-10s %s\n", "address", "signal", "left", "why")
	for _, ban := range active {
		left := "permanent"
		if !ban.Permanent() {
			left = ban.Remaining(now).Round(time.Second).String()
		}
		why := fmt.Sprintf("%d×", ban.Count)
		if ban.Offender {
			why += ", ladder exhausted"
		}
		fmt.Fprintf(env.Stdout, "%-20s %-9s %-10s %s\n", ban.IP, ban.Signal, left, why)
	}
	return ExitOK
}

// bansOffenders prints the sources the ladder gave up on.
func bansOffenders(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("bans offenders", "Sources that kept coming back after the longest ban the ladder has. Nothing automatic\n"+
		"blocks them permanently: `bans block` is the decision.")
	configFlag := flags.String("config", "", "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the list as JSON")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	banList, code := loadBans(env, *configFlag)
	if code != ExitOK {
		return code
	}
	now := env.Now()
	offenders := banList.Offenders(now)
	if *jsonOut {
		rows := make([]jsonBan, 0, len(offenders))
		for _, ban := range offenders {
			rows = append(rows, banRow(ban, now, ban.Active(now)))
		}
		if err := writeJSON(env.Stdout, rows); err != nil {
			fmt.Fprintf(env.Stderr, "%s bans: %v\n", name, err)
			return ExitFailure
		}
		return ExitOK
	}
	if len(offenders) == 0 {
		fmt.Fprintln(env.Stdout, "offenders:  none")
		return ExitOK
	}
	fmt.Fprintf(env.Stdout, "%-20s %-9s %-10s %s\n", "address", "signal", "seen", "banned now")
	for _, ban := range offenders {
		banned := "no"
		if ban.Active(now) {
			banned = "yes"
		}
		fmt.Fprintf(env.Stdout, "%-20s %-9s %-10s %s\n", ban.IP, ban.Signal, fmt.Sprintf("%d×", ban.Count), banned)
	}
	return ExitOK
}

// bansBlock records a permanent ban and puts it in the kernel.
func bansBlock(env *Env, args []string) int {
	name := env.Target.Name()
	flags := env.newFlags("bans block", "Bans one address permanently. It stays until `bans unblock`.")
	configFlag := flags.String("config", "", "path to config.yaml")
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return flagExit(err)
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(env.Stderr, "%s bans block: one address is required\n", name)
		return ExitUsage
	}
	component, banList, code := loadBansComponent(env, *configFlag)
	if code != ExitOK {
		return code
	}
	ip, err := bannable(context.Background(), component, flags.Arg(0))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s bans block: %v\n", name, err)
		return ExitUsage
	}
	now := env.Now()
	banList.Put(firewall.Ban{IP: ip, Signal: firewall.SignalOperator, Count: 1, Last: now.UTC()})
	if err := firewall.SaveBans(component.Paths.Bans, banList); err != nil {
		fmt.Fprintf(env.Stderr, "%s bans block: %v\n", name, err)
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "bans:       %s is banned permanently\n", ip)
	applyBanNow(env, ip, 0)
	return ExitOK
}

// bansLift lifts a ban without forgetting why it was there: the record keeps its count, so a source
// that comes back earns the longer ban next time. `unban` lifts any ban, `unblock` only the operator's
// permanent one.
func bansLift(env *Env, args []string, command string) int {
	name := env.Target.Name()
	flags := env.newFlags("bans "+command, map[string]string{
		"unban": "Lifts a ban now. The ladder remembers the offence, so a repeat earns the longer ban: this is " +
			"not a way to forget what happened.",
		"unblock": "Lifts the permanent block you put on an address with `bans block`. The record keeps the " +
			"offence, like `bans unban`.",
	}[command])
	configFlag := flags.String("config", "", "path to config.yaml")
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return flagExit(err)
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(env.Stderr, "%s bans %s: one address is required\n", name, command)
		return ExitUsage
	}
	component, banList, code := loadBansComponent(env, *configFlag)
	if code != ExitOK {
		return code
	}
	prefix, err := firewall.ParsePrefix(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s bans %s: %v\n", name, command, err)
		return ExitUsage
	}
	address := prefix.Addr().String()
	record, found := banList.Get(address)
	now := env.Now()
	if !found || !record.Active(now) {
		fmt.Fprintf(env.Stdout, "bans:       %s is not banned\n", address)
		return ExitOK
	}
	if command == "unblock" && !record.Permanent() {
		fmt.Fprintf(env.Stderr, "%s bans unblock: %s is banned by %s, not by you; use `bans unban`\n",
			name, address, record.Signal)
		return ExitUsage
	}
	record.Until = now.UTC()
	banList.Put(record)
	if err := firewall.SaveBans(component.Paths.Bans, banList); err != nil {
		fmt.Fprintf(env.Stderr, "%s bans %s: %v\n", name, err, command)
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "bans:       %s is no longer banned (offence %d kept)\n", address, record.Count)
	removeBanNow(env, address)
	return ExitOK
}

// loadBans reads the record, or reports what failed.
func loadBans(env *Env, flagValue string) (*firewall.Bans, int) {
	_, banList, code := loadBansComponent(env, flagValue)
	return banList, code
}

// loadBansComponent reads the configuration and the record, or reports which one failed.
func loadBansComponent(env *Env, flagValue string) (*Component, *firewall.Bans, int) {
	component, err := env.Target.Open(configPath(env, flagValue))
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s bans: %v\n", env.Target.Name(), err)
		return nil, nil, ExitFailure
	}
	banList, err := firewall.LoadBans(component.Paths.Bans)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%s bans: %v\n", env.Target.Name(), err)
		return nil, nil, ExitFailure
	}
	return component, banList, ExitOK
}

// applyBanNow updates the kernel right away, so a command takes effect without waiting for the guard's
// next pass. With no table applied there is nothing to update, and the record is what matters.
func applyBanNow(env *Env, ip string, ttl time.Duration) {
	tableName, _ := env.table()
	ctx := context.Background()
	present, err := env.applier().Present(ctx, tableName)
	if err != nil || !present {
		return
	}
	if err := env.applier().AddElement(ctx, tableName, setFor(ip), ip, ttl); err != nil {
		fmt.Fprintf(env.Stderr, "%s bans: %v\n", env.Target.Name(), err)
	}
}

// removeBanNow takes the address out of the kernel's sets, if it was ever there. The scan sets go with
// it: the meter remembers for minutes, and an unban that the next cycle undoes is not an unban.
func removeBanNow(env *Env, ip string) {
	tableName, _ := env.table()
	ctx := context.Background()
	present, err := env.applier().Present(ctx, tableName)
	if err != nil || !present {
		return
	}
	if err := env.applier().RemoveBan(ctx, tableName, ip); err != nil {
		fmt.Fprintf(env.Stderr, "%s bans: %v\n", env.Target.Name(), err)
	}
	if err := env.applier().ForgetScan(ctx, tableName, ip); err != nil {
		fmt.Fprintf(env.Stderr, "%s bans: %v\n", env.Target.Name(), err)
	}
}

// banRow renders one record for the JSON list.
func banRow(ban firewall.Ban, now time.Time, active bool) jsonBan {
	row := jsonBan{
		IP: ban.IP, Signal: string(ban.Signal), Count: ban.Count,
		Offender: ban.Offender, Active: active,
	}
	if !ban.Permanent() {
		row.Until = ban.Until.UTC().Format(time.RFC3339)
		row.Left = ban.Remaining(now).Round(time.Second).String()
	}
	return row
}
