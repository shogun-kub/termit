package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/menuplan"
	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/release"
	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
)

// menuEntry is one line of the interactive menu. The menu owns no behaviour: it asks the questions a
// flag would answer, then runs the same command line a script would. That is the whole point — a
// second implementation of "install" would drift from the first one.
type menuEntry struct {
	labelKey i18n.Key
	hintKey  i18n.Key
	// plan builds the commands to run. A nil plan with a nil error means the operator cancelled; an
	// error means the answer was unusable and nothing runs.
	plan func(ui *termui.UI) ([][]string, error)
	// children, when set, opens a level instead of running a command: the entry is a group, and the
	// list stays short enough to read.
	children func() []menuEntry
}

// runMenu is the interactive front end: the binary with no arguments on a terminal, or `termit-uv menu`.
//
// A `--config` given after `menu` reaches every command the menu runs and is the file the header reads:
// the flag used to be dropped, so the menu inspected and acted on the default configuration while the
// operator was looking at another node.
func (a *application) runMenu(args []string) int {
	flags := flag.NewFlagSet("termit-uv menu", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv menu: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	a.configPath = *configPath
	base := []string(nil)
	if setFlags(flags) {
		base = []string{"--config", *configPath}
	}

	// The menu speaks the configured language, unless this console cannot show it. Russian needs a
	// UTF-8 environment; a console that would print question marks gets English instead.
	lang := i18n.Default
	if cfg, err := config.Load(*configPath); err == nil {
		lang = cfg.Lang()
	}
	a.tr = i18n.New(i18n.Console(lang, a.env))

	ui := termui.New(a.stdin(), a.stdout, a.menuStyle())
	levels := []menuLevel{{entries: a.menuEntries()}}

	for {
		level := levels[len(levels)-1]
		ui.Clear()
		a.menuHeader(ui)
		tr := a.translator()
		if level.title != "" {
			ui.Title(tr.T(level.title))
		}
		items := make([]termui.Item, 0, len(level.entries)+1)
		for _, entry := range level.entries {
			items = append(items, termui.Item{Label: tr.T(entry.labelKey), Hint: tr.T(entry.hintKey)})
		}
		if len(levels) > 1 {
			items = append(items, termui.Item{Key: "0", Label: tr.T(i18n.MenuBackLabel), Hint: tr.T(i18n.MenuBackHint)})
		}
		choice, err := ui.Menu(items)
		if errors.Is(err, io.EOF) {
			// stdin closed: the session is over, and that is not an error.
			return exitOK
		}
		if err != nil {
			ui.Error(fmt.Sprintf("menu: %v", err))
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
		if err != nil {
			ui.Error(err.Error())
			continue
		}
		if index >= len(level.entries) {
			ui.Error(tr.Tf(i18n.MenuPastEnd, choice))
			continue
		}
		entry := level.entries[index]
		if entry.children != nil {
			levels = append(levels, menuLevel{entries: entry.children(), title: entry.labelKey})
			continue
		}

		plan, err := entry.plan(ui)
		switch {
		case err != nil:
			ui.Error(err.Error())
		case plan == nil:
			ui.Say(tr.T(i18n.MenuCancelled))
		default:
			if code := a.runPlan(ui, plan, base); code != exitOK {
				// A failed command has explained itself; keep the summary on screen long enough to read.
				_ = ui.Pause()
				continue
			}
		}
		if err := ui.Pause(); err != nil {
			return exitOK
		}
	}
}

// menuLevel is one screen of the menu: a list of entries and the group it belongs to, which the header
// repeats so a deep screen still says where the operator is.
type menuLevel struct {
	entries []menuEntry
	title   i18n.Key
}

// runPlan runs the commands an entry produced, one at a time, and stops at the first failure. The flags
// the menu itself was given are appended to each: every node command takes `--config`, and `help`
// ignores what follows it.
func (a *application) runPlan(ui *termui.UI, plan [][]string, base []string) int {
	for _, argv := range plan {
		argv = appendFlags(argv, base)
		ui.Blank()
		ui.Say("$ termit-uv " + strings.Join(argv, " "))
		ui.Blank()
		if code := a.run(argv); code != exitOK {
			ui.Blank()
			ui.Error(a.translator().Tf(i18n.MenuStepFailed, code))
			return code
		}
	}
	return exitOK
}

// menuHeader draws what this node is, without pretending to be a status report: the status command
// does that, and the menu offers it as an action.
func (a *application) menuHeader(ui *termui.UI) {
	tr := a.translator()
	ui.Title(tr.T(i18n.MenuTitle))
	ui.Hint(version.String("termit-uv"))
	ui.Blank()

	cfg, err := config.Load(a.menuConfigPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ui.State("warn", tr.T(i18n.MenuNotInstalled))
		ui.Blank()
		return
	case err != nil:
		ui.State("fail", tr.Tf(i18n.MenuConfigError, err.Error()))
		ui.Blank()
		return
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	switch {
	case errors.Is(err, state.ErrNotInstalled):
		ui.State("warn", tr.T(i18n.MenuNoState))
	case err != nil:
		ui.State("fail", tr.Tf(i18n.MenuStateError, err.Error()))
	default:
		// The card an owner screenshots when asking for help: what this node is, what it serves, where its
		// traffic leaves, and how the mesh looks from here. Every value is local — the card is drawn on
		// every screen, and a probe on every keystroke is not a header.
		ui.KV("domain", cfg.Domain)
		ui.KV("identity", state.Fingerprint(node.Identity.SelfUUID))
		ui.KV("peer path", state.Fingerprint(node.Identity.XHTTPPath))
		ui.KV("serves", servesLine(cfg))
		ui.KV("egress", egressLine(cfg))
		if node.ServerVersion != "" {
			ui.KV("server", serverLine(node.ServerVersion))
		}
		if len(node.Peers) > 0 {
			ui.KV("peers", peersLine(node.Peers))
		}
		if !node.Schedule.LastSyncAt.IsZero() {
			ui.KV("sync", humanAge(a.now(), node.Schedule.LastSyncAt))
		}
		ui.State("ok", tr.T(i18n.MenuInstalled))
	}
	ui.Blank()
}

// servesLine names the transports this node answers on. The configuration decides, so asking costs
// nothing and the card cannot drift from what the core was told.
func servesLine(cfg *config.Config) string {
	if cfg.Hysteria != nil {
		return fmt.Sprintf("TLS/443 + Hysteria2 UDP/%d", proto.HysteriaPort)
	}
	return "TLS/443 only"
}

// egressLine says where proxied traffic leaves and how it gets there; "direct" is a mode, not a failure.
func egressLine(cfg *config.Config) string {
	mode := cfg.Warp.Mode
	if mode == "" {
		mode = config.WarpAll
	}
	if mode == config.WarpOff {
		return "off (direct)"
	}
	return mode + " via " + cfg.Warp.TransportOrDefault()
}

// serverLine names the build the management server reported, and says plainly when this node is behind
// it — the one line of the card that can be wrong about two machines at once.
func serverLine(server string) string {
	if server == version.Version {
		return server
	}
	return server + " (this node is behind)"
}

// peersLine counts the mesh and says how much of it the server marks degraded.
func peersLine(peers []state.Peer) string {
	degraded := 0
	for _, peer := range peers {
		if peer.Degraded {
			degraded++
		}
	}
	if degraded == 0 {
		return fmt.Sprintf("%d", len(peers))
	}
	return fmt.Sprintf("%d (%d degraded)", len(peers), degraded)
}

// menuEntries is the menu in order. Every entry answers with argv, never with behaviour, and every level
// stays under ten choices so a screen can be read without scrolling.
func (a *application) menuEntries() []menuEntry {
	return []menuEntry{
		{labelKey: i18n.MenuGroupStateLabel, hintKey: i18n.MenuGroupStateHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MenuStatusLabel, hintKey: i18n.MenuStatusHint, plan: fixed("status")},
				{labelKey: i18n.MenuDoctorLabel, hintKey: i18n.MenuDoctorHint, plan: fixed("doctor")},
			}
		}},
		{labelKey: i18n.MenuGroupNetworkLabel, hintKey: i18n.MenuGroupNetworkHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MenuNetLabel, hintKey: i18n.MenuNetHint, plan: fixed("net")},
				{labelKey: i18n.MenuWarpLabel, hintKey: i18n.MenuWarpHint, children: a.warpEntries},
				{labelKey: i18n.MenuTransportLabel, hintKey: i18n.MenuTransportHint, plan: a.planTransport},
			}
		}},
		{labelKey: i18n.MenuGroupClientsLabel, hintKey: i18n.MenuGroupClientsHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MenuOptionsLabel, hintKey: i18n.MenuOptionsHint, plan: fixed("options")},
				{labelKey: i18n.MenuURILabel, hintKey: i18n.MenuURIHint, plan: fixed("uri")},
				{labelKey: i18n.MenuDevicesLabel, hintKey: i18n.MenuDevicesHint, children: a.deviceEntries},
			}
		}},
		{labelKey: i18n.MenuGroupMeshLabel, hintKey: i18n.MenuGroupMeshHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MenuSyncLabel, hintKey: i18n.MenuSyncHint, plan: fixed("sync")},
				{labelKey: i18n.MenuDriftLabel, hintKey: i18n.MenuDriftHint, plan: fixed("sync", "--check")},
				{labelKey: i18n.MenuAgentOnceLabel, hintKey: i18n.MenuAgentOnceHint, plan: fixed("agent", "--once")},
				{labelKey: i18n.MenuEnrollLabel, hintKey: i18n.MenuEnrollHint, plan: a.planEnroll},
				{labelKey: i18n.MenuUnblockLabel, hintKey: i18n.MenuUnblockHint, plan: fixed("unblock")},
			}
		}},
		{labelKey: i18n.MenuGroupDefenceLabel, hintKey: i18n.MenuGroupDefenceHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MenuFirewallLabel, hintKey: i18n.MenuFirewallHint,
					children: func() []menuEntry { return a.optionEntries(menuplan.Firewall()) }},
				{labelKey: i18n.MenuBansLabel, hintKey: i18n.MenuBansHint,
					children: func() []menuEntry { return a.optionEntries(menuplan.Bans()) }},
				{labelKey: i18n.MenuGuardLabel, hintKey: i18n.MenuGuardHint, plan: fixed("guard", "status")},
			}
		}},
		{labelKey: i18n.MenuGroupSiteLabel, hintKey: i18n.MenuGroupSiteHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MenuCertificateLabel, hintKey: i18n.MenuCertificateHint, plan: a.planCertificate},
				{labelKey: i18n.MenuDecoyLabel, hintKey: i18n.MenuDecoyHint, plan: a.planDecoy},
			}
		}},
		{labelKey: i18n.MenuGroupNodeLabel, hintKey: i18n.MenuGroupNodeHint, children: func() []menuEntry {
			return []menuEntry{
				{labelKey: i18n.MenuInstallLabel, hintKey: i18n.MenuInstallHint, plan: a.planInstall},
				{labelKey: i18n.MenuWizardLabel, hintKey: i18n.MenuWizardHint, plan: fixed("wizard")},
				{labelKey: i18n.MenuUpgradeLabel, hintKey: i18n.MenuUpgradeHint, plan: a.planUpgrade},
				{labelKey: i18n.MenuMigrateLabel, hintKey: i18n.MenuMigrateHint, plan: a.planMigrate},
				{labelKey: i18n.MenuAgentKeyLabel, hintKey: i18n.MenuAgentKeyHint, plan: a.planAgentKey},
				{labelKey: i18n.MenuUninstallLabel, hintKey: i18n.MenuUninstallHint, plan: a.planUninstall},
			}
		}},
		{labelKey: i18n.MenuHelpLabel, hintKey: i18n.MenuHelpHint, plan: fixed("help")},
	}
}

