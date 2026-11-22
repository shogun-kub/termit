package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/mn/admin"
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
		// The tests find entries by their English label; the language itself is a menu setting.
		tr: i18n.New(i18n.EN),
	}
	return m, &ran, out
}

func planOf(t *testing.T, m *menu, label, answers string) ([][]string, error) {
	t.Helper()
	for _, entry := range m.entries() {
		if entry.labelOf(m.translator()) != label {
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
		"guard":  "guard status",
		"help":   "help",
	}
	seen := map[string]bool{}
	for _, entry := range m.entries() {
		label := entry.labelOf(m.translator())
		expected, ok := want[label]
		if !ok {
			continue
		}
		seen[label] = true
		plan, err := planOf(t, m, label, "")
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got := strings.Join(plan[0], " "); got != expected {
			t.Errorf("%s runs %q, want %q", label, got, expected)
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
		"reissue":   {"8\nnode1.example.com\ny\n", "uv reissue node1.example.com"},
		"agent key": {"9\nnode1.example.com\n", "uv agentkey node1.example.com"},
		"forget":    {"10\nnode1.example.com\ny\n", "uv forget node1.example.com"},
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

// A node is the value the whole sub-menu acts on. The picker lists the mesh when the server answers;
// without one, the question takes a typed domain, and an empty answer cancels the action — the operator
// changed their mind, and asking again would not let them out.
func TestNodeMenuCancelsOnAnEmptyNode(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	plan, err := planOf(t, m, "nodes", "2\n\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan != nil {
		t.Errorf("plan = %v, want a cancel", plan)
	}
	plan, err = planOf(t, m, "nodes", "2\nnode1.example.com\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "uv show node1.example.com" {
		t.Errorf("plan = %q", got)
	}
}

// A reissued key and a forgotten record both break something, so a "no" has to run nothing at all.
func TestReissueAndForgetAskFirst(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"reissue": "8\nnode1.example.com\nn\n",
		"forget":  "10\nnode1.example.com\nn\n",
	}
	for name, answers := range cases {
		m, _, _ := newMenu("")
		plan, err := planOf(t, m, "nodes", answers)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if plan != nil {
			t.Errorf("%s: declining produced %q", name, plan)
		}
	}
}

// The archive is the one thing a restore cannot guess, and --force is the difference between a restore
// and a refusal, so both are part of the command the menu builds.
func TestRestoreNamesTheArchiveAndForces(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	plan, err := planOf(t, m, "restore from a snapshot", "backup.tar.gz\ny\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "restore --from backup.tar.gz --force" {
		t.Errorf("plan = %q", got)
	}
}

func TestRestoreAsksAgainForAnEmptyArchive(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	plan, err := planOf(t, m, "restore from a snapshot", "\nbackup.tar.gz\ny\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "restore --from backup.tar.gz --force" {
		t.Errorf("plan = %q", got)
	}
}

// The hardening commands read the host, not the API: their flag sets never declare --token or --socket,
// so the menu must not hand them the admin flags it carries. It marks them local instead.
func TestHardeningEntriesTakeNoAdminFlags(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("")
	want := map[string]bool{
		"restore from a snapshot": true,
		"firewall":                true,
		"bans":                    true,
		"guard":                   true,
		"status":                  false,
		"nodes":                   false,
	}
	seen := map[string]bool{}
	for _, entry := range m.entries() {
		label := entry.labelOf(m.translator())
		expected, ok := want[label]
		if !ok {
			continue
		}
		seen[label] = true
		if entry.local != expected {
			t.Errorf("%s: local = %v, want %v", label, entry.local, expected)
		}
	}
	for label := range want {
		if !seen[label] {
			t.Errorf("the menu has no %q entry", label)
		}
	}
}

// Both hardening sub-menus are drawn from the shared table, so this only proves the server wires them to
// the same commands the node does.
func TestServerMenuDrawsTheHardeningSubMenus(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		entry   string
		answers string
		want    string
	}{
		"firewall on":      {"firewall", "2\n", "firewall on"},
		"bans list":        {"bans", "1\n", "bans list"},
		"bans unblock one": {"bans", "5\n203.0.113.7\n", "bans unblock 203.0.113.7"},
	}
	for name, tc := range cases {
		m, _, _ := newMenu("")
		plan, err := planOf(t, m, tc.entry, tc.answers)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.Join(plan[0], " "); got != tc.want {
			t.Errorf("%s: runs %q, want %q", name, got, tc.want)
		}
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
	if got := strings.Join(plan[0], " "); got != "check now --all" {
		t.Errorf("runs %q, want check now --all", got)
	}

	one, _, _ := newMenu("")
	plan, err = planOf(t, one, "check now", "n\nnode1.example.com\n")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := strings.Join(plan[0], " "); got != "check now node1.example.com" {
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
		// Pinned like newMenu's: a bare menu takes its language from the console, and CI's console is
		// UTF-8, so without this the assertion below reads Russian output on Linux and English here.
		tr: i18n.New(i18n.EN),
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

// The menu runs the command line a script would run, so the credential the operator gave the menu has to
// travel with every one of them: `termit-mn menu --token-file …` used to warn that no token was
// configured while holding one, and every admin action then refused to run.
func TestMenuFlagsReachTheCommands(t *testing.T) {
	t.Parallel()
	flags, _, _, token, tokenFile := inviteFlags("termit-mn menu", io.Discard)
	if err := flags.Parse([]string{"--token-file", "/etc/termit/mn/admin.token", "--socket", "/run/x.sock"}); err != nil {
		t.Fatalf("%v", err)
	}
	// Visit walks the flags in name order, so the base is compared as a set of pairs.
	base := setFlagArgs(flags)
	got := map[string]string{}
	for i := 0; i+1 < len(base); i += 2 {
		got[base[i]] = base[i+1]
	}
	want := map[string]string{"--token-file": "/etc/termit/mn/admin.token", "--socket": "/run/x.sock"}
	if len(got) != len(want) || got["--token-file"] != want["--token-file"] || got["--socket"] != want["--socket"] {
		t.Fatalf("base = %v, want %v (flags: %v, %v)", base, want, *token, *tokenFile)
	}

	joined := strings.Join(withFlags([]string{"uv", "list"}, base), " ")
	if !strings.HasPrefix(joined, "uv list ") {
		t.Errorf("withFlags = %q, want the command words first", joined)
	}
	for _, fragment := range []string{"--token-file /etc/termit/mn/admin.token", "--socket /run/x.sock"} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("withFlags = %q, want it to contain %q", joined, fragment)
		}
	}
	// A command that takes no admin flags gets nothing, and an unset flag stays unset so each command
	// keeps its own environment lookup.
	if got := withFlags([]string{"backup"}, nil); len(got) != 1 {
		t.Errorf("withFlags without a base changed the command: %v", got)
	}
}

// The header has to say what the session actually runs on, including the two ways it can be wrong.
func TestAdminTokenSource(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/token"
	if err := os.WriteFile(file, []byte("from-the-file\n"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}

	cases := []struct {
		name        string
		token       string
		file        string
		envToken    string
		envFile     string
		wantSource  string
		wantProblem string
	}{
		{name: "flag wins", token: "typed", envToken: "exported", wantSource: "--token"},
		{name: "file flag", file: file, wantSource: "--token-file " + file},
		{name: "environment", envToken: "exported", wantSource: adminTokenEnv},
		{name: "environment file", envFile: file, wantSource: adminTokenFileEnv + "=" + file},
		{name: "nothing", wantSource: ""},
		{name: "both flags", token: "typed", file: file, wantProblem: "not both"},
		{name: "unreadable file", file: dir + "/missing", wantProblem: "does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(adminTokenEnv, tc.envToken)
			t.Setenv(adminTokenFileEnv, tc.envFile)
			value, source, err := resolveAdminTokenSource(tc.token, tc.file)
			if tc.wantProblem != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantProblem) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantProblem)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v", err)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			if tc.wantSource != "" && value == "" {
				t.Errorf("source %q came with an empty token", source)
			}
			if tc.wantSource == "" && value != "" {
				t.Errorf("a token appeared from nowhere: %q", value)
			}
		})
	}
}

