package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/menuplan"
	"github.com/shogun-kub/termit/internal/mn/admin"
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
	// children, when set, opens a level instead of running a command: the entry is a group.
	children func() []menuEntry
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
	// nodes is how the picker sees the mesh. It is a seam: production asks the admin socket, a test
	// answers from a table, and nil means the server cannot be asked at all.
	nodes func(context.Context) ([]admin.NodeView, error)
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
	flags, configPath, socket, token, tokenFile := inviteFlags("termit-mn menu", stderr)
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
		nodes: func(ctx context.Context) ([]admin.NodeView, error) {
			client, err := adminClient(*configPath, *socket, *token, *tokenFile)
			if err != nil {
				return nil, err
			}
			var nodes []admin.NodeView
			if err := client.Call(ctx, "uv.list", nil, &nodes); err != nil {
				return nil, err
			}
			return nodes, nil
		},
		tr: i18n.New(i18n.Console(lang, os.Getenv)),
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
	levels := []menuLevel{{entries: m.entries()}}
	for {
		level := levels[len(levels)-1]
		m.ui.Clear()
		m.header()
		tr := m.translator()
		if level.title != "" {
			m.ui.Title(tr.T(level.title))
		}
		items := make([]termui.Item, 0, len(level.entries)+1)
		for _, entry := range level.entries {
			items = append(items, termui.Item{Label: tr.T(entry.labelKey), Hint: tr.T(entry.hintKey)})
		}
		if len(levels) > 1 {
			items = append(items, termui.Item{Key: "0", Label: tr.T(i18n.MenuBackLabel), Hint: tr.T(i18n.MenuBackHint)})
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
		if choice == "0" && len(levels) > 1 {
			levels = levels[:len(levels)-1]
			continue
		}
		index, err := termui.Index(choice)
		if err != nil || index >= len(level.entries) {
			m.ui.Error(fmt.Sprintf("%q is not one of the choices", choice))
			continue
		}

		entry := level.entries[index]
		if entry.children != nil {
			levels = append(levels, menuLevel{entries: entry.children(), title: entry.labelKey})
			continue
		}
		plan, err := entry.plan(m.ui)
		switch {
		case err != nil:
			m.ui.Error(err.Error())
		case plan == nil:
			m.ui.Say(tr.T(i18n.MenuCancelled))
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

// menuLevel is one screen: a list of entries and the group it belongs to, which the header repeats so a
// deep screen still says where the operator is.
type menuLevel struct {
	entries []menuEntry
	title   i18n.Key
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
		{labelKey: i18n.MnGroupStateLabel, hintKey: i18n.MnGroupStateHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MnEntryStatusLabel, hintKey: i18n.MnEntryStatusHint, plan: fixedMn("status")},
				{labelKey: i18n.MnEntryCheckLabel, hintKey: i18n.MnEntryCheckHint, plan: m.planCheck},
				{labelKey: i18n.MnEntryPolicyLabel, hintKey: i18n.MnEntryPolicyHint, plan: m.planPolicy},
				{labelKey: i18n.MnEntryGuardLabel, hintKey: i18n.MnEntryGuardHint, plan: fixedMn("guard", "status"), local: true},
			}
		}},
		// The node verbs are leaves of their own: the picker asks which node, so a menu of verbs is one
		// screen and each verb is one command.
		{labelKey: i18n.MnEntryNodesLabel, hintKey: i18n.MnEntryNodesHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MnNodesListLabel, hintKey: i18n.MnNodesListHint, plan: fixedMn("uv", "list")},
				{labelKey: i18n.MnNodesShowLabel, hintKey: i18n.MnNodesShowHint, plan: m.nodeVerb("show")},
				{labelKey: i18n.MnNodesApproveLabel, hintKey: i18n.MnNodesApproveHint, plan: m.nodeVerb("approve")},
				{labelKey: i18n.MnNodesRejectLabel, hintKey: i18n.MnNodesRejectHint, plan: m.nodeVerb("reject")},
				{labelKey: i18n.MnNodesBlockLabel, hintKey: i18n.MnNodesBlockHint, plan: m.nodeVerb("block")},
				{labelKey: i18n.MnNodesUnblockLabel, hintKey: i18n.MnNodesUnblockHint, plan: m.nodeVerb("unblock")},
				{labelKey: i18n.MnNodesRemoveLabel, hintKey: i18n.MnNodesRemoveHint, plan: m.nodeVerb("rm")},
				{labelKey: i18n.MnNodesReissueLabel, hintKey: i18n.MnNodesReissueHint, plan: m.nodeVerb("reissue")},
				{labelKey: i18n.MnNodesAgentKeyLabel, hintKey: i18n.MnNodesAgentKeyHint, plan: m.nodeVerb("agentkey")},
				{labelKey: i18n.MnNodesForgetLabel, hintKey: i18n.MnNodesForgetHint, plan: m.nodeVerb("forget")},
			}
		}},
		{labelKey: i18n.MnEntryInvitesLabel, hintKey: i18n.MnEntryInvitesHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MnInvitesListLabel, hintKey: i18n.MnInvitesListHint, plan: fixedMn("invite", "list")},
				{labelKey: i18n.MnInvitesCreateLabel, hintKey: i18n.MnInvitesCreateHint, plan: m.planInviteCreate},
				{labelKey: i18n.MnInvitesRevokeLabel, hintKey: i18n.MnInvitesRevokeHint, plan: m.planInviteRevoke},
				{labelKey: i18n.MnInvitesPolicyLabel, hintKey: i18n.MnInvitesPolicyHint, plan: m.planInvitePolicy},
			}
		}},
		{labelKey: i18n.MnEntrySubLabel, hintKey: i18n.MnEntrySubHint, plan: m.planSubscription},
		// The hardening surface is local: these commands read the host, not the API, so the admin flags the
		// menu carries would be refused by their flag sets.
		{labelKey: i18n.MnGroupDefenceLabel, hintKey: i18n.MnGroupDefenceHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MnEntryFirewallLabel, hintKey: i18n.MnEntryFirewallHint,
					children: func() []menuEntry { return optionsAsEntries(menuplan.Firewall(), m.translator()) }},
				{labelKey: i18n.MnEntryBansLabel, hintKey: i18n.MnEntryBansHint,
					children: func() []menuEntry { return optionsAsEntries(menuplan.Bans(), m.translator()) }},
			}
		}},
		{labelKey: i18n.MnGroupServiceLabel, hintKey: i18n.MnGroupServiceHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MnEntryAdminLabel, hintKey: i18n.MnEntryAdminHint, plan: m.planAdminToken},
				{labelKey: i18n.MnEntryUpdateLabel, hintKey: i18n.MnEntryUpdateHint, plan: m.planUpdate, local: true},
				{labelKey: i18n.MnEntryBackupLabel, hintKey: i18n.MnEntryBackupHint, plan: fixedMn("backup"), local: true},
				{labelKey: i18n.MnEntryRestoreLabel, hintKey: i18n.MnEntryRestoreHint, plan: m.planRestore, local: true},
			}
		}},
		{labelKey: i18n.MnEntryHelpLabel, hintKey: i18n.MnEntryHelpHint, plan: fixedMn("help"), local: true},
	}
}

