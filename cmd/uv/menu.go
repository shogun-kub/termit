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
	entries := a.menuEntries()

	for {
		ui.Clear()
		a.menuHeader(ui)
		items := make([]termui.Item, len(entries))
		tr := a.translator()
		for i, entry := range entries {
			items[i] = termui.Item{Label: tr.T(entry.labelKey), Hint: tr.T(entry.hintKey)}
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
		index, err := termui.Index(choice)
		if err != nil {
			ui.Error(err.Error())
			continue
		}
		if index >= len(entries) {
			ui.Error(a.translator().Tf(i18n.MenuPastEnd, choice))
			continue
		}

		plan, err := entries[index].plan(ui)
		switch {
		case err != nil:
			ui.Error(err.Error())
		case plan == nil:
			ui.Say(a.translator().T(i18n.MenuCancelled))
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
		ui.KV("domain", cfg.Domain)
		ui.KV("identity", state.Fingerprint(node.Identity.SelfUUID))
		ui.KV("peer path", state.Fingerprint(node.Identity.XHTTPPath))
		ui.State("ok", tr.T(i18n.MenuInstalled))
	}
	ui.Blank()
}

// menuEntries is the menu in order. Every entry answers with argv, never with behaviour.
func (a *application) menuEntries() []menuEntry {
	return []menuEntry{
		{
			labelKey: i18n.MenuStatusLabel,
			hintKey:  i18n.MenuStatusHint,
			plan:     fixed("status"),
		},
		{
			labelKey: i18n.MenuDoctorLabel,
			hintKey:  i18n.MenuDoctorHint,
			plan:     fixed("doctor"),
		},
		{
			labelKey: i18n.MenuNetLabel,
			hintKey:  i18n.MenuNetHint,
			plan:     fixed("net"),
		},
		{
			labelKey: i18n.MenuInstallLabel,
			hintKey:  i18n.MenuInstallHint,
			plan:     a.planInstall,
		},
		{
			labelKey: i18n.MenuWizardLabel,
			hintKey:  i18n.MenuWizardHint,
			plan:     fixed("wizard"),
		},
		{
			labelKey: i18n.MenuOptionsLabel,
			hintKey:  i18n.MenuOptionsHint,
			plan:     fixed("options"),
		},
		{
			labelKey: i18n.MenuEnrollLabel,
			hintKey:  i18n.MenuEnrollHint,
			plan:     a.planEnroll,
		},
		{
			labelKey: i18n.MenuUnblockLabel,
			hintKey:  i18n.MenuUnblockHint,
			plan:     fixed("unblock"),
		},
		{
			labelKey: i18n.MenuMigrateLabel,
			hintKey:  i18n.MenuMigrateHint,
			plan:     a.planMigrate,
		},
		{
			labelKey: i18n.MenuSyncLabel,
			hintKey:  i18n.MenuSyncHint,
			plan:     fixed("sync"),
		},
		{
			labelKey: i18n.MenuDriftLabel,
			hintKey:  i18n.MenuDriftHint,
			plan:     fixed("sync", "--check"),
		},
		{
			labelKey: i18n.MenuDevicesLabel,
			hintKey:  i18n.MenuDevicesHint,
			plan:     a.planDevices,
		},
		{
			labelKey: i18n.MenuURILabel,
			hintKey:  i18n.MenuURIHint,
			plan:     fixed("uri"),
		},
		{
			labelKey: i18n.MenuWarpLabel,
			hintKey:  i18n.MenuWarpHint,
			plan:     a.planWarp,
		},
		{
			labelKey: i18n.MenuFirewallLabel,
			hintKey:  i18n.MenuFirewallHint,
			plan:     a.planFirewall,
		},
		{
			labelKey: i18n.MenuBansLabel,
			hintKey:  i18n.MenuBansHint,
			plan:     a.planBans,
		},
		{
			labelKey: i18n.MenuGuardLabel,
			hintKey:  i18n.MenuGuardHint,
			plan:     fixed("guard", "status"),
		},
		{
			labelKey: i18n.MenuCertificateLabel,
			hintKey:  i18n.MenuCertificateHint,
			plan:     a.planCertificate,
		},
		{
			labelKey: i18n.MenuDecoyLabel,
			hintKey:  i18n.MenuDecoyHint,
			plan:     a.planDecoy,
		},
		{
			labelKey: i18n.MenuUninstallLabel,
			hintKey:  i18n.MenuUninstallHint,
			plan:     a.planUninstall,
		},
		{
			labelKey: i18n.MenuHelpLabel,
			hintKey:  i18n.MenuHelpHint,
			plan:     fixed("help"),
		},
	}
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
	invite, err := ui.Required(tr.T(i18n.PromptInvite), "")
	if err != nil {
		return nil, err
	}
	return [][]string{{"enroll", "--invite", invite, "--mn", mn}}, nil
}

// planDevices handles the local devices on this node.
func (a *application) planDevices(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: tr.T(i18n.SubDevicesListLabel), Hint: tr.T(i18n.SubDevicesListHint)},
		{Label: tr.T(i18n.SubDevicesAddLabel), Hint: tr.T(i18n.SubDevicesAddHint)},
		{Label: tr.T(i18n.SubDevicesRemoveLabel), Hint: tr.T(i18n.SubDevicesRemoveHint)},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	switch choice {
	case "1":
		return [][]string{{"device", "list"}}, nil
	case "2":
		label, err := ui.Required(tr.T(i18n.PromptDeviceLabel), "phone")
		if err != nil {
			return nil, err
		}
		return [][]string{{"device", "add", "--label", label}}, nil
	default:
		label, err := ui.Required(tr.T(i18n.PromptDeviceRemove), "")
		if err != nil {
			return nil, err
		}
		return [][]string{{"device", "remove", label}}, nil
	}
}

// planWarp changes where proxied traffic leaves.
func (a *application) planWarp(ui *termui.UI) ([][]string, error) {
	tr := a.translator()
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: tr.T(i18n.SubWarpShowLabel), Hint: tr.T(i18n.SubWarpShowHint)},
		{Label: tr.T(i18n.SubWarpAllLabel), Hint: tr.T(i18n.SubWarpAllHint)},
		{Label: tr.T(i18n.SubWarpOffLabel), Hint: tr.T(i18n.SubWarpOffHint)},
		{Label: tr.T(i18n.SubWarpSelectedLabel), Hint: tr.T(i18n.SubWarpSelectedHint)},
		{Label: tr.T(i18n.SubWarpRotateLabel), Hint: tr.T(i18n.SubWarpRotateHint)},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	switch choice {
	case "1":
		return [][]string{{"warp", "status"}}, nil
	case "2":
		return [][]string{{"warp", "mode", "all"}}, nil
	case "3":
		return [][]string{{"warp", "mode", "off"}}, nil
	case "4":
		domains, err := ui.Required(tr.T(i18n.PromptWarpDomains), "")
		if err != nil {
			return nil, err
		}
		return [][]string{{"warp", "mode", "selected", "--selected", domains}}, nil
	default:
		rotate, err := ui.Confirm(tr.T(i18n.PromptWarpRotate), false)
		if err != nil {
			return nil, err
		}
		if !rotate {
			return nil, nil
		}
		return [][]string{{"warp", "rotate"}}, nil
	}
}

// planFirewall and planBans are the two groups the server's menu draws as well.
func (a *application) planFirewall(ui *termui.UI) ([][]string, error) {
	return menuplan.Choose(ui, a.translator(), menuplan.Firewall())
}

func (a *application) planBans(ui *termui.UI) ([][]string, error) {
	return menuplan.Choose(ui, a.translator(), menuplan.Bans())
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