// warpEntries is where proxied traffic leaves. The four mode changes and the rotation are separate
// commands, so they are separate lines.
func (a *application) warpEntries() []menuEntry {
	return []menuEntry{
		{labelKey: i18n.SubWarpShowLabel, hintKey: i18n.SubWarpShowHint, plan: fixed("warp", "status")},
		{labelKey: i18n.SubWarpAllLabel, hintKey: i18n.SubWarpAllHint, plan: fixed("warp", "mode", "all")},
		{labelKey: i18n.SubWarpOffLabel, hintKey: i18n.SubWarpOffHint, plan: fixed("warp", "mode", "off")},
		{labelKey: i18n.SubWarpSelectedLabel, hintKey: i18n.SubWarpSelectedHint, plan: a.planWarpSelected},
		{labelKey: i18n.SubWarpRotateLabel, hintKey: i18n.SubWarpRotateHint, plan: a.planWarpRotate},
	}
}

// deviceEntries is the node's own devices: the ones that use it without the server.
func (a *application) deviceEntries() []menuEntry {
	return []menuEntry{
		{labelKey: i18n.SubDevicesListLabel, hintKey: i18n.SubDevicesListHint, plan: fixed("device", "list")},
		{labelKey: i18n.SubDevicesAddLabel, hintKey: i18n.SubDevicesAddHint, plan: a.planDeviceAdd},
		{labelKey: i18n.SubDevicesRemoveLabel, hintKey: i18n.SubDevicesRemoveHint, plan: a.planDeviceRemove},
	}
}

