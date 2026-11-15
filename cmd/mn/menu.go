package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/version"
)

// menuEntry is one line of the interactive menu, exactly as in termit-uv: it asks what a flag would
// ask and then runs the command line a script would run.
type menuEntry struct {
	labelKey i18n.Key
	hintKey  i18n.Key
	plan     func(ui *termui.UI) ([][]string, error)
	// local marks an entry whose command takes no admin flags: the menu's own flags are not passed to it,
	// because a flag set that never declared them refuses them.
	local bool
}

// labelOf is the entry's label in one language, so a test can find an entry without depending on its
// position or on the machine's locale.
func (e menuEntry) labelOf(tr i18n.Translator) string { return tr.T(e.labelKey) }

// menu is the interactive front end for the server and its admin CLI.
type menu struct {
	ui  *termui.UI
	run func([]string) int
	// base is the argv every admin command inherits: the flags the operator gave the menu itself.
	base []string
	// credential names where the admin token comes from, or "" when nothing is configured.
	credential string
	// tr is the language the operator is spoken to in, resolved once when the menu opens.
	tr i18n.Translator
}

// translator is the menu's language. It is a method so a test can build a menu without opening one.
func (m *menu) translator() i18n.Translator {
	if m.tr.Lang() == "" {
		return i18n.New(i18n.Console(i18n.Default, os.Getenv))
	}
	return m.tr
}

// runMenu is `mn` with no arguments on a terminal, and `termit-mn menu` always.
//
// Flags after `menu` reach every command the menu runs, the way a script would pass them. They used to be
// dropped: `termit-mn menu --token-file /etc/termit/mn/admin.token` warned that no token was configured
// while holding one, and every admin action then refused to run.
func runMenu(stdout, stderr *os.File, in io.Reader, style termui.Style, args []string) int {
	flags, configPath, _, token, tokenFile := inviteFlags("termit-mn menu", stderr)
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
	// The menu speaks the configured language where this console can show it; see the node's menu for
	// the rule. The CLI output itself stays English either way.
	lang := i18n.Default
	if cfg, err := config.Load(*configPath); err == nil {
		lang = cfg.Lang()
	}
	m := &menu{
		ui:         termui.New(in, stdout, style),
		run:        func(argv []string) int { return run(argv, stdout, stderr) },
		base:       setFlagArgs(flags),
		credential: source,
		tr:         i18n.New(i18n.Console(lang, os.Getenv)),
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
			items[i] = termui.Item{Label: m.translator().T(entry.labelKey), Hint: m.translator().T(entry.hintKey)}
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
			m.ui.Say(m.translator().T(i18n.MenuCancelled))
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
			m.ui.Error(m.translator().Tf(i18n.MenuStepFailed, code))
			return code
		}
	}
	return exitOK
}

// header names the server and tells the operator what the admin commands need.
func (m *menu) header() {
	tr := m.translator()
	m.ui.Title(tr.T(i18n.MnTitle))
	m.ui.Hint(version.String("termit-mn"))
	m.ui.Blank()
	switch m.credential {
	case "":
		m.ui.State("warn", tr.T(i18n.MnNoToken))
		m.ui.Hint(tr.Tf(i18n.MnTokenHint, adminTokenEnv, adminTokenFileEnv))
	default:
		m.ui.State("ok", tr.Tf(i18n.MnTokenFrom, m.credential))
	}
	m.ui.Blank()
}

// entries is the menu in order. Server lifecycle (serve) is deliberately absent: the unit runs it, and
// starting a long-running server from a menu would hide its log.
func (m *menu) entries() []menuEntry {
	return []menuEntry{
		{labelKey: i18n.MnEntryStatusLabel, hintKey: i18n.MnEntryStatusHint, plan: fixedMn("status")},
		{labelKey: i18n.MnEntryNodesLabel, hintKey: i18n.MnEntryNodesHint, plan: m.planNodes},
		{labelKey: i18n.MnEntryInvitesLabel, hintKey: i18n.MnEntryInvitesHint, plan: m.planInvites},
		{labelKey: i18n.MnEntryCheckLabel, hintKey: i18n.MnEntryCheckHint, plan: m.planCheck},
		{labelKey: i18n.MnEntryPolicyLabel, hintKey: i18n.MnEntryPolicyHint, plan: m.planPolicy},
		{labelKey: i18n.MnEntrySubLabel, hintKey: i18n.MnEntrySubHint, plan: m.planSubscription},
		{labelKey: i18n.MnEntryAdminLabel, hintKey: i18n.MnEntryAdminHint, plan: m.planAdminToken},
		// These two take no admin flags: `backup` writes a local snapshot, `help` prints the usage.
		{labelKey: i18n.MnEntryBackupLabel, hintKey: i18n.MnEntryBackupHint, plan: fixedMn("backup"), local: true},
		{labelKey: i18n.MnEntryHelpLabel, hintKey: i18n.MnEntryHelpHint, plan: fixedMn("help"), local: true},
	}
}

func fixedMn(argv ...string) func(*termui.UI) ([][]string, error) {
	return func(*termui.UI) ([][]string, error) { return [][]string{argv}, nil }
}

