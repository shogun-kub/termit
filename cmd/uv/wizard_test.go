package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// wizardApp is an application whose wizard input is the given script and whose configuration path is a
// fresh temporary directory, so a test machine can never be touched by a step that would install.
func wizardApp(t *testing.T, answers string) (*application, *strings.Builder) {
	t.Helper()
	app, _, stderr := newTestApp(t)
	out := &strings.Builder{}
	app.stdout = out
	app.stderr = stderr
	app.in = strings.NewReader(answers)
	app.configPath = filepath.Join(t.TempDir(), "config.yaml")
	return app, out
}

func TestWizardRemembersAnswersBetweenRuns(t *testing.T) {
	t.Parallel()
	app, out := wizardApp(t, strings.Join([]string{
		"en",               // language
		"node.example.com", // domain
		"ops@example.com",  // email
		"",                 // ACME: the public directory
		"nope",             // not a template this build generates
		"portal",           // the template
		"all",              // egress
		"n",                // TLS only: no Hysteria2 on UDP/443
		"n",                // do not harden this node
		"",                 // no invite code
		"n",                // do not install
	}, "\n")+"\n")

	if code := app.run([]string{"wizard", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	draftPath := wizardDraftPath(app.configPath)
	draft, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatalf("the answers were not kept: %v", err)
	}
	for _, want := range []string{"domain: node.example.com", "email: ops@example.com", "decoy_template: portal"} {
		if !strings.Contains(string(draft), want) {
			t.Errorf("the draft does not contain %q:\n%s", want, draft)
		}
	}
	if !strings.Contains(out.String(), "not one this build generates") {
		t.Error("the wizard accepted an unknown template without saying so")
	}
	if !strings.Contains(out.String(), "Nothing was changed") {
		t.Error("declining the install did not say that nothing changed")
	}

	// A second run offers the same answers, and Enter alone accepts them: the script has one empty line
	// per question and then declines the install again.
	again, againOut := wizardApp(t, "en\n\n\n\n\n\n\nn\n\nn\n")
	// The draft lives beside the configuration, so the second run has to look at the same file.
	again.configPath = app.configPath
	if code := again.run([]string{"wizard", "--config", again.configPath}); code != exitOK {
		t.Fatalf("second run exit = %d, want 0", code)
	}
	if !strings.Contains(againOut.String(), "node.example.com") {
		t.Errorf("the second run did not offer the domain given in the first:\n%s", againOut.String())
	}
	if !strings.Contains(againOut.String(), "ops@example.com") {
		t.Errorf("the second run did not offer the email given in the first:\n%s", againOut.String())
	}
}

func TestWizardFreshDiscardsTheDraft(t *testing.T) {
	t.Parallel()
	app, _ := wizardApp(t, "en\nnode.example.com\n\n\nportal\nall\nn\nn\n\nn\n")
	if code := app.run([]string{"wizard", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	draftPath := wizardDraftPath(app.configPath)
	if _, err := os.Stat(draftPath); err != nil {
		t.Fatalf("the first run kept no draft: %v", err)
	}

	// --fresh forgets the answers, so the domain question comes back without a default. The run then
	// writes a new draft, which is correct: the operator started over, not stopped.
	app.in = strings.NewReader("en\n\n\n\n\n\n\nn\n\nn\n")
	app.stdout = &strings.Builder{}
	if code := app.run([]string{"wizard", "--fresh", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(app.stdout.(*strings.Builder).String(), "[node.example.com]") {
		t.Errorf("--fresh kept the old domain as a default:\n%s", app.stdout.(*strings.Builder).String())
	}
}

func TestWizardRequiresADomain(t *testing.T) {
	t.Parallel()
	// An empty answer with no default must not become an empty domain, and the wizard must ask again.
	app, out := wizardApp(t, "en\n\nnode.example.com\n\n\nportal\nall\nn\nn\n\nn\n")
	if code := app.run([]string{"wizard", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "needs a domain") {
		t.Errorf("an empty domain was accepted:\n%s", out.String())
	}
}

func TestWizardWithoutInputCancels(t *testing.T) {
	t.Parallel()
	app, out := wizardApp(t, "")
	if code := app.run([]string{"wizard", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("an exhausted input was not reported as a cancel:\n%s", out.String())
	}
}

func TestWizardOnAnInstalledNodeAsksNothing(t *testing.T) {
	t.Parallel()
	app, _, configPath := optionsFixture(t, "")
	app.in = strings.NewReader("")
	app.completeFn = func(*config.Config, string) (bool, error) { return true, nil }
	if code := app.run([]string{"wizard", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	rendered := app.stdout.(*strings.Builder).String()
	if !strings.Contains(rendered, "already installed") {
		t.Errorf("the wizard asked questions on an installed node:\n%s", rendered)
	}
	if strings.Contains(rendered, "Domain (a DNS name") {
		t.Error("the wizard asked for a domain on an installed node")
	}
	if !strings.Contains(rendered, "nothing to ask") {
		t.Errorf("the wizard did not say it has nothing to ask:\n%s", rendered)
	}
}

// A node that is installed but behind this build is an upgrade, not a first installation: asking for
// the domain again would be an insult, and running init from the wizard would hide the upgrade.
func TestWizardSaysWhatToRunOnAnOutdatedNode(t *testing.T) {
	t.Parallel()
	app, _, configPath := optionsFixture(t, "")
	app.in = strings.NewReader("")
	app.completeFn = func(*config.Config, string) (bool, error) { return false, nil }
	if code := app.run([]string{"wizard", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	rendered := app.stdout.(*strings.Builder).String()
	if !strings.Contains(rendered, "this build would change its configuration") {
		t.Errorf("the wizard did not explain the state of the node:\n%s", rendered)
	}
	if !strings.Contains(rendered, "termit-uv init") {
		t.Errorf("the wizard did not say what to run:\n%s", rendered)
	}
	if strings.Contains(rendered, "Domain (a DNS name") {
		t.Errorf("the wizard asked for a domain on an installed node:\n%s", rendered)
	}
}

// The install branch hands `init` the flags it parses, not the subcommand name: the wizard was the one
// path that could not install, because it passed "init" as an argument nobody read.
func TestWizardHandsInstallTheFlagsWithoutTheSubcommand(t *testing.T) {
	t.Parallel()
	app, _, configPath := optionsFixture(t, "")
	app.in = strings.NewReader("en\n\n\n\n\nportal\noff\nn\nn\n\ny\n")
	if code := app.run([]string{"wizard", "--config", configPath}); code == exitUsage {
		t.Fatalf("the wizard handed init an argument it does not parse:\n%v", app.stderr)
	}
}

// A node with WARP on always plans warp-egress: its Check is a measurement, not a file. Treating that
// plan as work told every such owner to run init forever, which is what this test pins down.
func TestStepSatisfiedTreatsMeasuringStepsAsDone(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"preflight", "start", "warp", "warp-egress"} {
		if !stepSatisfied(install.StepReport{Name: name, Status: state.StepPlanned}) {
			t.Errorf("%s is treated as work although it only measures", name)
		}
	}
	if stepSatisfied(install.StepReport{Name: "warp-egress", Status: state.StepFailed}) {
		t.Error("a failed step counts as satisfied")
	}
	if stepSatisfied(install.StepReport{Name: "xray-config", Status: state.StepPlanned}) {
		t.Error("a planned step with a real effect counts as satisfied")
	}
}

func TestInitCompleteIsFalseWithoutAServiceOrCertificate(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	done, err := app.initComplete(cfg, configPath)
	if err != nil {
		t.Fatalf("initComplete: %v", err)
	}
	if done {
		t.Error("a node with no certificate and no units is reported as complete")
	}
}

func TestInitCompleteIsFalseForAMissingStateFile(t *testing.T) {
	t.Parallel()
	app, _, _ := newTestApp(t)
	cfg := config.Default()
	cfg.Domain = "node.example.com"
	cfg.Paths.StateDir = filepath.Join(t.TempDir(), "state")
	done, err := app.initComplete(cfg, cfg.Paths.ConfigFile)
	if err != nil {
		t.Fatalf("initComplete: %v", err)
	}
	if done {
		t.Error("a node that was never installed is reported as complete")
	}
}

// The public directory is init's own default, so the standard installation must not learn a flag it does
// not need; a private one has to reach init, with skip-verify only when the operator asked for it.
func TestWizardInitArgsForTheCertificateAuthority(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		answers wizardAnswers
		want    []string
		absent  []string
	}{
		"public by default": {
			answers: wizardAnswers{Domain: "node.example.com"},
			absent:  []string{"--acme-ca", "--acme-skip-tls-verify"},
		},
		"the public directory named": {
			answers: wizardAnswers{Domain: "node.example.com", ACME: acme.DefaultDirectory},
			absent:  []string{"--acme-ca", "--acme-skip-tls-verify"},
		},
		"a private directory": {
			answers: wizardAnswers{Domain: "node.example.com", ACME: "https://ca.lan:14000/dir"},
			want:    []string{"--acme-ca", "https://ca.lan:14000/dir"},
			absent:  []string{"--acme-skip-tls-verify"},
		},
		"a private directory whose certificate is not trusted": {
			answers: wizardAnswers{Domain: "node.example.com", ACME: "https://ca.lan:14000/dir", ACMESkip: true},
			want:    []string{"--acme-ca", "https://ca.lan:14000/dir", "--acme-skip-tls-verify"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := strings.Join(wizardInitArgs(tc.answers, "/etc/termit/uv/config.yaml"), " ")
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("init args %q do not contain %q", got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("init args %q contain %q", got, absent)
				}
			}
		})
	}
}

// The wizard finishes what a previous run started: an installed node outside the mesh is not "already
// installed" while the draft still holds a code. The decision is the state, and this is the state it turns
// on — the enrollment itself is the lab scenario, which cannot run in the default test.
func TestEnrolledReportsWhetherTheNodeJoinedTheMesh(t *testing.T) {
	t.Parallel()
	app, cfg, _ := optionsFixture(t, "")
	if !app.enrolled(cfg) {
		t.Fatal("a node with an agent key is reported as not enrolled")
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	node.Credentials = nil
	if err := node.Save(cfg.Paths.StatePath()); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if app.enrolled(cfg) {
		t.Error("a node with no agent key is reported as enrolled")
	}
}

// A private directory has to reach the draft as well, or a resumed run asks the skip question again and
// the certificate step fails a second time; the summary has to name what it will use.
func TestWizardKeepsTheCertificateAuthorityInTheDraft(t *testing.T) {
	t.Parallel()
	app, out := wizardApp(t, "en\nnode.example.com\nops@example.com\nhttps://ca.lan:14000/dir\ny\nportal\nall\nn\nn\n\nn\n")
	if code := app.run([]string{"wizard", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	draft, err := os.ReadFile(wizardDraftPath(app.configPath))
	if err != nil {
		t.Fatalf("the answers were not kept: %v", err)
	}
	for _, want := range []string{"acme: https://ca.lan:14000/dir", "acme_skip_verify: true"} {
		if !strings.Contains(string(draft), want) {
			t.Errorf("the draft does not contain %q:\n%s", want, draft)
		}
	}
	if !strings.Contains(out.String(), "https://ca.lan:14000/dir") {
		t.Errorf("the summary does not name the certificate authority:\n%s", out.String())
	}
}

// The QUIC answer is the one that changes what a client can pick on the first day, so it has to reach
// init as a flag and stay in the draft for a resumed run.
func TestWizardKeepsTheQuicAnswerInTheDraft(t *testing.T) {
	t.Parallel()
	app, out := wizardApp(t, "en\nnode.example.com\n\n\nportal\nall\ny\nn\n\nn\n")
	if code := app.run([]string{"wizard", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	draft, err := os.ReadFile(wizardDraftPath(app.configPath))
	if err != nil {
		t.Fatalf("the answers were not kept: %v", err)
	}
	if !strings.Contains(string(draft), "hysteria: true") {
		t.Errorf("the draft does not contain the transport answer:\n%s", draft)
	}
	if !strings.Contains(out.String(), "UDP/443") {
		t.Errorf("the summary does not name the port the answer opens:\n%s", out.String())
	}
}

func TestWizardInitArgsCarryTheQuicTransport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		on   bool
		want bool
	}{{true, true}, {false, false}} {
		answers := wizardAnswers{Domain: "node.example.com", Warp: config.WarpAll, Hysteria: tc.on}
		args := strings.Join(wizardInitArgs(answers, "/etc/termit/uv/config.yaml"), " ")
		if got := strings.Contains(args, "--hysteria"); got != tc.want {
			t.Errorf("hysteria=%v: --hysteria in %q = %v, want %v", tc.on, args, got, tc.want)
		}
	}
}