// optionEntries turns a shared table into a level. The question an option declares is asked when the
// option is chosen, not when the level is drawn.
func (a *application) optionEntries(options []menuplan.Option) []menuEntry {
	entries := make([]menuEntry, len(options))
	for i, option := range options {
		option := option
		entries[i] = menuEntry{labelKey: option.Label, hintKey: option.Hint,
			plan: func(ui *termui.UI) ([][]string, error) { return option.Run(ui, a.translator()) }}
	}
	return entries
}

// labelOf is the entry's label in one language. It exists so tests can find an entry without
// depending on its position or on the machine's locale.
func (e menuEntry) labelOf(tr i18n.Translator) string { return tr.T(e.labelKey) }

// fixed is an entry that runs one command line with no questions.
func fixed(argv ...string) func(*termui.UI) ([][]string, error) {
	return func(*termui.UI) ([][]string, error) { return [][]string{argv}, nil }
}

// setFlags reports whether the operator set any flag, as opposed to the defaults being in place.
func setFlags(flags *flag.FlagSet) bool {
	set := false
	flags.Visit(func(*flag.Flag) { set = true })
	return set
}

// appendFlags appends the menu's own flags to a command, after its words: the first word selects the
// subcommand, and every node command reorders its flags anyway.
func appendFlags(argv, base []string) []string {
	if len(base) == 0 || len(argv) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+len(base))
	out = append(out, argv...)
	return append(out, base...)
}