// optionsAsEntries turns a shared table into a level; the questions an option declares are asked when the
// option is chosen. Firewall and bans are host commands, so their leaves take no admin flags.
func optionsAsEntries(options []menuplan.Option, tr i18n.Translator) []menuEntry {
	entries := make([]menuEntry, len(options))
	for i, option := range options {
		option := option
		entries[i] = menuEntry{labelKey: option.Label, hintKey: option.Hint, local: true,
			plan: func(ui *termui.UI) ([][]string, error) { return option.Run(ui, tr) }}
	}
	return entries
}

// nodeVerb returns the plan for one `uv` verb: which node, then the reason or the confirmation the verb
// needs. It is the whole of the old node sub-menu, one verb at a time.
func (m *menu) nodeVerb(verb string) func(*termui.UI) ([][]string, error) {
	return func(ui *termui.UI) ([][]string, error) {
		tr := m.translator()
		node, err := m.pickNode(ui)
		if err != nil {
			return nil, err
		}
		if node == "" {
			return nil, nil
		}
		argv := []string{"uv", verb, node}
		switch verb {
		case "rm", "block":
			reason, err := ui.Prompt(tr.T(i18n.MnPromptReason), "")
			if err != nil {
				return nil, err
			}
			if reason != "" {
				argv = append(argv, "--reason", reason)
			}
		case "reissue", "forget":
			// Both break something: a new key stops the old one, and a forgotten record stops the node
			// itself from syncing until it enrolls again.
			question := i18n.PromptReissue
			if verb == "forget" {
				question = i18n.PromptForget
			}
			sure, err := ui.Confirm(tr.T(question), false)
			if err != nil {
				return nil, err
			}
			if !sure {
				return nil, nil
			}
		}
		return [][]string{argv}, nil
	}
}