// A menu that cannot authenticate refuses at startup with a usage error: the operator asked for two
// credentials at once, or named a file that is not there, and clicking an action to find that out is
// worse than being told.
func TestMenuRefusesAnUnusableCredential(t *testing.T) {
	t.Parallel()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { _ = devNull.Close() })
	quiet, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("%v", err)
	}
	t.Cleanup(func() { _ = quiet.Close() })

	cases := map[string][]string{
		"both flags":     {"--token", "typed", "--token-file", "/etc/termit/mn/admin.token"},
		"missing file":   {"--token-file", t.TempDir() + "/absent"},
		"stray argument": {"nonsense"},
	}
	for name, args := range cases {
		code := runMenu(devNull, quiet, strings.NewReader(""), termui.Style{}, args)
		if code != exitUsage {
			t.Errorf("%s: exit = %d, want %d", name, code, exitUsage)
		}
	}
}

// The server menu speaks the configured language where the console can show it.
func TestServerMenuSpeaksTheConfiguredLanguage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		lang i18n.Lang
		want string
	}{
		{name: "russian", lang: i18n.RU, want: "Termit — сервер"},
		{name: "english", lang: i18n.EN, want: "Termit — server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _, out := newMenu("q\n")
			m.tr = i18n.New(tc.lang)
			if code := m.loop(); code != exitOK {
				t.Fatalf("exit = %d", code)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("the menu does not say %q:\n%s", tc.want, out.String())
			}
		})
	}
}