// planInstall converges an installed node, or installs one, asking only for what the flags would
// require on a first run.
func (a *application) planInstall(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	if _, err := config.Load(a.menuConfigPath()); err == nil {
		return [][]string{{"init"}}, nil
	}
	ui.Say(tr.T(i18n.MenuNoConfig))
	domain, err := ui.Required(tr.T(i18n.PromptDomain), "")
	if err != nil {
		return nil, err
	}
	email, err := ui.Prompt(tr.T(i18n.PromptEmail), "")
	if err != nil {
		return nil, err
	}
	warp, err := ui.Confirm(tr.T(i18n.PromptWarp), true)
	if err != nil {
		return nil, err
	}

	argv := []string{"init", "--domain", domain}
	if email != "" {
		argv = append(argv, "--email", email)
	}
	if !warp {
		argv = append(argv, "--no-warp")
	}
	return [][]string{argv}, nil
}

// planTransport turns the QUIC transport on or off. The question defaults to what the configuration
// already says, so pressing Enter changes nothing — and the menu still runs no behaviour of its own: it
// answers the flag `init --hysteria` would have carried.
func (a *application) planTransport(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	cfg, err := config.Load(a.menuConfigPath())
	if err != nil {
		ui.Say(tr.T(i18n.MenuNoConfig))
		return nil, nil
	}
	enabled := cfg.Hysteria != nil
	on, err := ui.Confirm(tr.T(i18n.PromptTransport), enabled)
	if err != nil {
		return nil, err
	}
	switch {
	case on && !enabled:
		return [][]string{{"init", "--hysteria"}}, nil
	case !on && enabled:
		return [][]string{{"init", "--no-hysteria"}}, nil
	}
	return nil, nil
}

