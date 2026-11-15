package main

import (
	"context"
	"fmt"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// bansSummary is the group's help page.
const bansSummary = `The ban list the guard keeps in bans.json, and the operator's own hand.

  termit-uv bans            the same as ` + "`bans list`" + `
  termit-uv bans list       what is banned now, and why
  termit-uv bans offenders  sources the ladder ran out of rungs for; the decision is yours
  termit-uv bans block IP   ban a source permanently, by hand
  termit-uv bans unban IP   lift a ban and forget the ladder history for that source
  termit-uv bans unblock IP lift your own permanent block

Only ` + "`block`" + ` is permanent, and only you can do it. Everything else expires on its own; ` + "`unban`" + `
starts the source's ladder over. The guard re-applies these decisions to the kernel, so a ban
survives a reboot even though the kernel's sets do not.`

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

func (a *application) runBans(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return a.runBansList(args[1:])
		case "offenders":
			return a.runBansOffenders(args[1:])
		case "block":
			return a.runBansBlock(args[1:])
		case "unban":
			return a.runBansLift(args[1:], "unban")
		case "unblock":
			return a.runBansLift(args[1:], "unblock")
		}
	}
	return a.runBansList(args)
}

// runBansList prints what is banned now.
func (a *application) runBansList(args []string) int {
	flags := a.newFlagSet("bans [list]", bansSummary)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the list as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv bans: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	_, banList, code := a.loadBans(*configFlag)
	if code != exitOK {
		return code
	}
	now := a.now()
	active := banList.Active(now)
	if *jsonOut {
		rows := make([]jsonBan, 0, len(active))
		for _, ban := range active {
			rows = append(rows, banRow(ban, now, true))
		}
		if err := writeJSON(a.stdout, rows); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv bans: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	if len(active) == 0 {
		fmt.Fprintln(a.stdout, "bans:       none active")
		return exitOK
	}
	fmt.Fprintf(a.stdout, "%-20s %-9s %-10s %s\n", "address", "signal", "left", "why")
	for _, ban := range active {
		left := "permanent"
		if !ban.Permanent() {
			left = ban.Remaining(now).Round(time.Second).String()
		}
		why := fmt.Sprintf("%d×", ban.Count)
		if ban.Offender {
			why += ", ladder exhausted"
		}
		fmt.Fprintf(a.stdout, "%-20s %-9s %-10s %s\n", ban.IP, ban.Signal, left, why)
	}
	return exitOK
}

// runBansOffenders prints the sources the ladder gave up on.
func (a *application) runBansOffenders(args []string) int {
	flags := a.newFlagSet("bans offenders", "Sources that kept coming back after the longest ban the ladder has. Nothing automatic\n"+
		"blocks them permanently: `bans block` is the decision.")
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the list as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	_, banList, code := a.loadBans(*configFlag)
	if code != exitOK {
		return code
	}
	now := a.now()
	offenders := banList.Offenders(now)
	if *jsonOut {
		rows := make([]jsonBan, 0, len(offenders))
		for _, ban := range offenders {
			rows = append(rows, banRow(ban, now, ban.Active(now)))
		}
		if err := writeJSON(a.stdout, rows); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv bans: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	if len(offenders) == 0 {
		fmt.Fprintln(a.stdout, "offenders:  none")
		return exitOK
	}
	fmt.Fprintf(a.stdout, "%-20s %-9s %-10s %s\n", "address", "signal", "seen", "banned now")
	for _, ban := range offenders {
		banned := "no"
		if ban.Active(now) {
			banned = "yes"
		}
		fmt.Fprintf(a.stdout, "%-20s %-9s %-10s %s\n", ban.IP, ban.Signal, fmt.Sprintf("%d×", ban.Count), banned)
	}
	return exitOK
}

// runBansBlock records a permanent ban and puts it in the kernel.
func (a *application) runBansBlock(args []string) int {
	flags := a.newFlagSet("bans block", "Bans one address permanently. It stays until `bans unblock`.")
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(a.stderr, "termit-uv bans block: one address is required")
		return exitUsage
	}
	cfg, banList, code := a.loadBans(*configFlag)
	if code != exitOK {
		return code
	}
	ip, err := a.checkBannable(cfg, flags.Arg(0))
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans block: %v\n", err)
		return exitUsage
	}
	now := a.now()
	banList.Put(firewall.Ban{IP: ip, Signal: firewall.SignalOperator, Count: 1, Last: now.UTC()})
	if err := firewall.SaveBans(cfg.Paths.BansPath(), banList); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans block: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "bans:       %s is banned permanently\n", ip)
	a.applyBanNow(cfg, ip, 0)
	return exitOK
}

