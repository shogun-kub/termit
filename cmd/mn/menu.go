package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/version"
)

// menuEntry is one line of the interactive menu, exactly as in termit-uv: it asks what a flag would
// ask and then runs the command line a script would run.
type menuEntry struct {
	label string
	hint  string
	plan  func(ui *termui.UI) ([][]string, error)
	// local marks an entry whose command takes no admin flags: the menu's own flags are not passed to it,
	// because a flag set that never declared them refuses them.
	local bool
}

// menu is the interactive front end for the server and its admin CLI.
type menu struct {
	ui  *termui.UI
	run func([]string) int
	// base is the argv every admin command inherits: the flags the operator gave the menu itself.
	base []string
	// credential names where the admin token comes from, or "" when nothing is configured.
	credential string
}

// runMenu is `mn` with no arguments on a terminal, and `termit-mn menu` always.
//
// Flags after `menu` reach every command the menu runs, the way a script would pass them. They used to be
// dropped: `termit-mn menu --token-file /etc/termit/mn/admin.token` warned that no token was configured
// while holding one, and every admin action then refused to run.
func runMenu(stdout, stderr *os.File, in io.Reader, style termui.Style, args []string) int {
	flags, _, _, token, tokenFile := inviteFlags("termit-mn menu", stderr)
	if err := flags.Parse(flagsFirst(args, map[string]bool{})); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn menu: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	// Resolved once: a contradictory pair of flags or an unreadable file is a usage error, not something
	// to discover one menu click later, and the header gets the name of the credential in use.
	_, source, err := resolveAdminTokenSource(*token, *tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn menu: %v\n", err)
		return exitUsage
	}
	m := &menu{
		ui:         termui.New(in, stdout, style),
		run:        func(argv []string) int { return run(argv, stdout, stderr) },
		base:       setFlagArgs(flags),
		credential: source,
	}
	return m.loop()
}

// setFlagArgs renders the flags the operator actually gave as argv, so the commands the menu runs inherit
// them. Only set flags are included: an unset one has to keep each command's own environment lookup
// rather than freeze a default into the menu.
func setFlagArgs(flags *flag.FlagSet) []string {
	var out []string
	flags.Visit(func(f *flag.Flag) {
		out = append(out, "--"+f.Name, f.Value.String())
	})
	return out
}

// withFlags appends the inherited flags to a command. They go after the plan's own words — `uv list`,
// `check now <domain>` — because the first word selects the subcommand; every admin command reorders its
// flags anyway (`flagsFirst`), so the tail is the one place that is always right.
func withFlags(argv, base []string) []string {
	if len(base) == 0 || len(argv) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+len(base))
	out = append(out, argv...)
	return append(out, base...)
}

func (m *menu) loop() int {
	entries := m.entries()
	for {
		m.ui.Clear()
		m.header()
		items := make([]termui.Item, len(entries))
		for i, entry := range entries {
			items[i] = termui.Item{Label: entry.label, Hint: entry.hint}
		}
		choice, err := m.ui.Menu(items)
		if errors.Is(err, io.EOF) {
			return exitOK
		}
		if err != nil {
			m.ui.Error(fmt.Sprintf("menu: %v", err))
			return exitFailure
		}
		if choice == "" {
			return exitOK
		}
		index, err := termui.Index(choice)
		if err != nil || index >= len(entries) {
			m.ui.Error(fmt.Sprintf("%q is not one of the choices", choice))
			continue
		}

		entry := entries[index]
		plan, err := entry.plan(m.ui)
		switch {
		case err != nil:
			m.ui.Error(err.Error())
		case plan == nil:
			m.ui.Say("cancelled")
		default:
			if code := m.runPlan(plan, !entry.local); code != exitOK {
				_ = m.ui.Pause()
				continue
			}
		}
		if err := m.ui.Pause(); err != nil {
			return exitOK
		}
	}
}

func (m *menu) runPlan(plan [][]string, inherit bool) int {
	for _, argv := range plan {
		if inherit {
			argv = withFlags(argv, m.base)
		}
		m.ui.Blank()
		m.ui.Say("$ termit-mn " + strings.Join(argv, " "))
		m.ui.Blank()
		if code := m.run(argv); code != exitOK {
			m.ui.Blank()
			m.ui.Error(fmt.Sprintf("that step exited with status %d", code))
			return code
		}
	}
	return exitOK
}

// header names the server and tells the operator what the admin commands need.
func (m *menu) header() {
	m.ui.Title("Termit — server")
	m.ui.Hint(version.String("termit-mn"))
	m.ui.Blank()
	switch m.credential {
	case "":
		m.ui.State("warn", "no admin token: admin actions will refuse to run")
		m.ui.Hint("pass --token-file <path> to the menu, or export " + adminTokenEnv + " / " + adminTokenFileEnv)
	default:
		m.ui.State("ok", "admin token from "+m.credential)
	}
	m.ui.Blank()
}