// planInviteCreate makes a code, with a note so the operator remembers who it was for.
func (m *menu) planInviteCreate(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	note, err := ui.Prompt(tr.T(i18n.MnPromptNote), "")
	if err != nil {
		return nil, err
	}
	argv := []string{"invite", "create"}
	if note != "" {
		argv = append(argv, "--note", note)
	}
	return [][]string{argv}, nil
}

// planInvitePolicy picks the rule a new node meets: a code, a code plus an approval, or nothing at all.
// The last one widens the door, so it asks a second time; the command itself refuses it without --force,
// and the menu passes the flag only after that answer.
func (m *menu) planInvitePolicy(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: tr.T(i18n.MnPolicyByInviteLabel), Hint: tr.T(i18n.MnPolicyByInviteHint)},
		{Label: tr.T(i18n.MnPolicyByApprovalLabel), Hint: tr.T(i18n.MnPolicyByApprovalHint)},
		{Label: tr.T(i18n.MnPolicyOpenLabel), Hint: tr.T(i18n.MnPolicyOpenHint)},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	switch choice {
	case "1":
		return [][]string{{"invite", "policy", "--mode", config.ModeInvite}}, nil
	case "2":
		return [][]string{{"invite", "policy", "--mode", config.ModeInviteApproval}}, nil
	}
	sure, err := ui.Confirm(tr.T(i18n.MnPromptOpenMode), false)
	if err != nil {
		return nil, err
	}
	if !sure {
		return nil, nil
	}
	return [][]string{{"invite", "policy", "--mode", config.ModeOpen, "--force"}}, nil
}

// planInviteRevoke revokes a code. An empty answer cancels: the code is a thing the operator either has
// or does not.
func (m *menu) planInviteRevoke(ui *termui.UI) ([][]string, error) {
	code, err := ui.Prompt(m.translator().T(i18n.MnPromptInviteCode), "")
	if err != nil {
		return nil, err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, nil
	}
	return [][]string{{"invite", "revoke", code}}, nil
}
func fixedMn(argv ...string) func(*termui.UI) ([][]string, error) {
	return func(*termui.UI) ([][]string, error) { return [][]string{argv}, nil }
}

// pickNode asks which node an action is about. The mesh is drawn as a list, because a domain is what an
// operator remembers and an id is not; a number, a domain, an id or any unique part of a domain answers
// the question, and an empty answer cancels. When the server cannot be asked the list is skipped: the
// admin CLI resolves a domain on its own, so the action is still possible.
func (m *menu) pickNode(ui *termui.UI) (string, error) {
	tr := m.translator()
	nodes, err := m.meshNodes()
	if err != nil {
		ui.Say(tr.T(i18n.MnPickNodeOffline))
		return m.promptNode(ui)
	}
	if len(nodes) == 0 {
		ui.Say(tr.T(i18n.MnPickNodeNone))
		return "", nil
	}

	items := make([]termui.Item, 0, len(nodes)+1)
	for _, node := range nodes {
		items = append(items, termui.Item{Label: node.Domain, Hint: string(node.State)})
	}
	items = append(items, termui.Item{Key: "0", Label: tr.T(i18n.MnPickNodeCancel)})

	ui.Blank()
	ui.Say(tr.T(i18n.MnPickNodePrompt))
	for {
		answer, err := ui.MenuFree(items)
		if err != nil {
			return "", err
		}
		if answer == "" || answer == "0" {
			return "", nil
		}
		if index, err := termui.Index(answer); err == nil {
			if index < len(nodes) {
				return nodes[index].Domain, nil
			}
			continue
		}
		switch matches := matchNodes(nodes, answer); len(matches) {
		case 1:
			return matches[0].Domain, nil
		case 0:
			ui.Error(tr.Tf(i18n.MnPickNodeMiss, answer))
		default:
			ui.Say(tr.Tf(i18n.MnPickNodeMany, len(matches)))
		}
	}
}

