package main

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// menuApp builds an application whose menu input is the given answers, whose configuration path points
// nowhere (so the machine running the tests cannot change the outcome), and whose terminal is faked.
func menuApp(t *testing.T, answers string, terminal bool) (*application, *strings.Builder) {
	t.Helper()
	app, stdout, stderr := newTestApp(t)
	out := &strings.Builder{}
	app.stdout = out
	app.stderr = stderr
	app.in = strings.NewReader(answers)
	app.interactiveFn = func() bool { return terminal }
	app.configPath = t.TempDir() + "/config.yaml"
	_ = stdout
	return app, out
}

func TestBareBinaryOpensTheMenuOnATerminal(t *testing.T) {
	t.Parallel()
	// One action that fails fast (nothing is installed at the temporary path), then quit.
	app, out := menuApp(t, "1\n\nq\n", true)
	if code := app.run(nil); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "Termit — node") {
		t.Errorf("no menu header:\n%s", rendered)
	}
	if !strings.Contains(rendered, "install or converge") {
		t.Errorf("the menu does not offer installing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "$ termit-uv status") {
		t.Errorf("choosing status did not run the status command:\n%s", rendered)
	}
}

func TestBareBinaryStaysUsageOffATerminal(t *testing.T) {
	t.Parallel()
	// A script that runs the bare binary with no arguments must still get exit 2 and the usage text, never a
	// prompt waiting for input that will never come.
	app, _, stderr := newTestApp(t)
	app.interactiveFn = func() bool { return false }
	app.in = strings.NewReader("")
	if code := app.run(nil); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("no usage text:\n%s", stderr.String())
	}
}

func TestMenuSubcommandRunsTheMenuEvenOffATerminal(t *testing.T) {
	t.Parallel()
	app, out := menuApp(t, "q\n", false)
	if code := app.run([]string{"menu"}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "Termit — node") {
		t.Errorf("`termit-uv menu` did not draw the menu:\n%s", out.String())
	}
}

func TestMenuEndsOnClosedInput(t *testing.T) {
	t.Parallel()
	// Piped input that runs out is a finished session, not a failure: an empty pipe into the menu exits 0.
	app, _ := menuApp(t, "", false)
	if code := app.run([]string{"menu"}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestIndexMapsAMenuAnswerToAPosition(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		key   string
		index int
		fail  bool
	}{
		"first": {"1", 0, false},
		"tenth": {"10", 9, false},
		"zero":  {"0", 0, true},
		"label": {"status", 0, true},
		"empty": {"", 0, true},
	}
	for name, tc := range cases {
		index, err := termui.Index(tc.key)
		if tc.fail {
			if err == nil {
				t.Errorf("%s: %q was accepted", name, tc.key)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if index != tc.index {
			t.Errorf("%s: index = %d, want %d", name, index, tc.index)
		}
	}
}

func TestMenuEntriesRunTheCommandsTheyName(t *testing.T) {
	t.Parallel()
	// Every question-free entry must map to an argv the CLI already understands; the menu is a front
	// end, so a mismatch here is a menu that cannot work.
	app, _ := menuApp(t, "", false)
	want := map[string]string{
		"status":          "status",
		"doctor":          "doctor",
		"sync now":        "sync",
		"check drift":     "sync --check",
		"print vless URI": "uri",
		"help":            "help",
	}
	seen := map[string]bool{}
	for _, entry := range app.menuEntries() {
		wantArgv, ok := want[entry.label]
		if !ok {
			continue
		}
		seen[entry.label] = true
		plan, err := entry.plan(termui.New(strings.NewReader(""), &strings.Builder{}, termui.Style{}))
		if err != nil {
			t.Fatalf("%s: %v", entry.label, err)
		}
		if len(plan) != 1 {
			t.Fatalf("%s: %d commands, want 1", entry.label, len(plan))
		}
		if got := strings.Join(plan[0], " "); got != wantArgv {
			t.Errorf("%s runs %q, want %q", entry.label, got, wantArgv)
		}
	}
	for label := range want {
		if !seen[label] {
			t.Errorf("the menu has no %q entry", label)
		}
	}
}

func TestInstallAsksForADomainAndEmail(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "node1.example.com\nme@example.com\ny\n", false)
	entry := entryNamed(t, app, "install or converge")
	plan, err := entry.plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "init --domain node1.example.com --email me@example.com" {
		t.Errorf("plan = %q", got)
	}
}

func TestInstallPassesNoWarpWhenDeclined(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "node1.example.com\nme@example.com\n\n", false)
	entry := entryNamed(t, app, "install or converge")
	plan, err := entry.plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if strings.Contains(strings.Join(plan[0], " "), "--no-warp") {
		t.Errorf("an empty answer (the default, yes) passed --no-warp: %v", plan[0])
	}

	declined, _ := menuApp(t, "node1.example.com\nme@example.com\nn\n", false)
	plan, err = entryNamed(t, declined, "install or converge").plan(
		termui.New(declined.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !strings.Contains(strings.Join(plan[0], " "), "--no-warp") {
		t.Errorf("declining WARP did not pass --no-warp: %v", plan[0])
	}
}

func TestInstallRequiresADomain(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "\n", false)
	entry := entryNamed(t, app, "install or converge")
	if _, err := entry.plan(termui.New(app.in, &strings.Builder{}, termui.Style{})); err == nil {
		t.Fatal("an empty domain was accepted")
	}
}

func TestInstallConvergesAnInstalledNodeWithoutQuestions(t *testing.T) {
	t.Parallel()
	// With a configuration in place, "install" must not ask anything: init is idempotent and the
	// answers already live in the file.
	app, _ := menuApp(t, "", false)
	cfg := config.Default()
	cfg.Domain = "node1.example.com"
	if err := cfg.Save(app.configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entry := entryNamed(t, app, "install or converge")
	plan, err := entry.plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "init" {
		t.Errorf("plan = %q, want plain init", got)
	}
}

func TestEnrollRequiresAnInvite(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "https://mn.example.com\n\n", false)
	entry := entryNamed(t, app, "join the mesh")
	if _, err := entry.plan(termui.New(app.in, &strings.Builder{}, termui.Style{})); err == nil {
		t.Fatal("an empty invite code was accepted")
	}
}

func TestEnrollBuildsTheCommand(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "https://mn.example.com\nABCD-EFGH-IJKL\n", false)
	entry := entryNamed(t, app, "join the mesh")
	plan, err := entry.plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "enroll --invite ABCD-EFGH-IJKL --mn https://mn.example.com" {
		t.Errorf("plan = %q", got)
	}
}

func TestDevicesAddAsksForALabel(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "2\nlaptop\n", false)
	plan, err := entryNamed(t, app, "devices").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "device add --label laptop" {
		t.Errorf("plan = %q", got)
	}
}

