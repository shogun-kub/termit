package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/i18n"
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
	app, out := menuApp(t, "1\n1\n\nq\n", true)
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
		"lift a block":    "unblock",
		"guard":           "guard status",
		"help":            "help",
	}
	seen := map[string]bool{}
	var walk func(entries []menuEntry)
	walk = func(entries []menuEntry) {
		for _, entry := range entries {
			if entry.children != nil {
				walk(entry.children())
				continue
			}
			label := entry.labelOf(app.translator())
			wantArgv, ok := want[label]
			if !ok || seen[label] {
				continue
			}
			seen[label] = true
			plan, err := entry.plan(termui.New(strings.NewReader(""), &strings.Builder{}, termui.Style{}))
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if len(plan) != 1 {
				t.Fatalf("%s: %d commands, want 1", label, len(plan))
			}
			if got := strings.Join(plan[0], " "); got != wantArgv {
				t.Errorf("%s runs %q, want %q", label, got, wantArgv)
			}
		}
	}
	walk(app.menuEntries())
	for label := range want {
		if !seen[label] {
			t.Errorf("the menu has no %q entry", label)
		}
	}
}

// everyCommand answers "can an operator reach this from the menu?" for every command the help documents:
// the label of the entry that runs it, or the reason there is deliberately none. The usage text is the
// list of commands, so a command added there without a row here fails this suite — which is the rule
// AGENTS.md §7 states: a new or changed option is either in the menu or explained where it is not.
var everyCommand = map[string]struct {
	menu i18n.Key
	why  string
}{
	"init":       {menu: i18n.MenuInstallLabel},
	"doctor":     {menu: i18n.MenuDoctorLabel},
	"net":        {menu: i18n.MenuNetLabel},
	"status":     {menu: i18n.MenuStatusLabel},
	"device":     {menu: i18n.MenuDevicesLabel},
	"renew-cert": {menu: i18n.MenuCertificateLabel},
	"uri":        {menu: i18n.MenuURILabel},
	"options":    {menu: i18n.MenuOptionsLabel},
	"wizard":     {menu: i18n.MenuWizardLabel},
	"renew-site": {menu: i18n.MenuDecoyLabel},
	"enroll":     {menu: i18n.MenuEnrollLabel},
	"sync":       {menu: i18n.MenuSyncLabel, why: "--check is the drift entry"},
	"agent":      {menu: i18n.MenuAgentOnceLabel, why: "the bare command is the unit's loop; --once is the entry"},
	"unblock":    {menu: i18n.MenuUnblockLabel},
	"upgrade":    {menu: i18n.MenuUpgradeLabel, why: "--check is a flag of the entry, not a second line"},
	"migrate":    {menu: i18n.MenuMigrateLabel},
	"warp":       {menu: i18n.MenuWarpLabel},
	"warp-relay": {why: "the loopback relay's own unit runs it; no operator action"},
	"firewall":   {menu: i18n.MenuFirewallLabel, why: "rollback belongs to the rollback timer"},
	"bans":       {menu: i18n.MenuBansLabel},
	"guard":      {menu: i18n.MenuGuardLabel, why: "the entry runs status; run is the unit's loop"},
	"uninstall":  {menu: i18n.MenuUninstallLabel},
	"version":    {why: "the menu header prints it, so the build is always on screen"},
	"menu":       {why: "the menu is the entry point"},
	"help":       {menu: i18n.MenuHelpLabel},
}

// The menu is a front end for the CLI: a command a script can run and an operator cannot reach is either
// a gap or a decision, and this test is where it has to be one of those two.
func TestEveryDocumentedCommandIsInTheMenuOrExplained(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "", false)
	var usage strings.Builder
	app.usage(&usage)

	labels := map[i18n.Key]bool{}
	var walk func(entries []menuEntry)
	walk = func(entries []menuEntry) {
		for _, entry := range entries {
			labels[entry.labelKey] = true
			if entry.children != nil {
				walk(entry.children())
			}
		}
	}
	walk(app.menuEntries())

	documented := 0
	for _, command := range documentedCommands(t, usage.String()) {
		documented++
		answer, ok := everyCommand[command]
		if !ok {
			t.Errorf("`%s` is documented but has no answer here: give it a menu entry, or the reason there is none",
				command)
			continue
		}
		switch {
		case answer.menu != "" && !labels[answer.menu]:
			t.Errorf("`%s` points at a menu entry that does not exist (%s)", command, answer.menu)
		case answer.menu == "" && answer.why == "":
			t.Errorf("`%s` is not in the menu and no reason is given", command)
		}
	}
	if documented == 0 {
		t.Fatal("the usage text documents no commands")
	}
	for command := range everyCommand {
		if !strings.Contains(usage.String(), "  "+command+" ") {
			t.Errorf("`%s` is in the table but no longer documented", command)
		}
	}
}