// entries is the menu in order. Server lifecycle (serve) is deliberately absent: the unit runs it, and
// starting a long-running server from a menu would hide its log.
func (m *menu) entries() []menuEntry {
	return []menuEntry{
		{label: "status", hint: "server, nodes and the check schedule", plan: fixedMn("status")},
		{label: "nodes", hint: "list, inspect, approve, block, remove", plan: m.planNodes},
		{label: "invites", hint: "create, list, revoke codes", plan: m.planInvites},
		{label: "check now", hint: "one node, or the whole mesh", plan: m.planCheck},
		{label: "policy", hint: "show and change scheduler thresholds", plan: m.planPolicy},
		{label: "subscription", hint: "show or rotate a node's subscription token", plan: m.planSubscription},
		{label: "admin token", hint: "rotate the credential every admin command uses", plan: m.planAdminToken},
		// These two take no admin flags: `backup` writes a local snapshot, `help` prints the usage.
		{label: "backup", hint: "write a consistent snapshot", plan: fixedMn("backup"), local: true},
		{label: "help", hint: "every command and flag", plan: fixedMn("help"), local: true},
	}
}

func fixedMn(argv ...string) func(*termui.UI) ([][]string, error) {
	return func(*termui.UI) ([][]string, error) { return [][]string{argv}, nil }
}

func (m *menu) planNodes(ui *termui.UI) ([][]string, error) {
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: "list", Hint: "domain, state, last check"},
		{Label: "show", Hint: "everything about one node"},
		{Label: "approve", Hint: "accept a pending node"},
		{Label: "reject", Hint: "refuse a pending node"},
		{Label: "block", Hint: "stop serving it in subscriptions"},
		{Label: "unblock", Hint: "let it back in"},
		{Label: "remove", Hint: "drop it from the registry"},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	verb := map[string]string{
		"1": "list", "2": "show", "3": "approve", "4": "reject",
		"5": "block", "6": "unblock", "7": "rm",
	}[choice]
	if verb == "" {
		return nil, fmt.Errorf("%q is not one of the choices", choice)
	}
	if verb == "list" {
		return [][]string{{"uv", "list"}}, nil
	}
	node, err := ui.Prompt("Node id or domain", "")
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, errors.New("a node id or domain is required")
	}
	argv := []string{"uv", verb, node}
	if verb == "rm" || verb == "block" {
		reason, err := ui.Prompt("Reason, recorded with the state change", "")
		if err != nil {
			return nil, err
		}
		if reason != "" {
			argv = append(argv, "--reason", reason)
		}
	}
	return [][]string{argv}, nil
}

func (m *menu) planInvites(ui *termui.UI) ([][]string, error) {
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: "list", Hint: "codes, uses left and expiry"},
		{Label: "create", Hint: "print a code once for one new node"},
		{Label: "revoke", Hint: "make a code unusable"},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	switch choice {
	case "1":
		return [][]string{{"invite", "list"}}, nil
	case "2":
		note, err := ui.Prompt("Note, so you remember who it is for", "")
		if err != nil {
			return nil, err
		}
		argv := []string{"invite", "create"}
		if note != "" {
			argv = append(argv, "--note", note)
		}
		return [][]string{argv}, nil
	default:
		code, err := ui.Prompt("Invite code to revoke", "")
		if err != nil {
			return nil, err
		}
		if code == "" {
			return nil, errors.New("a code is required")
		}
		return [][]string{{"invite", "revoke", code}}, nil
	}
}

func (m *menu) planCheck(ui *termui.UI) ([][]string, error) {
	whole, err := ui.Confirm("Check every node instead of one?", true)
	if err != nil {
		return nil, err
	}
	if whole {
		return [][]string{{"check", "--all"}}, nil
	}
	node, err := ui.Prompt("Node id or domain", "")
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, errors.New("a node id or domain is required")
	}
	return [][]string{{"check", node}}, nil
}

func (m *menu) planPolicy(ui *termui.UI) ([][]string, error) {
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: "show", Hint: "thresholds in force and where they come from"},
		{Label: "set", Hint: "change one threshold"},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	if choice == "1" {
		return [][]string{{"policy", "show"}}, nil
	}
	key, err := ui.Prompt("Setting, for example check_interval", "")
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New("a setting name is required")
	}
	value, err := ui.Prompt("Value, empty to clear the override", "")
	if err != nil {
		return nil, err
	}
	return [][]string{{"policy", "set", key, value}}, nil
}

// planAdminToken rotates the credential the menu itself is running on. It asks first, and says what
// breaks: every timer and script that still carries the old token stops working at that moment.
func (m *menu) planAdminToken(ui *termui.UI) ([][]string, error) {
	out, err := ui.Confirm("Rotate the admin token? Every script and timer using it stops working now", false)
	if err != nil {
		return nil, err
	}
	if !out {
		return nil, nil
	}
	path, err := ui.Prompt("Write the new token to this file (empty to print it once)", "")
	if err != nil {
		return nil, err
	}
	if path == "" {
		return [][]string{{"admin", "rotate"}}, nil
	}
	return [][]string{{"admin", "rotate", "--token-out", path}}, nil
}

func (m *menu) planSubscription(ui *termui.UI) ([][]string, error) {
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: "rotate", Hint: "print a new token; the old URL stops working"},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	if choice != "1" {
		return nil, fmt.Errorf("%q is not one of the choices", choice)
	}
	node, err := ui.Prompt("Node id or domain", "")
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, errors.New("a node id or domain is required")
	}
	domain, err := ui.Prompt("Server's public name, for the URL (empty to leave it out)", "")
	if err != nil {
		return nil, err
	}
	argv := []string{"sub", "rotate", node}
	if domain != "" {
		argv = append(argv, "--domain", domain)
	}
	return [][]string{argv}, nil
}