// planEnroll asks for the server and the invite code. The MN URL defaults to the configured one, so a
// converged node needs one answer.
func (a *application) planEnroll(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	mn := ""
	if cfg, err := config.Load(a.menuConfigPath()); err == nil {
		mn = cfg.MN.URL
	}
	if mn == "" {
		answer, err := ui.Prompt(tr.T(i18n.PromptMN), "https://mn.example.com")
		if err != nil {
			return nil, err
		}
		mn = answer
	}
	invite, err := ui.Prompt(tr.T(i18n.PromptInvite), "")
	if err != nil {
		return nil, err
	}
	invite = strings.TrimSpace(invite)
	if invite == "" {
		return nil, nil
	}
	return [][]string{{"enroll", "--invite", invite, "--mn", mn}}, nil
}

// planDeviceAdd names a new local device: a phone, a laptop, whatever only this node serves.
func (a *application) planDeviceAdd(ui *termui.UI) ([][]string, error) {
	label, err := ui.Required(a.translator().T(i18n.PromptDeviceLabel), "phone")
	if err != nil {
		return nil, err
	}
	return [][]string{{"device", "add", "--label", label}}, nil
}

// planDeviceRemove removes one by its label. An empty answer cancels: the device list is small, and
// retyping a name is cheaper than removing the wrong one.
func (a *application) planDeviceRemove(ui *termui.UI) ([][]string, error) {
	label, err := ui.Prompt(a.translator().T(i18n.PromptDeviceRemove), "")
	if err != nil {
		return nil, err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, nil
	}
	return [][]string{{"device", "remove", label}}, nil
}

// planWarpSelected sends only the named domains through WARP.
func (a *application) planWarpSelected(ui *termui.UI) ([][]string, error) {
	domains, err := ui.Required(a.translator().T(i18n.PromptWarpDomains), "")
	if err != nil {
		return nil, err
	}
	return [][]string{{"warp", "mode", "selected", "--selected", domains}}, nil
}

// planWarpRotate moves the node to a fresh WARP identity, which is what gets it out of an exit address
// Cloudflare has started treating as suspicious.
func (a *application) planWarpRotate(ui *termui.UI) ([][]string, error) {
	rotate, err := ui.Confirm(a.translator().T(i18n.PromptWarpRotate), false)
	if err != nil {
		return nil, err
	}
	if !rotate {
		return nil, nil
	}
	return [][]string{{"warp", "rotate"}}, nil
}

