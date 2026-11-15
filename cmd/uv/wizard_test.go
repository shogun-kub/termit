package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
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
		"node.example.com", // domain
		"ops@example.com",  // email
		"nope",             // not a template this build generates
		"portal",           // the template
		"all",              // egress
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
	again, againOut := wizardApp(t, "\n\n\n\n\nn\n")
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
	app, _ := wizardApp(t, "node.example.com\n\nportal\nall\n\nn\n")
	if code := app.run([]string{"wizard", "--config", app.configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	draftPath := wizardDraftPath(app.configPath)
	if _, err := os.Stat(draftPath); err != nil {
		t.Fatalf("the first run kept no draft: %v", err)
	}

	// --fresh forgets the answers, so the domain question comes back without a default. The run then
	// writes a new draft, which is correct: the operator started over, not stopped.
	app.in = strings.NewReader("\n\n\n\n\nn\n")
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
	app, out := wizardApp(t, "\nnode.example.com\n\nportal\nall\n\nn\n")
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

func TestWizardContinuesAnUnfinishedInstallWithItsAnswers(t *testing.T) {
	t.Parallel()
	app, _, configPath := optionsFixture(t, "")
	app.in = strings.NewReader("\n\n\n\n\nn\n")
	app.completeFn = func(*config.Config, string) (bool, error) { return false, nil }
	if code := app.run([]string{"wizard", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	rendered := app.stdout.(*strings.Builder).String()
	// The half-installed node already knows its domain: the question offers it, so Enter is enough.
	if !strings.Contains(rendered, "Domain (a DNS name that points at this machine) [node.example.com]") {
		t.Errorf("the installed domain is not offered as the default:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Nothing was changed") {
		t.Errorf("declining the install did not say that nothing changed:\n%s", rendered)
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
