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

	"github.com/shogun-kub/termit/internal/decoy"
	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// wizardAnswers is what the wizard has learned so far. It is written after every answered step, so an
// interrupted run resumes with the answers already given instead of starting over.
type wizardAnswers struct {
	Domain       string   `yaml:"domain,omitempty"`
	Email        string   `yaml:"email,omitempty"`
	Template     string   `yaml:"decoy_template,omitempty"`
	Warp         string   `yaml:"warp,omitempty"`
	WarpSelected []string `yaml:"warp_selected,omitempty"`
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
	}

	answers, err := loadWizardAnswers(draftPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv wizard: %v\n", err)
		return exitFailure
	}

	ui := termui.New(a.stdin(), a.stdout, a.menuStyle())
	ui.Clear()
	ui.Title("Termit — first installation")
	ui.Hint("Enter accepts the value in brackets; an empty answer with no value means \"not now\".")
	ui.Blank()

	save := func() error { return saveWizardAnswers(draftPath, answers) }

	domain, err := a.wizardAskDomain(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Domain = domain
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	email, err := ui.Prompt("Contact address for the certificate authority (optional)", firstNonEmpty(answers.Email, cfg.Email))
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.Email = email
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

	mnURL, invite, err := a.wizardAskEnrollment(ui, answers, cfg)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	answers.MNURL, answers.Invite = mnURL, invite
	if err := save(); err != nil {
		return a.wizardSaveFailed(err)
	}

	a.wizardSummary(ui, answers)
	proceed, err := ui.Confirm("Install this node now?", true)
	if err != nil {
		return wizardInputEnded(ui, err)
	}
	if !proceed {
		ui.Say("Nothing was changed. The answers are kept: run `termit-uv wizard` again to continue.")
		return exitOK
	}

	initArgs := []string{"init", "--config", *configPath, "--domain", answers.Domain}
	if answers.Email != "" {
		initArgs = append(initArgs, "--email", answers.Email)
	}
	if answers.Template != "" {
		initArgs = append(initArgs, "--decoy-template", answers.Template)
	}
	if answers.MNURL != "" {
		initArgs = append(initArgs, "--mn", answers.MNURL)
	}
	switch answers.Warp {
	case config.WarpOff:
		initArgs = append(initArgs, "--no-warp")
	case config.WarpSelected:
		initArgs = append(initArgs, "--warp", config.WarpSelected)
		if len(answers.WarpSelected) > 0 {
			initArgs = append(initArgs, "--warp-selected", strings.Join(answers.WarpSelected, ","))
		}
	default:
		initArgs = append(initArgs, "--warp", config.WarpAll)
	}

	ui.Blank()
	if code := a.runInit(initArgs); code != exitOK {
		// The draft stays: a second run continues from the same answers.
		ui.Error("the installation did not finish; the answers are kept for the next run")
		return code
	}

	if answers.Invite != "" {
		enrollArgs := []string{"enroll", "--config", *configPath, "--invite", answers.Invite}
		if answers.MNURL != "" {
			enrollArgs = append(enrollArgs, "--mn", answers.MNURL)
		}
		ui.Blank()
		if code := a.runEnroll(enrollArgs); code != exitOK {
			ui.Error("the node is installed but not in the mesh yet: fix the invite and run `termit-uv enroll`")
			return code
		}
	}

	if err := os.Remove(draftPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stderr, "termit-uv wizard: remove %s: %v\n", draftPath, err)
	}

	ui.Blank()
	ui.Title("Done")
	ui.State("ok", "this node is installed")
	return a.runOptions([]string{"--config", *configPath})
}

// wizardAlreadyInstalled is what a second run of the wizard shows: what the node is, not questions
// with answers the node already made.
func (a *application) wizardAlreadyInstalled(cfg *config.Config, configPath, draftPath string) int {
	if err := os.Remove(draftPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stderr, "termit-uv wizard: remove %s: %v\n", draftPath, err)
	}
	fmt.Fprintln(a.stdout, "This node is already installed; nothing to ask.")
	fmt.Fprintln(a.stdout)
	if code := a.runStatus([]string{"--config", configPath}); code != exitOK {
		return code
	}
	if node, err := state.LoadInstalled(cfg.Paths.StatePath()); err == nil {
		if tok := node.Options.Token; tok != "" {
			fmt.Fprintf(a.stdout, "\nclient options: https://%s/%s/\n", cfg.Domain, tok)
		} else {
			fmt.Fprintln(a.stdout, "\nclient options: none yet — `termit-uv options` renders one")
		}
	}
	return exitOK
}