// runBansLift lifts a ban without forgetting why it was there: the record keeps its count, so a source
// that comes back earns the longer ban next time. `unban` lifts any ban, `unblock` only the operator's
// permanent one.
func (a *application) runBansLift(args []string, command string) int {
	flags := a.newFlagSet("bans "+command, map[string]string{
		"unban": "Lifts a ban now. The ladder remembers the offence, so a repeat earns the longer ban: this is " +
			"not a way to forget what happened.",
		"unblock": "Lifts the permanent block you put on an address with `bans block`. The record keeps the " +
			"offence, like `bans unban`.",
	}[command])
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(a.stderr, "termit-uv bans %s: one address is required\n", command)
		return exitUsage
	}
	cfg, banList, code := a.loadBans(*configFlag)
	if code != exitOK {
		return code
	}
	prefix, err := firewall.ParsePrefix(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans %s: %v\n", command, err)
		return exitUsage
	}
	address := prefix.Addr().String()
	record, found := banList.Get(address)
	now := a.now()
	if !found || !record.Active(now) {
		fmt.Fprintf(a.stdout, "bans:       %s is not banned\n", address)
		return exitOK
	}
	if command == "unblock" && !record.Permanent() {
		fmt.Fprintf(a.stderr, "termit-uv bans unblock: %s is banned by %s, not by you; use `bans unban`\n",
			address, record.Signal)
		return exitUsage
	}
	record.Until = now.UTC()
	banList.Put(record)
	if err := firewall.SaveBans(cfg.Paths.BansPath(), banList); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans %s: %v\n", command, err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "bans:       %s is no longer banned (offence %d kept)\n", address, record.Count)
	a.removeBanNow(cfg, address)
	return exitOK
}

// loadBans reads the configuration and the record, or reports which one failed.
func (a *application) loadBans(configPath string) (*config.Config, *firewall.Bans, int) {
	cfg, err := a.loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans: %v\n", err)
		return nil, nil, exitFailure
	}
	banList, err := firewall.LoadBans(cfg.Paths.BansPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans: %v\n", err)
		return nil, nil, exitFailure
	}
	return cfg, banList, exitOK
}

// checkBannable refuses an address a ban would make self-inflicted: loopback, the private ranges, the
// management server and the operator's own ssh_from.
func (a *application) checkBannable(cfg *config.Config, raw string) (string, error) {
	prefix, err := firewall.ParsePrefix(raw)
	if err != nil {
		return "", err
	}
	extra, _ := firewall.Prefixes(cfg.Security.Firewall.SSHFrom)
	never := firewall.NewNeverBan(extra...)
	if never.Covers(prefix.Addr()) {
		return "", fmt.Errorf("%s is loopback, a private or reserved range, the management server or your own "+
			"ssh_from: banning it would cut you off, not an attacker", prefix.Addr())
	}
	return prefix.Addr().String(), nil
}

// applyBanNow updates the kernel right away, so a command takes effect without waiting for the guard's
// next pass. With no table applied there is nothing to update, and the record is what matters.
func (a *application) applyBanNow(cfg *config.Config, ip string, ttl time.Duration) {
	ctx := context.Background()
	present, err := a.nftApplier().Present(ctx, firewall.NodeTable)
	if err != nil || !present {
		return
	}
	if err := a.nftApplier().AddElement(ctx, firewall.NodeTable, banSetFor(ip), ip, ttl); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans: %v\n", err)
	}
}

// removeBanNow takes the address out of the kernel's sets, if it was ever there. The scan sets go with
// it: the meter remembers for minutes, and an unban that the next cycle undoes is not an unban.
func (a *application) removeBanNow(cfg *config.Config, ip string) {
	ctx := context.Background()
	present, err := a.nftApplier().Present(ctx, firewall.NodeTable)
	if err != nil || !present {
		return
	}
	if err := a.nftApplier().RemoveBan(ctx, firewall.NodeTable, ip); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans: %v\n", err)
	}
	if err := a.nftApplier().ForgetScan(ctx, firewall.NodeTable, ip); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv bans: %v\n", err)
	}
}

// banSetFor picks the ban set for an address, the way the firewall package renders them.
func banSetFor(ip string) string {
	prefix, err := firewall.ParsePrefix(ip)
	if err == nil && prefix.Addr().Is6() && !prefix.Addr().Is4In6() {
		return "banned6"
	}
	return "banned4"
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

// describeBans is the one-line summary `status` prints.
func describeBans(bans *firewall.Bans, now time.Time) string {
	active := bans.Active(now)
	if len(active) == 0 {
		return "no bans"
	}
	permanent := 0
	for _, ban := range active {
		if ban.Permanent() {
			permanent++
		}
	}
	out := fmt.Sprintf("%d bans", len(active))
	if permanent > 0 {
		out += fmt.Sprintf(" (%d permanent)", permanent)
	}
	return out
}
