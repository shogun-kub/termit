package menuplan_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/menuplan"
	"github.com/shogun-kub/termit/internal/termui"
)

// ui builds a UI that answers with scripted lines and writes nowhere a reader cares about.
func ui(answers ...string) (*termui.UI, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return termui.New(strings.NewReader(strings.Join(answers, "\n")+"\n"), out, termui.Style{}), out
}

// The option the operator picked is the command that runs, and the value it asked for is the last word of
// the command line.
func TestRunBuildsTheCommandTheOptionNames(t *testing.T) {
	t.Parallel()
	tr := i18n.New(i18n.EN)

	screen, _ := ui("")
	plan, err := menuplan.Firewall()[2].Run(screen, tr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(plan[0], " ") != "firewall off" {
		t.Fatalf("plan = %q, want `firewall off`", plan)
	}

	screen, _ = ui("203.0.113.7")
	plan, err = menuplan.Bans()[3].Run(screen, tr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(plan[0], " ") != "bans unban 203.0.113.7" {
		t.Fatalf("plan = %q, want `bans unban 203.0.113.7`", plan)
	}
}

// An address is what the command acts on, so an empty answer asks again instead of running a command with
// nothing to act on.
func TestRunAsksAgainForAnEmptyValue(t *testing.T) {
	t.Parallel()
	screen, _ := ui("", "203.0.113.7")
	plan, err := menuplan.Bans()[2].Run(screen, i18n.New(i18n.EN))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Join(plan[0], " ") != "bans block 203.0.113.7" {
		t.Fatalf("plan = %q, want `bans block 203.0.113.7`", plan)
	}
}

// `firewall rollback` is missing on purpose: it is the timer's command and its own help says it is not for
// interactive use. This test is here so adding it back is a decision, not an accident.
func TestRollbackStaysOutOfTheMenu(t *testing.T) {
	t.Parallel()
	for _, option := range menuplan.Firewall() {
		if strings.Join(option.Argv, " ") == "firewall rollback" {
			t.Fatal("firewall rollback is in the menu; the timer runs it")
		}
	}
}

// Both binaries draw these, so every label and hint has to exist in both languages.
func TestEveryOptionIsTranslated(t *testing.T) {
	t.Parallel()
	for _, options := range [][]menuplan.Option{menuplan.Firewall(), menuplan.Bans()} {
		for _, option := range options {
			for _, lang := range []i18n.Lang{i18n.RU, i18n.EN} {
				tr := i18n.New(lang)
				if got := tr.T(option.Label); got == string(option.Label) {
					t.Errorf("%s: no %s label", option.Label, lang)
				}
				if got := tr.T(option.Hint); got == string(option.Hint) {
					t.Errorf("%s: no %s hint", option.Hint, lang)
				}
			}
		}
	}
}