// planUpgrade offers the server's release when the two differ. It is the only menu entry that replaces the
// running binary, so a node with nothing to install says so instead of offering a no-op.
func (a *application) planUpgrade(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	cfg, err := config.Load(a.menuConfigPath())
	if err != nil {
		ui.Say(tr.T(i18n.MenuNoConfig))
		return nil, nil
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil || !release.Version.MatchString(node.ServerVersion) {
		ui.Say(tr.T(i18n.MenuUpgradeNoOffer))
		return nil, nil
	}
	if node.ServerVersion == version.Version {
		ui.Say(tr.Tf(i18n.MenuUpgradeSame, node.ServerVersion))
		return nil, nil
	}
	sure, err := ui.Confirm(tr.Tf(i18n.PromptUpgrade, node.ServerVersion), true)
	if err != nil {
		return nil, err
	}
	if !sure {
		return nil, nil
	}
	return [][]string{{"upgrade"}}, nil
}

// planMigrate moves the node to another domain. The configured domain is the default, so a move already
// written into the configuration needs no answer at all.
func (a *application) planMigrate(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	current := ""
	if cfg, err := config.Load(a.menuConfigPath()); err == nil {
		current = cfg.Domain
	}
	domain, err := ui.Required(tr.T(i18n.PromptNewDomain), current)
	if err != nil {
		return nil, err
	}
	argv := []string{"migrate", "--relocate"}
	if domain != current {
		argv = append(argv, "--domain", domain)
	}
	return [][]string{argv}, nil
}

// planAgentKey installs an agent key a new home issued. It asks for the file and confirms, because the swap
// takes effect at once: until the node syncs, it answers to nobody.
func (a *application) planAgentKey(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	path, err := ui.Required(tr.T(i18n.PromptAgentKeyFile), "")
	if err != nil {
		return nil, err
	}
	sure, err := ui.Confirm(tr.T(i18n.PromptAgentKeyConfirm), false)
	if err != nil || !sure {
		return nil, err
	}
	return [][]string{{"agentkey", "--install", path}}, nil
}

// planCertificate renews the certificate, forcing a reissue when asked.
func (a *application) planCertificate(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	force, err := ui.Confirm(tr.T(i18n.PromptCertForce), false)
	if err != nil {
		return nil, err
	}
	if force {
		return [][]string{{"renew-cert", "--force"}}, nil
	}
	return [][]string{{"renew-cert"}}, nil
}

// planDecoy regenerates the decoy site, optionally with another archetype.
func (a *application) planDecoy(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	template, err := ui.Prompt(tr.T(i18n.PromptTemplate), "")
	if err != nil {
		return nil, err
	}
	switch template {
	case "":
		return [][]string{{"renew-site"}}, nil
	case "product", "portal":
		return [][]string{{"renew-site", "--template", template}}, nil
	default:
		return nil, fmt.Errorf("%q is neither product nor portal", template)
	}
}

// planUninstall is the one destructive path, so it asks twice and names what --purge takes.
func (a *application) planUninstall(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	purge, err := ui.Confirm(tr.T(i18n.PromptPurge), false)
	if err != nil {
		return nil, err
	}
	what := "the units, the core and the nginx vhost"
	if purge {
		what = "the units, the core, the nginx vhost, and every secret and certificate"
	}
	sure, err := ui.Confirm(tr.Tf(i18n.PromptSure, what), false)
	if err != nil {
		return nil, err
	}
	if !sure {
		return nil, nil
	}
	if purge {
		return [][]string{{"uninstall", "--purge"}}, nil
	}
	return [][]string{{"uninstall"}}, nil
}

// menuConfigPath is the configuration the menu inspects: the default one in production, whatever a
// test pinned otherwise.
func (a *application) menuConfigPath() string {
	if a.configPath != "" {
		return a.configPath
	}
	return configPathDefault()
}

// stdin is the input stream; os.Stdin unless a test replaced it.
func (a *application) stdin() io.Reader {
	if a.in != nil {
		return a.in
	}
	return os.Stdin
}

// menuStyle is colour on a terminal, plain text otherwise.
func (a *application) menuStyle() termui.Style {
	if a.interactiveFn != nil {
		if !a.interactiveFn() {
			return termui.Style{}
		}
		return termui.Colour()
	}
	return termui.PlainColour(os.Stdout)
}

// interactive reports whether the bare binary should open the menu: both streams are terminals.
func (a *application) interactive() bool {
	if a.interactiveFn != nil {
		return a.interactiveFn()
	}
	return termui.Interactive(os.Stdin, os.Stdout)
}