// pickNode is the answer to "which node?": a list of domains, a number, a typed domain, a unique part of
// one, or nothing at all.
func TestPickNodeChoosesANumber(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("2\n")
	m.nodes = func(context.Context) ([]admin.NodeView, error) {
		return []admin.NodeView{
			{ID: "a", Domain: "node-a.example.com", State: "active"},
			{ID: "b", Domain: "then5.bookinet.ru", State: "active"},
		}, nil
	}
	got, err := m.pickNode(m.ui)
	if err != nil {
		t.Fatalf("pickNode: %v", err)
	}
	if got != "then5.bookinet.ru" {
		t.Errorf("picked %q, want then5.bookinet.ru", got)
	}
}

func TestPickNodeAcceptsAPartOfADomain(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("then\n")
	m.nodes = func(context.Context) ([]admin.NodeView, error) {
		return []admin.NodeView{
			{ID: "a", Domain: "node-a.example.com"},
			{ID: "b", Domain: "then5.bookinet.ru"},
		}, nil
	}
	got, err := m.pickNode(m.ui)
	if err != nil {
		t.Fatalf("pickNode: %v", err)
	}
	if got != "then5.bookinet.ru" {
		t.Errorf("picked %q, want then5.bookinet.ru", got)
	}
}

func TestPickNodeCancelsOnZero(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("0\n")
	m.nodes = func(context.Context) ([]admin.NodeView, error) {
		return []admin.NodeView{{ID: "a", Domain: "node-a.example.com"}}, nil
	}
	got, err := m.pickNode(m.ui)
	if err != nil {
		t.Fatalf("pickNode: %v", err)
	}
	if got != "" {
		t.Errorf("picked %q, want a cancel", got)
	}
}

// The list needs the server; the action does not. When the socket is unreachable the picker says so and
// takes what the operator types, still cancelling on an empty answer.
func TestPickNodeFallsBackToTyping(t *testing.T) {
	t.Parallel()
	m, _, out := newMenu("node-a.example.com\n")
	m.nodes = func(context.Context) ([]admin.NodeView, error) { return nil, errors.New("no socket") }
	got, err := m.pickNode(m.ui)
	if err != nil {
		t.Fatalf("pickNode: %v", err)
	}
	if got != "node-a.example.com" {
		t.Errorf("picked %q, want the typed domain", got)
	}
	if !strings.Contains(out.String(), "did not answer") {
		t.Errorf("the picker did not say why the list is missing:\n%s", out.String())
	}

	m.ui = termui.New(strings.NewReader("\n"), &strings.Builder{}, termui.Style{})
	if got, _ := m.pickNode(m.ui); got != "" {
		t.Errorf("an empty answer picked %q, want a cancel", got)
	}
}

func TestPickNodeAsksAgainOnAMiss(t *testing.T) {
	t.Parallel()
	m, _, _ := newMenu("zzz\n1\n")
	m.nodes = func(context.Context) ([]admin.NodeView, error) {
		return []admin.NodeView{{ID: "a", Domain: "node-a.example.com"}}, nil
	}
	got, err := m.pickNode(m.ui)
	if err != nil {
		t.Fatalf("pickNode: %v", err)
	}
	if got != "node-a.example.com" {
		t.Errorf("picked %q, want node-a.example.com", got)
	}
}

func TestMatchNodesNeverGuesses(t *testing.T) {
	t.Parallel()
	nodes := []admin.NodeView{
		{ID: "AbC", Domain: "node-a.example.com"},
		{ID: "b", Domain: "node-b.example.com"},
		{ID: "c", Domain: "then5.bookinet.ru"},
	}
	cases := map[string]int{"abc": 1, "Node-A.Example.com": 1, "then": 1, "node": 2, "elsewhere": 0, "": 0}
	for typed, want := range cases {
		if got := len(matchNodes(nodes, typed)); got != want {
			t.Errorf("%q matched %d nodes, want %d", typed, got, want)
		}
	}
}
