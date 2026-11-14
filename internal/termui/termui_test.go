package termui

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// session builds a UI reading from answers and drawing into a buffer.
func session(t *testing.T, answers string, style Style) (*UI, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	return New(strings.NewReader(answers), out, style), out
}

func TestMenuReturnsTheChosenKey(t *testing.T) {
	t.Parallel()
	ui, out := session(t, "2\n", Style{})
	key, err := ui.Menu([]Item{{Label: "status", Hint: "overview"}, {Label: "doctor"}})
	if err != nil {
		t.Fatalf("Menu: %v", err)
	}
	if key != "2" {
		t.Errorf("key = %q, want 2", key)
	}
	if !strings.Contains(out.String(), "1  status") || !strings.Contains(out.String(), "2  doctor") {
		t.Errorf("the menu did not list both items:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "q") {
		t.Error("the menu does not offer a way out")
	}
}

func TestMenuAcceptsALabel(t *testing.T) {
	t.Parallel()
	// People read labels, not numbers; typing the label they see must work.
	ui, _ := session(t, "doctor\n", Style{})
	key, err := ui.Menu([]Item{{Label: "status"}, {Label: "doctor"}})
	if err != nil {
		t.Fatalf("Menu: %v", err)
	}
	if key != "2" {
		t.Errorf("key = %q, want 2", key)
	}
}

func TestMenuKeepsAskingAfterNonsense(t *testing.T) {
	t.Parallel()
	ui, out := session(t, "banana\n9\n\n1\n", Style{})
	key, err := ui.Menu([]Item{{Label: "status"}, {Label: "doctor"}})
	if err != nil {
		t.Fatalf("Menu: %v", err)
	}
	if key != "1" {
		t.Errorf("key = %q, want 1", key)
	}
	if count := strings.Count(out.String(), "is not one of the choices"); count != 2 {
		t.Errorf("rejected %d answers, want 2:\n%s", count, out.String())
	}
}

func TestMenuQuits(t *testing.T) {
	t.Parallel()
	for _, answer := range []string{"q\n", "Q\n", "quit\n", "exit\n"} {
		ui, _ := session(t, answer, Style{})
		key, err := ui.Menu([]Item{{Label: "status"}})
		if err != nil {
			t.Fatalf("Menu(%q): %v", answer, err)
		}
		if key != "" {
			t.Errorf("Menu(%q) = %q, want no choice", answer, key)
		}
	}
}

func TestMenuReportsEndOfInput(t *testing.T) {
	t.Parallel()
	// A closed stdin must end the session, not spin forever on empty answers.
	ui, _ := session(t, "", Style{})
	if _, err := ui.Menu([]Item{{Label: "status"}}); err != io.EOF {
		t.Fatalf("Menu = %v, want io.EOF", err)
	}
}

func TestMenuNeedsAnItem(t *testing.T) {
	t.Parallel()
	ui, _ := session(t, "", Style{})
	if _, err := ui.Menu(nil); err == nil {
		t.Fatal("an empty menu was accepted")
	}
}

func TestMenuNumbersItsOwnKeys(t *testing.T) {
	t.Parallel()
	// A caller may pin a key; the rest get numbers, and both are printed.
	ui, out := session(t, "x\n", Style{})
	key, err := ui.Menu([]Item{{Label: "a"}, {Label: "b"}, {Key: "x", Label: "c"}})
	if err != nil {
		t.Fatalf("Menu: %v", err)
	}
	if key != "x" {
		t.Errorf("key = %q, want the explicit key x", key)
	}
	rendered := out.String()
	for _, want := range []string{"1  a", "2  b", "x  c"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("menu does not show %q:\n%s", want, rendered)
		}
	}
}

func TestPromptTakesTheDefault(t *testing.T) {
	t.Parallel()
	ui, out := session(t, "\n", Style{})
	answer, err := ui.Prompt("Domain", "node1.example.com")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if answer != "node1.example.com" {
		t.Errorf("answer = %q, want the default", answer)
	}
	if !strings.Contains(out.String(), "node1.example.com") {
		t.Errorf("the default was not shown:\n%s", out.String())
	}
}

func TestPromptTrimsAndReturnsInput(t *testing.T) {
	t.Parallel()
	ui, _ := session(t, "  node2.example.com  \n", Style{})
	answer, err := ui.Prompt("Domain", "")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if answer != "node2.example.com" {
		t.Errorf("answer = %q, want it trimmed", answer)
	}
}

func TestConfirmReadsTheAnswer(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		answer string
		def    bool
		want   bool
	}{
		"yes":                       {"y\n", false, true},
		"short russian":             {"д\n", false, true},
		"no":                        {"n\n", true, false},
		"empty takes default true":  {"\n", true, true},
		"empty takes default false": {"\n", false, false},
		"nonsense takes default":    {"maybe\n", true, true},
	}
	for name, tc := range cases {
		ui, _ := session(t, tc.answer, Style{})
		got, err := ui.Confirm("Install?", tc.def)
		if err != nil {
			t.Fatalf("%s: Confirm: %v", name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestColourOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	plain, plainOut := session(t, "1\n", Style{})
	if plain.Colour() {
		t.Error("a plain session reports colour")
	}
	if _, err := plain.Menu([]Item{{Label: "status"}}); err != nil {
		t.Fatalf("Menu: %v", err)
	}
	if strings.Contains(plainOut.String(), "\x1b[") {
		t.Errorf("a plain session emitted an escape:\n%q", plainOut.String())
	}

	coloured, colouredOut := session(t, "1\n", Colour())
	if !coloured.Colour() {
		t.Error("a coloured session reports no colour")
	}
	coloured.Clear()
	coloured.Title("Termit")
	coloured.State("ok", "everything runs")
	coloured.State("fail", "it does not")
	if _, err := coloured.Menu([]Item{{Label: "status"}}); err != nil {
		t.Fatalf("Menu: %v", err)
	}
	if !strings.Contains(colouredOut.String(), "\x1b[") {
		t.Error("a coloured session emitted no escape")
	}
	if !strings.Contains(colouredOut.String(), "\x1b[32m") {
		t.Error("an ok state was not green")
	}
	if !strings.Contains(colouredOut.String(), "\x1b[31m") {
		t.Error("a failure was not red")
	}
}

func TestClearOnlyOnATerminal(t *testing.T) {
	t.Parallel()
	plain, out := session(t, "", Style{})
	plain.Clear()
	if out.Len() != 0 {
		t.Errorf("a plain session cleared the screen: %q", out.String())
	}
	coloured, out2 := session(t, "", Colour())
	coloured.Clear()
	if !strings.HasPrefix(out2.String(), "\x1b[2J") {
		t.Errorf("a coloured session did not clear: %q", out2.String())
	}
}

func TestPauseWaitsForALine(t *testing.T) {
	t.Parallel()
	ui, out := session(t, "\n", Style{})
	if err := ui.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !strings.Contains(out.String(), "Enter") {
		t.Errorf("Pause asked nothing:\n%s", out.String())
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("Pause left the cursor mid-line: %q", out.String())
	}
}
