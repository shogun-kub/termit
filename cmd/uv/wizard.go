package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/decoy"
	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// wizardAnswers is what the wizard has learned so far. It is written after every answered step, so an
// interrupted run resumes with the answers already given instead of starting over.
type wizardAnswers struct {
	Locale       string   `yaml:"locale,omitempty"`
	Domain       string   `yaml:"domain,omitempty"`
	Email        string   `yaml:"email,omitempty"`
	ACME         string   `yaml:"acme,omitempty"`
	ACMESkip     bool     `yaml:"acme_skip_verify,omitempty"`
	Template     string   `yaml:"decoy_template,omitempty"`
	Warp         string   `yaml:"warp,omitempty"`
	WarpSelected []string `yaml:"warp_selected,omitempty"`
	Hysteria     bool     `yaml:"hysteria,omitempty"`
	Harden       bool     `yaml:"harden,omitempty"`
	MNURL        string   `yaml:"mn,omitempty"`
	Invite       string   `yaml:"invite,omitempty"`
}

// runWizard walks a first-time operator through the decisions init needs and then runs it.
//
// It is a front end, not a second installer: every answer becomes a flag of `init`, and the wizard
// itself touches nothing. That keeps one code path that can break, and it means an operator who
// prefers flags can do exactly what the wizard does.
func (a *application) runWizard(args []string) int {
	flags := a.newFlagSet("wizard", `Walks through a first installation step by step.

Each question shows the answer given last time, so an interrupted run continues where it stopped and
Enter accepts what is already there. When the node is installed the wizard says so and exits; it never
re-installs a working node.`)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	fresh := flags.Bool("fresh", false, "forget the answers given so far and start from the beginning")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv wizard: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	draftPath := wizardDraftPath(*configPath)
	if *fresh {
		if err := os.Remove(draftPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(a.stderr, "termit-uv wizard: discard %s: %v\n", draftPath, err)
			return exitFailure
		}
	}

	cfg, cfgErr := a.loadConfig(*configPath)
	installed := cfgErr == nil
	switch {
	case errors.Is(cfgErr, fs.ErrNotExist):
		cfg = config.Default()
	case cfgErr != nil:
		fmt.Fprintf(a.stderr, "termit-uv wizard: %v\n", cfgErr)
		return exitFailure
	}

	if installed {
		done, err := a.initDone(cfg, *configPath)
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv wizard: %v\n", err)
			return exitFailure
		}
		if done {
			return a.wizardAlreadyInstalled(cfg, *configPath, draftPath)
		}
		// Installed, but not what this build would write: an upgrade, not a first installation. Asking
		// for the domain again would be an insult; saying what to run is the answer.
		if node, err := state.LoadInstalled(cfg.Paths.StatePath()); err == nil && node.Identity.SelfUUID != "" {
			return a.wizardOutdated(cfg, *configPath, draftPath)
		}
	}

	answers, err := loadWizardAnswers(draftPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv wizard: %v\n", err)
		return exitFailure
	}

	ui := termui.New(a.stdin(), a.stdout, a.menuStyle())
	save := func() error { return saveWizardAnswers(draftPath, answers) }

	// The language comes first, because everything after it is written in it. The question itself is
	// bilingual: it cannot be asked in a language the operator has not chosen yet.
	ui.Clear()
	lang, err := a.wizardAskLanguage(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Locale = lang.String()
	// The console decides whether Cyrillic is safe here; the page keeps the chosen language either way.
	a.tr = i18n.New(i18n.Console(lang, a.env))
	tr := a.translator()
	if a.tr.Lang() != lang {
		ui.Say(tr.T(i18n.WizardConsoleNote))
	}
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	ui.Blank()
	ui.Title(tr.T(i18n.WizardTitle))
	ui.Hint(tr.T(i18n.WizardHint))
	ui.Blank()

	domain, err := a.wizardAskDomain(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Domain = domain
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	email, err := ui.Prompt(tr.T(i18n.WizardEmail), firstNonEmpty(answers.Email, cfg.Email))
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Email = email
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	// The certificate authority comes before the site, because it is the one question whose wrong answer
	// is not visible until the installation fails at the last step.
	acmeDirectory, acmeSkip, err := a.wizardAskACME(ui, answers)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.ACME, answers.ACMESkip = acmeDirectory, acmeSkip
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	template, err := a.wizardAskTemplate(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Template = template
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	mode, selected, err := a.wizardAskWarp(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Warp, answers.WarpSelected = mode, selected
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	// The second transport is a "what clients can use" decision like egress, so it comes next, and
	// before the hardening question, which is about the host's own doors.
	hysteria, err := a.wizardAskTransport(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Hysteria = hysteria
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	harden, err := a.wizardAskHarden(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Harden = harden
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	mnURL, invite, err := a.wizardAskEnrollment(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.MNURL, answers.Invite = mnURL, invite
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	a.wizardSummary(ui, answers)
	proceed, err := ui.Confirm(tr.T(i18n.WizardConfirm), true)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	if !proceed {
		ui.Say(tr.T(i18n.WizardNothingChanged))
		return exitOK
	}

	ui.Blank()
	if code := a.runInit(wizardInitArgs(answers, *configPath)); code != exitOK {
		// The draft stays: a second run continues from the same answers.
		ui.Error(tr.T(i18n.WizardInstallFailed))
		return code
	}

	if answers.Invite != "" {
		enrollArgs := []string{"--config", *configPath, "--invite", answers.Invite}
		if answers.MNURL != "" {
			enrollArgs = append(enrollArgs, "--mn", answers.MNURL)
		}
		ui.Blank()
		if code := a.runEnroll(enrollArgs); code != exitOK {
			ui.Error(tr.T(i18n.WizardEnrollFailed))
			return code
		}
	}

	if err := os.Remove(draftPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stderr, "termit-uv wizard: remove %s: %v\n", draftPath, err)
	}

	ui.Blank()
	ui.Title(tr.T(i18n.WizardDone))
	ui.State("ok", tr.T(i18n.WizardDoneDetail))
	return a.runOptions([]string{"--config", *configPath})
}

// wizardOutdated is the third case: a node that is installed but behind this build. The wizard cannot
// converge it — that is init's job — so it says so and leaves the answers alone.
func (a *application) wizardOutdated(cfg *config.Config, configPath, draftPath string) int {
	a.tr = i18n.New(i18n.Console(cfg.Lang(), a.env))
	tr := a.translator()
	if err := os.Remove(draftPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stderr, "termit-uv wizard: remove %s: %v\n", draftPath, err)
	}
	fmt.Fprintln(a.stdout, tr.Tf(i18n.WizardOutdated, "termit-uv init"))
	fmt.Fprintln(a.stdout)
	if code := a.runStatus([]string{"--config", configPath}); code != exitOK {
		return code
	}
	fmt.Fprintf(a.stdout, "\n%s\n", tr.T(i18n.WizardOutdatedHint))
	return exitOK
}

// wizardAskLanguage is the one question asked before a language exists. It shows both names, and an
// empty answer keeps whatever was chosen last time (or the default).
func (a *application) wizardAskLanguage(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (i18n.Lang, error) {
	current, ok := i18n.Parse(firstNonEmpty(answers.Locale, cfg.Locale))
	if !ok {
		current = i18n.Default
	}
	for {
		answer, err := ui.Prompt(i18n.New(i18n.EN).T(i18n.WizardLang), current.String())
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(answer) == "" {
			return current, nil
		}
		lang, ok := i18n.Parse(answer)
		if !ok {
			ui.Error(i18n.New(i18n.EN).T(i18n.WizardLangInvalid))
			continue
		}
		return lang, nil
	}
}

// wizardAlreadyInstalled is what a second run of the wizard shows: what the node is, not questions
// with answers the node already made.
//
// One case is not finished, though: a run whose init succeeded and whose enroll did not leaves a node
// installed and outside the mesh, with the code still in the draft. This run is the second attempt at
// exactly that step, and declaring victory would leave the owner to work out from the draft what to type.
func (a *application) wizardAlreadyInstalled(cfg *config.Config, configPath, draftPath string) int {
	a.tr = i18n.New(i18n.Console(cfg.Lang(), a.env))
	tr := a.translator()
	if answers, err := loadWizardAnswers(draftPath); err == nil && answers.Invite != "" && !a.enrolled(cfg) {
		return a.wizardFinishEnrollment(cfg, configPath, draftPath, answers)
	}
	if err := os.Remove(draftPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stderr, "termit-uv wizard: remove %s: %v\n", draftPath, err)
	}
	fmt.Fprintln(a.stdout, tr.T(i18n.WizardInstalled))
	fmt.Fprintln(a.stdout)
	if code := a.runStatus([]string{"--config", configPath}); code != exitOK {
		return code
	}
	if node, err := state.LoadInstalled(cfg.Paths.StatePath()); err == nil {
		if tok := node.Options.Token; tok != "" {
			fmt.Fprintf(a.stdout, "\n"+tr.T(i18n.WizardOptionsLine)+"\n", "https://"+cfg.Domain+"/"+tok+"/")
		} else {
			fmt.Fprintf(a.stdout, "\n%s\n", tr.T(i18n.WizardOptionsNone))
		}
	}
	return exitOK
}

// wizardFinishEnrollment runs the registration a previous run did not finish, with the code it kept. The
// draft goes only when the node is in the mesh, so a third attempt is the same command again.
func (a *application) wizardFinishEnrollment(cfg *config.Config, configPath, draftPath string, answers wizardAnswers) int {
	tr := a.translator()
	fmt.Fprintln(a.stdout, tr.T(i18n.WizardEnrollPending))
	fmt.Fprintln(a.stdout)
	args := []string{"--config", configPath, "--invite", answers.Invite}
	if answers.MNURL != "" {
		args = append(args, "--mn", answers.MNURL)
	}
	if code := a.runEnroll(args); code != exitOK {
		fmt.Fprintln(a.stderr, tr.T(i18n.WizardEnrollFailed))
		return code
	}
	if err := os.Remove(draftPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stderr, "termit-uv wizard: remove %s: %v\n", draftPath, err)
	}
	ui := termui.New(a.stdin(), a.stdout, a.menuStyle())
	ui.Blank()
	ui.Title(tr.T(i18n.WizardDone))
	ui.State("ok", tr.T(i18n.WizardDoneDetail))
	return a.runOptions([]string{"--config", configPath})
}

// enrolled reports whether this node has registered with a management server.
func (a *application) enrolled(cfg *config.Config) bool {
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	return err == nil && node.Credentials != nil && node.Credentials.AgentKey != ""
}

// wizardAskDomain keeps asking until there is a domain, because there is nothing to install without
// one; every other question accepts an empty answer.
func (a *application) wizardAskDomain(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, error) {
	tr := a.translator()
	def := firstNonEmpty(answers.Domain, cfg.Domain)
	for {
		answer, err := ui.Prompt(tr.T(i18n.WizardDomain), def)
		if err != nil {
			return "", err
		}
		if answer == "" {
			ui.Error(tr.T(i18n.WizardDomainNeeded))
			continue
		}
		if strings.ContainsAny(answer, " /:@") {
			ui.Error(tr.T(i18n.WizardDomainShape))
			continue
		}
		return answer, nil
	}
}

// wizardInitArgs turns the answers into the flags `init` parses. It is the whole contract between the
// wizard and the installer: nothing here touches the host, and a question that never reaches this function
// is a question the installation ignores.
func wizardInitArgs(answers wizardAnswers, configPath string) []string {
	args := []string{"--config", configPath, "--domain", answers.Domain}
	if answers.Locale != "" {
		args = append(args, "--locale", answers.Locale)
	}
	if answers.Email != "" {
		args = append(args, "--email", answers.Email)
	}
	// A directory that is not the public one needs naming; the public one is init's own default, and
	// skip-verify only means something for a private CA this host cannot check.
	if answers.ACME != "" && answers.ACME != acme.DefaultDirectory {
		args = append(args, "--acme-ca", answers.ACME)
		if answers.ACMESkip {
			args = append(args, "--acme-skip-tls-verify")
		}
	}
	if answers.Template != "" {
		args = append(args, "--decoy-template", answers.Template)
	}
	if answers.MNURL != "" {
		args = append(args, "--mn", answers.MNURL)
	}
	if answers.Harden {
		args = append(args, "--harden")
	}
	if answers.Hysteria {
		args = append(args, "--hysteria")
	}
	switch answers.Warp {
	case config.WarpOff:
		args = append(args, "--no-warp")
	case config.WarpSelected:
		args = append(args, "--warp", config.WarpSelected)
		if len(answers.WarpSelected) > 0 {
			args = append(args, "--warp-selected", strings.Join(answers.WarpSelected, ","))
		}
	default:
		args = append(args, "--warp", config.WarpAll)
	}
	return args
}

// wizardAskACME asks which directory signs the certificate. The public one is the default and needs no
// flag, so a standard installation answers nothing here; a private authority — a test lab, a company CA —
// names its own, and only then is the question about verifying it worth asking: a self-signed CA is not
// one this host can check.
func (a *application) wizardAskACME(ui *termui.UI, answers wizardAnswers) (string, bool, error) {
	tr := a.translator()
	ui.Blank()
	ui.Say(tr.Tf(i18n.WizardACMEIntro, acme.DefaultDirectory))
	for {
		answer, err := ui.Prompt(tr.T(i18n.WizardACME), answers.ACME)
		if err != nil {
			return "", false, err
		}
		answer = strings.TrimRight(strings.TrimSpace(answer), "/")
		if answer == "" || answer == acme.DefaultDirectory {
			return "", false, nil
		}
		if !strings.HasPrefix(answer, "https://") && !strings.HasPrefix(answer, "http://") {
			ui.Error(tr.T(i18n.WizardACMEShape))
			continue
		}
		skip, err := ui.Confirm(tr.T(i18n.WizardACMESkip), answers.ACMESkip)
		if err != nil {
			return "", false, err
		}
		return answer, skip, nil
	}
}

// wizardAskTemplate offers the archetypes the build can generate, with the configured one as default.
func (a *application) wizardAskTemplate(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, error) {
	tr := a.translator()
	known := decoy.Templates()
	def := firstNonEmpty(answers.Template, cfg.Decoy.Template, config.DefaultDecoyTemplate)
	ui.Blank()
	ui.Say(tr.Tf(i18n.WizardTemplateIntro, strings.Join(known, ", ")))
	for {
		answer, err := ui.Prompt(tr.T(i18n.WizardTemplate), def)
		if err != nil {
			return "", err
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer == "" {
			answer = def
		}
		if decoy.KnownTemplate(answer) {
			return answer, nil
		}
		ui.Error(tr.Tf(i18n.WizardTemplateBad, answer))
	}
}

// wizardAskWarp decides where proxied traffic leaves, and, for "selected", which domains use WARP.
func (a *application) wizardAskWarp(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, []string, error) {
	tr := a.translator()
	def := firstNonEmpty(answers.Warp, cfg.Warp.Mode, config.WarpAll)
	ui.Blank()
	ui.Say(tr.T(i18n.WizardWarpIntro1))
	ui.Say(tr.T(i18n.WizardWarpIntro2))
	for {
		answer, err := ui.Prompt(tr.T(i18n.WizardWarp), def)
		if err != nil {
			return "", nil, err
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer == "" {
			answer = def
		}
		switch answer {
		case config.WarpAll, config.WarpOff:
			return answer, nil, nil
		case config.WarpSelected:
			selected := strings.Join(answers.WarpSelected, ", ")
			line, err := ui.Prompt(tr.T(i18n.WizardWarpDomains), selected)
			if err != nil {
				return "", nil, err
			}
			domains := splitList(line)
			if len(domains) == 0 {
				ui.Error(tr.T(i18n.WizardWarpNeedsOne))
				continue
			}
			return answer, domains, nil
		default:
			ui.Error(tr.T(i18n.WizardWarpBad))
		}
	}
}

// wizardAskTransport asks whether the node serves Hysteria2 beside the TLS profile. The answer is the
// only one that changes what a client can pick on the first day, so the summary repeats it.
func (a *application) wizardAskTransport(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (bool, error) {
	tr := a.translator()
	ui.Blank()
	ui.Say(tr.T(i18n.WizardTransportIntro))
	return ui.Confirm(tr.T(i18n.WizardTransport), answers.Hysteria || cfg.Hysteria != nil)
}

// transportNote renders the second transport in the summary: the port is the part the operator has to
// open, so it is named even though the answer was one word.
func transportNote(on bool) string {
	if !on {
		return "off"
	}
	return "on (UDP/443)"
}

// wizardAskEnrollment asks for the server and the invite code. Both are optional: a node can be
// installed now and joined to the mesh later with `termit-uv enroll`.
func (a *application) wizardAskEnrollment(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, string, error) {
	tr := a.translator()
	ui.Blank()
	ui.Say(tr.T(i18n.WizardMeshIntro))
	defURL := firstNonEmpty(answers.MNURL, cfg.MN.URL)
	if defURL != "" {
		ui.Say(tr.Tf(i18n.WizardMeshServer, defURL))
	}
	invite, err := ui.Prompt(tr.T(i18n.WizardInvite), answers.Invite)
	if err != nil {
		return "", "", err
	}
	invite = strings.TrimSpace(invite)
	if invite == "" {
		return defURL, "", nil
	}
	mnURL := defURL
	for mnURL == "" {
		mnURL, err = ui.Prompt(tr.T(i18n.WizardMNURL), "")
		if err != nil {
			return "", "", err
		}
		mnURL = strings.TrimRight(strings.TrimSpace(mnURL), "/")
		if mnURL == "" {
			ui.Error(tr.T(i18n.WizardMNNeeded))
			continue
		}
		if !strings.HasPrefix(mnURL, "https://") && !strings.HasPrefix(mnURL, "http://") {
			ui.Error(tr.T(i18n.WizardMNShape))
			mnURL = ""
		}
	}
	return mnURL, invite, nil
}

func (a *application) wizardSummary(ui *termui.UI, answers wizardAnswers) {
	tr := a.translator()
	ui.Blank()
	ui.Title(tr.T(i18n.WizardSummary))
	ui.KV("domain", answers.Domain)
	ui.KV("email", orNone(answers.Email))
	ui.KV("acme", orPublic(answers.ACME))
	ui.KV("site", answers.Template)
	ui.KV("egress", answers.Warp+selectedNote(answers.WarpSelected))
	ui.KV("quic", transportNote(answers.Hysteria))
	ui.KV("mesh", orNone(answers.MNURL))
	ui.KV(tr.T(i18n.WizardKVIndeed), orNone(maskInvite(answers.Invite)))
}

// maskInvite shows enough of a code to recognise it and not enough to use it from a screenshot.
func maskInvite(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[:2] + strings.Repeat("•", min(len(code)-4, 8)) + code[len(code)-2:]
}

func selectedNote(domains []string) string {
	if len(domains) == 0 {
		return ""
	}
	return " (" + strings.Join(domains, ", ") + ")"
}

func orNone(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

// orPublic renders the certificate authority in the summary: the empty answer is not a missing value but
// the public directory, and saying so is clearer than a dash.
func orPublic(directory string) string {
	if directory == "" {
		return "Let's Encrypt"
	}
	return directory
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// wizardInputEnded turns a closed or broken input into a clean exit: a session that ran out of answers
// is a cancel, not a failure, and nothing it saved is thrown away.
func wizardInputEnded(ui *termui.UI, err error) int {
	if errors.Is(err, io.EOF) || errors.Is(err, fs.ErrClosed) {
		ui.Say(i18n.New(i18n.EN).T(i18n.WizardCancelled))
		return exitOK
	}
	ui.Error(err.Error())
	return exitFailure
}

func (a *application) wizardSaveFailed(err error) int {
	fmt.Fprintf(a.stderr, "termit-uv wizard: keep the answers: %v\n", err)
	return exitFailure
}

// initDone asks whether the installation is finished. Tests replace it: the real answer depends on the
// host's units and certificate, which a unit test cannot arrange.
func (a *application) initDone(cfg *config.Config, configPath string) (bool, error) {
	if a.completeFn != nil {
		return a.completeFn(cfg, configPath)
	}
	return a.initComplete(cfg, configPath)
}

// initComplete reports whether the install pipeline has nothing left to do. It asks every step the
// same question `init --dry-run` asks, without touching the host: a step whose effect is already
// present is done, anything else is work.
func (a *application) initComplete(cfg *config.Config, configPath string) (bool, error) {
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		if errors.Is(err, state.ErrNotInstalled) {
			return false, nil
		}
		return false, err
	}
	if node.Identity.SelfUUID == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	installer, err := install.New(a.installOptions(cfg, configPath))
	if err != nil {
		return false, err
	}
	report, err := installer.Run(ctx, install.RunOptions{Command: "wizard", DryRun: true, KeepGoing: true})
	if err != nil {
		return false, err
	}
	for _, step := range report.Steps {
		if !stepSatisfied(step) {
			return false, nil
		}
	}
	return true, nil
}

// stepSatisfied reports whether one step of a dry run says the node is already installed. A step that
// never reports its effect as present says nothing either way; a failure in it falls through, because a
// node whose host checks fail is not installed.
func stepSatisfied(step install.StepReport) bool {
	switch {
	case step.Status == state.StepPresent:
		return true
	case nonEffectSteps[step.Name] && step.Status == state.StepPlanned:
		return true
	}
	return false
}

// nonEffectSteps are the steps whose Check never reports the effect as present, by design: preflight
// verifies the host on every run, start restarts the agent on every run because init is also the upgrade
// path, warp registers with Cloudflare when that is possible at all, and warp-egress measures where the
// tunnel exits — a measurement, not a file. None of them tells us whether the node is installed: a
// network that blocks WARP is the very case this project exists for, and it must not make the wizard
// tell the owner to run init forever.
var nonEffectSteps = map[string]bool{"preflight": true, "start": true, "warp": true, "warp-egress": true}

// wizardDraftPath keeps the answers beside the configuration they belong to, not in the state
// directory: a `--config` elsewhere is a node whose answers should travel with it.
func wizardDraftPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "wizard.yaml")
}

func loadWizardAnswers(path string) (wizardAnswers, error) {
	var answers wizardAnswers
	data, err := os.ReadFile(path) // #nosec G304 -- the path is the configuration's own directory.
	if errors.Is(err, fs.ErrNotExist) {
		return answers, nil
	}
	if err != nil {
		return answers, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &answers); err != nil {
		// A corrupt draft is not worth an error the operator cannot act on: keep the file, start empty.
		return wizardAnswers{}, nil
	}
	return answers, nil
}

func saveWizardAnswers(path string, answers wizardAnswers) error {
	data, err := yaml.Marshal(answers)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	// 0600: the draft may hold an invite code, which is a credential until it is used.
	return state.WriteFileAtomic(path, data, 0o600)
}

// wizardAskHarden asks whether the node should close itself. The default is no: an operator who does not
// know what the table does must not end up behind one, and a node that refuses scans is a choice, not a
// side effect of installing.
//
// A yes only writes the configuration. The table itself is applied by `termit-uv firewall on`, which arms
// the rollback timer first — the wizard cannot promise the operator's SSH survives, and only that command
// can undo a mistake in five minutes.
func (a *application) wizardAskHarden(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (bool, error) {
	tr := a.translator()
	ui.Blank()
	ui.Say(tr.T(i18n.WizardHardenIntro))
	ui.Hint(tr.T(i18n.WizardHardenHint))
	def := answers.Harden || cfg.Security.Firewall.Enabled
	answer, err := ui.Confirm(tr.T(i18n.WizardHarden), def)
	if err != nil {
		return false, err
	}
	return answer, nil
}
