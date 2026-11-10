package main

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/termui"
)

// newMenu builds a menu whose input is answers and whose commands are recorded instead of run.
func newMenu(answers string) (*menu, *[]string, *strings.Builder) {
	out := &strings.Builder{}
	ui := termui.New(strings.NewReader(answers), out, termui.Style{})
	var ran []string
	m := &menu{
		ui: ui,
		run: func(argv []string) int {
			ran = append(ran, strings.Join(argv, " "))
			return exitOK
		},
	}
	return m, &ran, out
}

func planOf(t *testing.T, m *menu, label, answers string) ([][]string, error) {
	t.Helper()
	for _, entry := range m.entries() {
		if entry.label != label {
			continue
		}
		m.ui = termui.New(strings.NewReader(answers), &strings.Builder{}, termui.Style{})
		return entry.plan(m.ui)
	}
	t.Fatalf("the menu has no %q entry", label)
	return nil, nil
}

func TestServerMenuEntriesMapToCommands(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	want := map[string]string{
		"status": "status",
		"backup": "backup",
		"help":   "help",
	}
	seen := map[string]bool{}
	for _, entry := range m.entries() {
		expected, ok := want[entry.label]
		if !ok {
			continue
		}
		seen[entry.label] = true
		plan, err := planOf(t, m, entry.label, "")
		if err != nil {
			t.Fatalf("%s: %v", entry.label, err)
		}
		if got := strings.Join(plan[0], " "); got != expected {
			t.Errorf("%s runs %q, want %q", entry.label, got, expected)
		}
	}
	for label := range want {
		if !seen[label] {
			t.Errorf("the menu has no %q entry", label)
		}
	}
}

func TestNodeMenuAsksForTheNodeItActsOn(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		answers string
		want    string
	}{
		"list needs no node": {"1\n", "uv list"},
		"show":               {"2\nnode1.example.com\n", "uv show node1.example.com"},
		"approve":            {"3\nnode1.example.com\n", "uv approve node1.example.com"},
		"block records a reason": {"5\nnode1.example.com\ncertificate is broken\n",
			"uv block node1.example.com --reason certificate is broken"},
		"remove records a reason": {"7\nnode1.example.com\nhardware gone\n",
			"uv rm node1.example.com --reason hardware gone"},
	}
	for name, tc := range cases {
		m, _, _ := newMenu("")
		plan, err := planOf(t, m, "nodes", tc.answers)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.Join(plan[0], " "); got != tc.want {
			t.Errorf("%s: runs %q, want %q", name, got, tc.want)
		}
	}
}

func TestNodeMenuRefusesAnEmptyNode(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	if _, err := planOf(t, m, "nodes", "2\n\n"); err == nil {
		t.Fatal("an empty node was accepted")
	}
}

func TestInviteMenu(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		answers string
		want    string
	}{
		"list":                  {"1\n", "invite list"},
		"create with a note":    {"2\nfor the second node\n", "invite create --note for the second node"},
		"create without a note": {"2\n\n", "invite create"},
		"revoke":                {"3\nABCD-EFGH\n", "invite revoke ABCD-EFGH"},
	}
	for name, tc := range cases {
		m, _, _ := newMenu("")
		plan, err := planOf(t, m, "invites", tc.answers)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.Join(plan[0], " "); got != tc.want {
			t.Errorf("%s: runs %q, want %q", name, got, tc.want)
		}
	}
}

func TestCheckMenuDefaultsToTheWholeMesh(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	plan, err := planOf(t, m, "check now", "\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "check --all" {
		t.Errorf("runs %q, want check --all", got)
	}

	one, _, _ := newMenu("")
	plan, err = planOf(t, one, "check now", "n\nnode1.example.com\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "check node1.example.com" {
		t.Errorf("runs %q, want one node", got)
	}
}

func TestPolicyMenu(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	plan, err := planOf(t, m, "policy", "1\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "policy show" {
		t.Errorf("runs %q", got)
	}

	set, _, _ := newMenu("")
	plan, err = planOf(t, set, "policy", "2\ncheck_interval\n1h\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "policy set check_interval 1h" {
		t.Errorf("runs %q", got)
	}
}

func TestSubscriptionMenuRotates(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	plan, err := planOf(t, m, "subscription", "1\nnode1.example.com\n\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "sub rotate node1.example.com" {
		t.Errorf("runs %q", got)
	}

	withDomain, _, _ := newMenu("")
	plan, err = planOf(t, withDomain, "subscription", "1\nnode1.example.com\nmn.example.com\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "sub rotate node1.example.com --domain mn.example.com" {
		t.Errorf("runs %q", got)
	}
}

func TestServerMenuRunsTheChosenCommand(t *testing.T) {
	t.Parallel()
	// Choosing status runs `status` and nothing else; quitting then leaves.
	m, ran, out := newMenu("1\n\nq\n")
	if code := m.loop(); code != exitOK {
		t.Fatalf("loop = %d, want 0", code)
	}
	if len(*ran) != 1 || (*ran)[0] != "status" {
		t.Errorf("ran %v, want [status]", *ran)
	}
	if !strings.Contains(out.String(), "Termit — server") {
		t.Errorf("no menu header:\n%s", out.String())
	}
}

func TestServerMenuStopsAtAFailingStep(t *testing.T) {
	t.Parallel()
	out := &strings.Builder{}
	m := &menu{
		ui:  termui.New(strings.NewReader("1\n\nq\n"), out, termui.Style{}),
		run: func([]string) int { return exitFailure },
	}
	if code := m.loop(); code != exitOK {
		t.Fatalf("loop = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "exited with status 1") {
		t.Errorf("a failing step was not reported:\n%s", out.String())
	}
}

func TestServerMenuEndsOnClosedInput(t *testing.T) {
	t.Parallel()
	m, ran, _ := newMenu("")
	if code := m.loop(); code != exitOK {
		t.Fatalf("loop = %d, want 0", code)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v with no input", *ran)
	}
}

func TestAdminTokenMenuConfirmsBeforeRotating(t *testing.T) {
	t.Parallel()
	// Declining must produce no command at all: a rotation nobody meant is unrecoverable without the
	// token that was just thrown away.
	declined, _, _ := newMenu("")
	if plan, err := planOf(t, declined, "admin token", "n\n"); err != nil || plan != nil {
		t.Fatalf("declining planned %v (%v)", plan, err)
	}

	printed, _, _ := newMenu("")
	plan, err := planOf(t, printed, "admin token", "y\n\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "admin rotate" {
		t.Errorf("runs %q, want admin rotate", got)
	}

	toFile, _, _ := newMenu("")
	plan, err = planOf(t, toFile, "admin token", "y\n/etc/termit/mn/admin.token\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "admin rotate --token-out /etc/termit/mn/admin.token" {
		t.Errorf("runs %q, want it to write the token to the file", got)
	}
}
