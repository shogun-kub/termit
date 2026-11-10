package main

import (
	"errors"
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
}

// menu is the interactive front end for the server and its admin CLI.
type menu struct {
	ui  *termui.UI
	run func([]string) int
}

// runMenu is `mn` with no arguments on a terminal, and `termit-mn menu` always.
func runMenu(stdout, stderr *os.File, in io.Reader, style termui.Style) int {
	m := &menu{
		ui:  termui.New(in, stdout, style),
		run: func(args []string) int { return run(args, stdout, stderr) },
	}
	return m.loop()
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

		plan, err := entries[index].plan(m.ui)
		switch {
		case err != nil:
			m.ui.Error(err.Error())
		case plan == nil:
			m.ui.Say("cancelled")
		default:
			if code := m.runPlan(plan); code != exitOK {
				_ = m.ui.Pause()
				continue
			}
		}
		if err := m.ui.Pause(); err != nil {
			return exitOK
		}
	}
}

func (m *menu) runPlan(plan [][]string) int {
	for _, argv := range plan {
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
	switch {
	case os.Getenv(adminTokenEnv) != "":
		m.ui.State("ok", "admin token present in "+adminTokenEnv)
	case os.Getenv(adminTokenFileEnv) != "":
		m.ui.State("ok", "admin token file named in "+adminTokenFileEnv)
	default:
		m.ui.State("warn", "no admin token: admin actions will refuse to run")
		m.ui.Hint("export " + adminTokenEnv + "=<token>, or " + adminTokenFileEnv + "=<path>, before the menu")
	}
	m.ui.Blank()
}

// entries is the menu in order. Server lifecycle (serve) is deliberately absent: the unit runs it, and
// starting a long-running server from a menu would hide its log.
func (m *menu) entries() []menuEntry {
	return []menuEntry{
		{"status", "server, nodes and the check schedule", fixedMn("status")},
		{"nodes", "list, inspect, approve, block, remove", m.planNodes},
		{"invites", "create, list, revoke codes", m.planInvites},
		{"check now", "one node, or the whole mesh", m.planCheck},
		{"policy", "show and change scheduler thresholds", m.planPolicy},
		{"subscription", "show or rotate a node's subscription token", m.planSubscription},
		{"backup", "write a consistent snapshot", fixedMn("backup")},
		{"help", "every command and flag", fixedMn("help")},
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