// wizardAskDomain keeps asking until there is a domain, because there is nothing to install without
// one; every other question accepts an empty answer.
func (a *application) wizardAskDomain(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, error) {
	def := firstNonEmpty(answers.Domain, cfg.Domain)
	for {
		answer, err := ui.Prompt("Domain (a DNS name that points at this machine)", def)
		if err != nil {
			return "", err
		}
		if answer == "" {
			ui.Error("this node needs a domain: it is what the certificate is issued for")
			continue
		}
		if strings.ContainsAny(answer, " /:@") {
			ui.Error("a domain is a bare name such as node.example.com")
			continue
		}
		return answer, nil
	}
}

// wizardAskTemplate offers the archetypes the build can generate, with the configured one as default.
func (a *application) wizardAskTemplate(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, error) {
	known := decoy.Templates()
	def := firstNonEmpty(answers.Template, cfg.Decoy.Template, config.DefaultDecoyTemplate)
	ui.Blank()
	ui.Say("The page the node serves to a visitor who is not a client: " + strings.Join(known, ", ") + ".")
	for {
		answer, err := ui.Prompt("Masking site", def)
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
		ui.Error(fmt.Sprintf("%q is not one this build generates", answer))
	}
}

// wizardAskWarp decides where proxied traffic leaves, and, for "selected", which domains use WARP.
func (a *application) wizardAskWarp(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, []string, error) {
	def := firstNonEmpty(answers.Warp, cfg.Warp.Mode, config.WarpAll)
	ui.Blank()
	ui.Say("Egress: all — proxied traffic leaves through Cloudflare WARP;")
	ui.Say("        selected — only the listed domains do; off — it leaves directly from this node.")
	for {
		answer, err := ui.Prompt("Egress (all, selected, off)", def)
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
			line, err := ui.Prompt("Domains that use WARP, comma separated", selected)
			if err != nil {
				return "", nil, err
			}
			domains := splitList(line)
			if len(domains) == 0 {
				ui.Error("selected mode needs at least one domain")
				continue
			}
			return answer, domains, nil
		default:
			ui.Error("answer all, selected or off")
		}
	}
}

// wizardAskEnrollment asks for the server and the invite code. Both are optional: a node can be
// installed now and joined to the mesh later with `termit-uv enroll`.
func (a *application) wizardAskEnrollment(ui *termui.UI, answers wizardAnswers, cfg *config.Config) (string, string, error) {
	ui.Blank()
	ui.Say("The mesh: one management server hands every client the whole peer list.")
	defURL := firstNonEmpty(answers.MNURL, cfg.MN.URL)
	if defURL != "" {
		ui.Say("Server: " + defURL)
	}
	invite, err := ui.Prompt("Invite code from the server operator (empty to skip for now)", answers.Invite)
	if err != nil {
		return "", "", err
	}
	invite = strings.TrimSpace(invite)
	if invite == "" {
		return defURL, "", nil
	}
	mnURL := defURL
	for mnURL == "" {
		mnURL, err = ui.Prompt("Management server URL", "")
		if err != nil {
			return "", "", err
		}
		mnURL = strings.TrimRight(strings.TrimSpace(mnURL), "/")
		if mnURL == "" {
			ui.Error("an invite code is useless without the server it belongs to")
			continue
		}
		if !strings.HasPrefix(mnURL, "https://") && !strings.HasPrefix(mnURL, "http://") {
			ui.Error("the server URL starts with https://")
			mnURL = ""
		}
	}
	return mnURL, invite, nil
}

func (a *application) wizardSummary(ui *termui.UI, answers wizardAnswers) {
	ui.Blank()
	ui.Title("Ready to install")
	ui.KV("domain", answers.Domain)
	ui.KV("email", orNone(answers.Email))
	ui.KV("masking site", answers.Template)
	ui.KV("egress", answers.Warp+selectedNote(answers.WarpSelected))
	ui.KV("mesh", orNone(answers.MNURL))
	ui.KV("invite", orNone(maskInvite(answers.Invite)))
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
		ui.Say("cancelled; the answers so far are kept")
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
		switch {
		case step.Status == state.StepPresent:
		case nonEffectSteps[step.Name] && step.Status == state.StepPlanned:
			// A step that never reports its effect as present says nothing either way. A failure in it
			// is a different matter and falls through: a node whose host checks fail is not installed.
		default:
			return false, nil
		}
	}
	return true, nil
}

// nonEffectSteps are the steps whose Check never reports the effect as present, by design: preflight
// verifies the host on every run, and start restarts the agent on every run because init is also the
// upgrade path. Neither tells us whether the node is installed.
var nonEffectSteps = map[string]bool{"preflight": true, "start": true}

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