// matchNodes answers "which node did they mean" for anything that is not a menu number: an exact id or
// domain, then a part of a domain. It never guesses between two matches.
func matchNodes(nodes []admin.NodeView, typed string) []admin.NodeView {
	typed = strings.ToLower(strings.TrimSpace(typed))
	if typed == "" {
		return nil
	}
	for _, node := range nodes {
		if strings.ToLower(node.ID) == typed || strings.ToLower(node.Domain) == typed {
			return []admin.NodeView{node}
		}
	}
	var matches []admin.NodeView
	for _, node := range nodes {
		if strings.Contains(strings.ToLower(node.Domain), typed) {
			matches = append(matches, node)
		}
	}
	return matches
}

// promptNode is the fallback when the list is unavailable: a domain or an id typed by hand, with an empty
// answer cancelling, which the label says.
func (m *menu) promptNode(ui *termui.UI) (string, error) {
	answer, err := ui.Prompt(m.translator().T(i18n.MnPromptNode), "")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}

// meshNodes is the list the picker draws. The timeout is short because a menu that waits a minute for a
// dead socket is worse than asking for the domain by hand.
func (m *menu) meshNodes() ([]admin.NodeView, error) {
	if m.nodes == nil {
		return nil, errors.New("no mesh connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.nodes(ctx)
}

func (m *menu) planCheck(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	whole, err := ui.Confirm(tr.T(i18n.MnPromptCheckAll), true)
	if err != nil {
		return nil, err
	}
	if whole {
		return [][]string{{"check", "now", "--all"}}, nil
	}
	node, err := m.pickNode(ui)
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, nil
	}
	return [][]string{{"check", "now", node}}, nil
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
	key, err := ui.Required(tr.T(i18n.MnPromptSetting), "")
	if err != nil {
		return nil, err
	}

	value, err := ui.Prompt(tr.T(i18n.MnPromptValue), "")
	if err != nil {
		return nil, err
	}
	return [][]string{{"policy", "set", key, value}}, nil
}

// planRestore replaces the server's database and certificates from an archive. The running server holds
// the database open, so the first thing the menu says is that it has to be stopped: the command cannot
// stop it, and a restore under a live server is a corrupt database.
func (m *menu) planRestore(ui *termui.UI) ([][]string, error) {
	tr := m.translator()
	archive, err := ui.Required(tr.T(i18n.PromptArchive), "")
	if err != nil {
		return nil, err
	}

	sure, err := ui.Confirm(tr.T(i18n.PromptRestore), false)
	if err != nil {
		return nil, err
	}
	if !sure {
		return nil, nil
	}
	// --force because the point of a restore is to replace what is there.
	return [][]string{{"restore", "--from", archive, "--force"}}, nil
}

// planUpdate asks before replacing the server's own binary and restarting it. The command publishes what
// it found before it touches anything, and puts the previous binary back if the restart fails.
func (m *menu) planUpdate(ui *termui.UI) ([][]string, error) {
	sure, err := ui.Confirm(m.translator().T(i18n.MnPromptUpdate), false)
	if err != nil {
		return nil, err
	}
	if !sure {
		return nil, nil
	}
	return [][]string{{"update", "--apply"}}, nil
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
	node, err := m.pickNode(ui)
	if err != nil {
		return nil, err
	}
	if node == "" {
		return nil, nil
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