// documentedCommands reads the command names out of the usage text: one per line, indented, with at least
// two spaces before the description.
func documentedCommands(t *testing.T, usage string) []string {
	t.Helper()
	matches := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*)\s{2,}`).FindAllStringSubmatch(usage, -1)
	if len(matches) == 0 {
		t.Fatal("the usage text lists no commands")
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

// The menu is a tree: a level that does not fit on one screen is a screen nobody reads, and every
// command the flat menu offered has to still be reachable — the move regrouped them, it dropped none.
func TestTheMenuTreeFitsOnOneScreen(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "", false)
	top := app.menuEntries()
	if len(top) != 8 {
		t.Errorf("the top level has %d entries, want 8", len(top))
	}
	leaves := 0
	var walk func(entries []menuEntry)
	walk = func(entries []menuEntry) {
		if len(entries) > 10 {
			t.Errorf("a level has %d entries, want at most 10", len(entries))
		}
		for _, entry := range entries {
			if entry.children != nil {
				walk(entry.children())
				continue
			}
			leaves++
		}
	}
	walk(top)
	// 23 commands, four of which were their own menu and are now groups of 5, 3, 6 and 5 leaves, plus
	// the second cycle `agent --once` that the CLI always had.
	if leaves != 39 {
		t.Errorf("the tree has %d leaves, want 39", leaves)
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

// An empty domain is asked again rather than dropped: the operator keeps the flow, and the value arrives
// on the second try.
func TestInstallAsksAgainForAnEmptyDomain(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "\nnode1.example.com\nme@example.com\n\n", false)
	plan, err := entryNamed(t, app, "install or converge").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "init --domain node1.example.com --email me@example.com" {
		t.Errorf("plan = %q", got)
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

// The invite names a code the operator may not have at hand: an empty answer cancels the entry, and the
// label says so, instead of asking for the code until the session ends.
func TestEnrollCancelsOnAnEmptyInvite(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "\n\n", false)
	plan, err := entryNamed(t, app, "join the mesh").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan != nil {
		t.Errorf("plan = %v, want a cancel", plan)
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
	app, _ := menuApp(t, "laptop\n", false)
	plan, err := childNamed(t, app, "devices", "add").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "device add --label laptop" {
		t.Errorf("plan = %q", got)
	}
}

func TestDevicesRemovePassesTheLabel(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "old-phone\n", false)
	plan, err := childNamed(t, app, "devices", "remove").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "device remove old-phone" {
		t.Errorf("plan = %q", got)
	}
}

// A move already written into the configuration is one answer — the default — because the domain the
// prompt offers is the one in the file.
func TestMigrateDefaultsToTheConfiguredDomain(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "\n", false)
	cfg := config.Default()
	cfg.Domain = "node1.example.com"
	if err := cfg.Save(app.configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	plan, err := entryNamed(t, app, "move to another domain").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "migrate --relocate" {
		t.Errorf("plan = %q, want the configured domain to need no flag", got)
	}
}

func TestMigrateNamesTheNewDomain(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "node2.example.com\n", false)
	plan, err := entryNamed(t, app, "move to another domain").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "migrate --relocate --domain node2.example.com" {
		t.Errorf("plan = %q", got)
	}
}

// With no configuration there is no default domain, so the empty answer is refused and asked again.
func TestMigrateAsksAgainForAnEmptyDomain(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "\nnode2.example.com\n", false)
	plan, err := entryNamed(t, app, "move to another domain").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "migrate --relocate --domain node2.example.com" {
		t.Errorf("plan = %q", got)
	}
}

// A new Cloudflare device stops the old one, so the menu asks first and a "no" runs nothing.
func TestWarpRotateAsksBeforeReplacingTheDevice(t *testing.T) {
	t.Parallel()
	app, _ := menuApp(t, "y\n", false)
	plan, err := childNamed(t, app, "egress (WARP)", "rotate").plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "warp rotate" {
		t.Errorf("plan = %q", got)
	}

	declined, _ := menuApp(t, "n\n", false)
	plan, err = childNamed(t, declined, "egress (WARP)", "rotate").plan(
		termui.New(declined.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan != nil {
		t.Errorf("declining the rotation produced %q", plan)
	}
}

// The host table and the ban list are levels of their own; each answer has to reach its command
// unchanged, and an option that declares a question asks it when chosen.
func TestSecurityEntriesDrawTheirSubMenus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		group  string
		entry  string
		answer string
		want   string
	}{
		{group: "firewall", entry: "on", want: "firewall on"},
		{group: "bans", entry: "list", want: "bans list"},
		{group: "bans", entry: "block", answer: "203.0.113.7\n", want: "bans block 203.0.113.7"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			app, _ := menuApp(t, tc.answer, false)
			plan, err := childNamed(t, app, tc.group, tc.entry).plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if got := strings.Join(plan[0], " "); got != tc.want {
				t.Errorf("plan = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWarpModes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		answers string
		want    string
	}{
		"show":     {"", "warp status"},
		"all":      {"", "warp mode all"},
		"off":      {"", "warp mode off"},
		"selected": {"x\n", "warp mode selected --selected x"},
	}
	for label, tc := range cases {
		app, _ := menuApp(t, tc.answers, false)
		plan, err := childNamed(t, app, "egress (WARP)", label).plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
		if err != nil {
			t.Fatalf("%s: plan: %v", label, err)
		}
		if got := strings.Join(plan[0], " "); got != tc.want {
			t.Errorf("%s: plan = %q, want %q", label, got, tc.want)
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
// childNamed finds one entry of a group, so a test can name "on" without caring that several groups have
// an "on" of their own.
func childNamed(t *testing.T, app *application, group, label string) menuEntry {
	t.Helper()
	parent := entryNamed(t, app, group)
	if parent.children == nil {
		t.Fatalf("%q is not a group", group)
	}
	for _, entry := range parent.children() {
		if entry.labelOf(app.translator()) == label {
			return entry
		}
	}
	t.Fatalf("%q has no %q entry", group, label)
	return menuEntry{}
}

// entryNamed finds an entry anywhere in the tree, so a test names the command it means and not the group
// it happens to live in.
func entryNamed(t *testing.T, app *application, label string) menuEntry {
	t.Helper()
	var find func(entries []menuEntry) (menuEntry, bool)
	find = func(entries []menuEntry) (menuEntry, bool) {
		for _, entry := range entries {
			if entry.labelOf(app.translator()) == label {
				return entry, true
			}
			if entry.children != nil {
				if found, ok := find(entry.children()); ok {
					return found, true
				}
			}
		}
		return menuEntry{}, false
	}
	if entry, ok := find(app.menuEntries()); ok {
		return entry
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

// The menu speaks the configured language where the console can show it, and English where it cannot.
func TestMenuSpeaksTheConfiguredLanguage(t *testing.T) {
	t.Parallel()
	writeConfig := func(t *testing.T, app *application, locale string) {
		t.Helper()
		cfg := config.Default()
		cfg.Domain = "node.example.com"
		cfg.Locale = locale
		cfg.Paths.StateDir = t.TempDir()
		if err := cfg.Save(app.configPath); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	t.Run("russian on a utf-8 console", func(t *testing.T) {
		t.Parallel()
		app, out := menuApp(t, "q\n", true)
		writeConfig(t, app, "ru")
		app.getenv = func(name string) string {
			if name == "LANG" {
				return "ru_RU.UTF-8"
			}
			return ""
		}
		if code := app.run([]string{"menu", "--config", app.configPath}); code != exitOK {
			t.Fatalf("exit = %d", code)
		}
		rendered := out.String()
		if !strings.Contains(rendered, "Termit — узел") {
			t.Errorf("the menu title is not Russian:\n%s", rendered)
		}
		if !strings.Contains(rendered, "Состояние") {
			t.Errorf("the menu entries are not Russian:\n%s", rendered)
		}
	})

	t.Run("english on a c console", func(t *testing.T) {
		t.Parallel()
		app, out := menuApp(t, "q\n", true)
		writeConfig(t, app, "ru")
		app.getenv = func(string) string { return "C" }
		if code := app.run([]string{"menu", "--config", app.configPath}); code != exitOK {
			t.Fatalf("exit = %d", code)
		}
		rendered := out.String()
		if !strings.Contains(rendered, "Termit — node") {
			t.Errorf("the menu did not fall back to English:\n%s", rendered)
		}
		if strings.Contains(rendered, "состояние") {
			t.Errorf("Cyrillic leaked onto a console that cannot show it:\n%s", rendered)
		}
	})
}