func TestDevicesRemovePassesTheLabel(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "3\nold-phone\n", false)
	plan, err := entryNamed(t, app, "devices").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "device remove old-phone" {
		t.Errorf("plan = %q", got)
	}
}

func TestWarpModes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"1\n":    "warp status",
		"2\n":    "warp mode all",
		"3\n":    "warp mode off",
		"4\nx\n": "warp mode selected --selected x",
	}
	for answers, want := range cases {
		app, _ := menuApp(t, answers, false)
		plan, err := entryNamed(t, app, "egress (WARP)").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
		if err != nil {
			t.Fatalf("%q: plan: %v", answers, err)
		}
		if got := strings.Join(plan[0], " "); got != want {
			t.Errorf("%q: plan = %q, want %q", answers, got, want)
		}
	}
}

func TestUninstallAsksTwiceAndNamesWhatItTakes(t *testing.T) {
	t.Parallel()
	// A single "y" is not enough, and the second question must say what goes.
	app, _ := menuApp(t, "y\nn\n", false)
	out := &strings.Builder{}
	plan, err := entryNamed(t, app, "uninstall").plan(termui.New(app.in, out, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan != nil {
		t.Errorf("a declined confirmation still planned %v", plan)
	}
	if !strings.Contains(out.String(), "units, the core") {
		t.Errorf("the confirmation does not say what is removed:\n%s", out.String())
	}

	purging, _ := menuApp(t, "y\ny\n", false)
	out = &strings.Builder{}
	plan, err = entryNamed(t, purging, "uninstall").plan(termui.New(purging.in, out, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan) != 1 || strings.Join(plan[0], " ") != "uninstall --purge" {
		t.Errorf("plan = %v, want uninstall --purge", plan)
	}
	if !strings.Contains(out.String(), "every secret and certificate") {
		t.Errorf("the confirmation does not name the secrets:\n%s", out.String())
	}
}

func TestDecoyTemplateIsValidated(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "сова\n", false)
	if _, err := entryNamed(t, app, "decoy site").plan(
		termui.New(app.in, &strings.Builder{}, termui.Style{})); err == nil {
		t.Fatal("an unknown archetype was accepted")
	}
}

// entryNamed finds a menu entry by label, so a test does not depend on its position.
func entryNamed(t *testing.T, app *application, label string) menuEntry {
	t.Helper()
	for _, entry := range app.menuEntries() {
		if entry.label == label {
			return entry
		}
	}
	t.Fatalf("the menu has no %q entry", label)
	return menuEntry{}
}

// `menu --config <path>` has to inspect and act on that node: the flag used to be dropped, so the menu
// showed the default configuration while the operator was looking at another one.
func TestMenuHonoursTheConfigFlag(t *testing.T) {
	t.Parallel()
	app, out := menuApp(t, "\nq\n", true)
	path := t.TempDir() + "/elsewhere.yaml"
	if code := app.runMenu([]string{"--config", path}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if app.configPath != path {
		t.Errorf("the menu reads %q, want %q", app.configPath, path)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "Termit — node") {
		t.Errorf("no menu header:\n%s", rendered)
	}

	// An argument that is not a flag is a mistake, not something to ignore silently.
	app, _ = menuApp(t, "\n", true)
	if code := app.runMenu([]string{"nonsense"}); code != exitUsage {
		t.Errorf("exit = %d, want %d for a stray argument", code, exitUsage)
	}
}

// Every command the menu builds inherits the flag: the first word selects the subcommand, so the flags go
// after it.
func TestAppendFlags(t *testing.T) {
	t.Parallel()
	got := strings.Join(appendFlags([]string{"status"}, []string{"--config", "/etc/termit/uv/config.yaml"}), " ")
	if got != "status --config /etc/termit/uv/config.yaml" {
		t.Errorf("appendFlags = %q", got)
	}
	if got := appendFlags([]string{"status"}, nil); len(got) != 1 {
		t.Errorf("appendFlags without a base changed the command: %v", got)
	}
}
