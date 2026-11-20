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

// A number is the answer the menu prints, and it has to reach the command that number showed.
func TestChooseRunsTheOptionThatWasPicked(t *testing.T) {
	t.Parallel()
	screen, _ := ui("3")
	plan, err := menuplan.Choose(screen, i18n.New(i18n.EN), menuplan.Firewall())
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if len(plan) != 1 || strings.Join(plan[0], " ") != "firewall off" {
		t.Fatalf("plan = %q, want one `firewall off`", plan)
	}
}

// The ban list's three verbs name the address they act on; the value the operator types is the last word
// of the command line.
func TestChooseAppendsTheValueItAskedFor(t *testing.T) {
	t.Parallel()
	screen, _ := ui("4", "203.0.113.7")
	plan, err := menuplan.Choose(screen, i18n.New(i18n.EN), menuplan.Bans())
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if len(plan) != 1 || strings.Join(plan[0], " ") != "bans unban 203.0.113.7" {
		t.Fatalf("plan = %q, want `bans unban 203.0.113.7`", plan)
	}
}

// An empty answer is refused rather than turned into a command with no argument.
func TestChooseRefusesAnEmptyAddress(t *testing.T) {
	t.Parallel()
	screen, _ := ui("3", "")
	if _, err := menuplan.Choose(screen, i18n.New(i18n.EN), menuplan.Bans()); err == nil {
		t.Fatal("an empty address produced a plan")
	}
}

// "q" is how every menu says no; a sub-menu that ignored it would trap the operator. An empty plan means
// the caller reports the cancellation.
func TestChooseCancelsOnQuit(t *testing.T) {
	t.Parallel()
	screen, _ := ui("q")
	plan, err := menuplan.Choose(screen, i18n.New(i18n.EN), menuplan.Firewall())
	if err != nil || plan != nil {
		t.Fatalf("plan = %q, err = %v, want nothing", plan, err)
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