func (m *menu) planNodes(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: tr.T(i18n.MnNodesListLabel), Hint: tr.T(i18n.MnNodesListHint)},
		{Label: tr.T(i18n.MnNodesShowLabel), Hint: tr.T(i18n.MnNodesShowHint)},
		{Label: tr.T(i18n.MnNodesApproveLabel), Hint: tr.T(i18n.MnNodesApproveHint)},
		{Label: tr.T(i18n.MnNodesRejectLabel), Hint: tr.T(i18n.MnNodesRejectHint)},
		{Label: tr.T(i18n.MnNodesBlockLabel), Hint: tr.T(i18n.MnNodesBlockHint)},
		{Label: tr.T(i18n.MnNodesUnblockLabel), Hint: tr.T(i18n.MnNodesUnblockHint)},
		{Label: tr.T(i18n.MnNodesRemoveLabel), Hint: tr.T(i18n.MnNodesRemoveHint)},
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
	node, err := ui.Prompt(tr.T(i18n.MnPromptNode), "")
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, errors.New(i18n.New(i18n.EN).T(i18n.MnErrNodeRequired))
	}
	argv := []string{"uv", verb, node}
	if verb == "rm" || verb == "block" {
		reason, err := ui.Prompt(tr.T(i18n.MnPromptReason), "")
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
	tr := m.translator()
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: tr.T(i18n.MnInvitesListLabel), Hint: tr.T(i18n.MnInvitesListHint)},
		{Label: tr.T(i18n.MnInvitesCreateLabel), Hint: tr.T(i18n.MnInvitesCreateHint)},
		{Label: tr.T(i18n.MnInvitesRevokeLabel), Hint: tr.T(i18n.MnInvitesRevokeHint)},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	switch choice {
	case "1":
		return [][]string{{"invite", "list"}}, nil
	case "2":
		note, err := ui.Prompt(tr.T(i18n.MnPromptNote), "")
		if err != nil {
			return nil, err
		}
		argv := []string{"invite", "create"}
		if note != "" {
			argv = append(argv, "--note", note)
		}
		return [][]string{argv}, nil
	default:
		code, err := ui.Prompt(tr.T(i18n.MnPromptInviteCode), "")
		if err != nil {
			return nil, err
		}
		if code == "" {
			return nil, errors.New(i18n.New(i18n.EN).T(i18n.MnErrCodeRequired))
		}
		return [][]string{{"invite", "revoke", code}}, nil
	}
}

func (m *menu) planCheck(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	whole, err := ui.Confirm(tr.T(i18n.MnPromptCheckAll), true)
	if err != nil {
		return nil, err
	}
	if whole {
		return [][]string{{"check", "--all"}}, nil
	}
	node, err := ui.Prompt(tr.T(i18n.MnPromptNode), "")
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, errors.New(i18n.New(i18n.EN).T(i18n.MnErrNodeRequired))
	}
	return [][]string{{"check", node}}, nil
}

func (m *menu) planPolicy(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: tr.T(i18n.MnPolicyShowLabel), Hint: tr.T(i18n.MnPolicyShowHint)},
		{Label: tr.T(i18n.MnPolicySetLabel), Hint: tr.T(i18n.MnPolicySetHint)},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	if choice == "1" {
		return [][]string{{"policy", "show"}}, nil
	}
	key, err := ui.Prompt(tr.T(i18n.MnPromptSetting), "")
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New(i18n.New(i18n.EN).T(i18n.MnErrSettingRequired))
	}
	value, err := ui.Prompt(tr.T(i18n.MnPromptValue), "")
	if err != nil {
		return nil, err
	}
	return [][]string{{"policy", "set", key, value}}, nil
}

// planAdminToken rotates the credential the menu itself is running on. It asks first, and says what
// breaks: every timer and script that still carries the old token stops working at that moment.
func (m *menu) planAdminToken(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	out, err := ui.Confirm(tr.T(i18n.MnPromptRotateAdmin), false)
	if err != nil {
		return nil, err
	}
	if !out {
		return nil, nil
	}
	path, err := ui.Prompt(tr.T(i18n.MnPromptTokenFile), "")
	if err != nil {
		return nil, err
	}
	if path == "" {
		return [][]string{{"admin", "rotate"}}, nil
	}
	return [][]string{{"admin", "rotate", "--token-out", path}}, nil
}

func (m *menu) planSubscription(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: tr.T(i18n.MnSubRotateLabel), Hint: tr.T(i18n.MnSubRotateHint)},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	if choice != "1" {
		return nil, fmt.Errorf("%q is not one of the choices", choice)
	}
	node, err := ui.Prompt(tr.T(i18n.MnPromptNode), "")
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, errors.New(i18n.New(i18n.EN).T(i18n.MnErrNodeRequired))
	}
	domain, err := ui.Prompt(tr.T(i18n.MnPromptPublicName), "")
	if err != nil {
		return nil, err
	}
	argv := []string{"sub", "rotate", node}
	if domain != "" {
		argv = append(argv, "--domain", domain)
	}
	return [][]string{argv}, nil
}
