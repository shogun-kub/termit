package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
)

// menuEntry is one line of the interactive menu. The menu owns no behaviour: it asks the questions a
// flag would answer, then runs the same command line a script would. That is the whole point — a
// second implementation of "install" would drift from the first one.
type menuEntry struct {
	label string
	hint  string
	// plan builds the commands to run. A nil plan with a nil error means the operator cancelled; an
	// error means the answer was unusable and nothing runs.
	plan func(ui *termui.UI) ([][]string, error)
}

// runMenu is the interactive front end: the binary with no arguments on a terminal, or `uv menu`.
func (a *application) runMenu() int {
	ui := termui.New(a.stdin(), a.stdout, a.menuStyle())
	entries := a.menuEntries()

	for {
		ui.Clear()
		a.menuHeader(ui)
		items := make([]termui.Item, len(entries))
		for i, entry := range entries {
			items[i] = termui.Item{Label: entry.label, Hint: entry.hint}
		}
		choice, err := ui.Menu(items)
		if errors.Is(err, io.EOF) {
			// stdin closed: the session is over, and that is not an error.
			return exitOK
		}
		if err != nil {
			ui.Error(fmt.Sprintf("menu: %v", err))
			return exitFailure
		}
		if choice == "" {
			return exitOK
		}
		index, err := termui.Index(choice)
		if err != nil {
			ui.Error(err.Error())
			continue
		}
		if index >= len(entries) {
			ui.Error(fmt.Sprintf("%q is past the end of the menu", choice))
			continue
		}

		plan, err := entries[index].plan(ui)
		switch {
		case err != nil:
			ui.Error(err.Error())
		case plan == nil:
			ui.Say("cancelled")
		default:
			if code := a.runPlan(ui, plan); code != exitOK {
				// A failed command has explained itself; keep the summary on screen long enough to read.
				_ = ui.Pause()
				continue
			}
		}
		if err := ui.Pause(); err != nil {
			return exitOK
		}
	}
}

// runPlan runs the commands an entry produced, one at a time, and stops at the first failure.
func (a *application) runPlan(ui *termui.UI, plan [][]string) int {
	for _, argv := range plan {
		ui.Blank()
		ui.Say("$ uv " + strings.Join(argv, " "))
		ui.Blank()
		if code := a.run(argv); code != exitOK {
			ui.Blank()
			ui.Error(fmt.Sprintf("that step exited with status %d", code))
			return code
		}
	}
	return exitOK
}

// menuHeader draws what this node is, without pretending to be a status report: the status command
// does that, and the menu offers it as an action.
func (a *application) menuHeader(ui *termui.UI) {
	ui.Title("Termit — node")
	ui.Hint(version.String("termit-uv"))
	ui.Blank()

	cfg, err := config.Load(a.menuConfigPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ui.State("warn", "not installed yet: choose “install or converge”")
		ui.Blank()
		return
	case err != nil:
		ui.State("fail", "configuration: "+err.Error())
		ui.Blank()
		return
	}
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	switch {
	case errors.Is(err, state.ErrNotInstalled):
		ui.State("warn", "configured, but no node state: run “install or converge”")
	case err != nil:
		ui.State("fail", "state: "+err.Error())
	default:
		ui.KV("domain", cfg.Domain)
		ui.KV("identity", state.Fingerprint(node.Identity.SelfUUID))
		ui.KV("peer path", state.Fingerprint(node.Identity.XHTTPPath))
		ui.State("ok", "installed")
	}
	ui.Blank()
}

// menuEntries is the menu in order. Every entry answers with argv, never with behaviour.
func (a *application) menuEntries() []menuEntry {
	return []menuEntry{
		{
			label: "status",
			hint:  "identity, certificate, peers, sync, WARP",
			plan:  fixed("status"),
		},
		{
			label: "doctor",
			hint:  "check the host and the node, change nothing",
			plan:  fixed("doctor"),
		},
		{
			label: "install or converge",
			hint:  "first run asks for the domain; later runs only re-check",
			plan:  a.planInstall,
		},
		{
			label: "join the mesh",
			hint:  "enroll with an invite code from the server operator",
			plan:  a.planEnroll,
		},
		{
			label: "sync now",
			hint:  "fetch the peer set immediately instead of waiting a day",
			plan:  fixed("sync"),
		},
		{
			label: "check drift",
			hint:  "exit 4 tells you the core differs from the server",
			plan:  fixed("sync", "--check"),
		},
		{
			label: "devices",
			hint:  "phones and laptops that use this node only",
			plan:  a.planDevices,
		},
		{
			label: "print vless URI",
			hint:  "the URI of this node's own device",
			plan:  fixed("uri"),
		},
		{
			label: "egress (WARP)",
			hint:  "where proxied traffic leaves",
			plan:  a.planWarp,
		},
		{
			label: "certificate",
			hint:  "renew the TLS certificate now",
			plan:  a.planCertificate,
		},
		{
			label: "decoy site",
			hint:  "regenerate the site the node serves to strangers",
			plan:  a.planDecoy,
		},
		{
			label: "uninstall",
			hint:  "remove what Termit created, optionally with its secrets",
			plan:  a.planUninstall,
		},
		{
			label: "help",
			hint:  "every command and flag",
			plan:  fixed("help"),
		},
	}
}

// fixed is an entry that runs one command line with no questions.
func fixed(argv ...string) func(*termui.UI) ([][]string, error) {
	return func(*termui.UI) ([][]string, error) { return [][]string{argv}, nil }
}

// planInstall converges an installed node, or installs one, asking only for what the flags would
// require on a first run.
func (a *application) planInstall(ui *termui.UI) ([][]string, error) {
	if _, err := config.Load(a.menuConfigPath()); err == nil {
		return [][]string{{"init"}}, nil
	}
	ui.Say("This machine has no node configuration yet.")
	domain, err := ui.Prompt("Domain (a DNS name that points at this machine)", "")
	if err != nil {
		return nil, err
	}
	if domain == "" {
		return nil, errors.New("a domain is required")
	}
	email, err := ui.Prompt("Email for the certificate authority", "")
	if err != nil {
		return nil, err
	}
	warp, err := ui.Confirm("Send proxied traffic through Cloudflare WARP?", true)
	if err != nil {
		return nil, err
	}

	argv := []string{"init", "--domain", domain}
	if email != "" {
		argv = append(argv, "--email", email)
	}
	if !warp {
		argv = append(argv, "--no-warp")
	}
	return [][]string{argv}, nil
}

// planEnroll asks for the server and the invite code. The MN URL defaults to the configured one, so a
// converged node needs one answer.
func (a *application) planEnroll(ui *termui.UI) ([][]string, error) {
	mn := ""
	if cfg, err := config.Load(a.menuConfigPath()); err == nil {
		mn = cfg.MN.URL
	}
	if mn == "" {
		answer, err := ui.Prompt("Management server URL", "https://mn.example.com")
		if err != nil {
			return nil, err
		}
		mn = answer
	}
	invite, err := ui.Prompt("Invite code from the server operator", "")
	if err != nil {
		return nil, err
	}
	if invite == "" {
		return nil, errors.New("an invite code is required")
	}
	return [][]string{{"enroll", "--invite", invite, "--mn", mn}}, nil
}

// planDevices handles the local devices on this node.
func (a *application) planDevices(ui *termui.UI) ([][]string, error) {
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: "list", Hint: "labels and UUID prefixes"},
		{Label: "add", Hint: "create a device and print its URI"},
		{Label: "remove", Hint: "delete one by label"},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	switch choice {
	case "1":
		return [][]string{{"device", "list"}}, nil
	case "2":
		label, err := ui.Prompt("Label for the new device", "phone")
		if err != nil {
			return nil, err
		}
		if label == "" {
			return nil, errors.New("a label is required")
		}
		return [][]string{{"device", "add", "--label", label}}, nil
	default:
		label, err := ui.Prompt("Label of the device to remove", "")
		if err != nil {
			return nil, err
		}
		if label == "" {
			return nil, errors.New("a label is required")
		}
		return [][]string{{"device", "remove", label}}, nil
	}
}

// planWarp changes where proxied traffic leaves.
func (a *application) planWarp(ui *termui.UI) ([][]string, error) {
	ui.Blank()
	choice, err := ui.Menu([]termui.Item{
		{Label: "show", Hint: "mode, what warp-cli says, and what Cloudflare sees"},
		{Label: "all", Hint: "every proxied connection goes through WARP (default)"},
		{Label: "off", Hint: "exit from this node's own address"},
		{Label: "selected", Hint: "only the domains you list"},
	})
	if err != nil || choice == "" {
		return nil, err
	}
	switch choice {
	case "1":
		return [][]string{{"warp", "status"}}, nil
	case "2":
		return [][]string{{"warp", "mode", "all"}}, nil
	case "3":
		return [][]string{{"warp", "mode", "off"}}, nil
	default:
		domains, err := ui.Prompt("Domains that use WARP, comma separated", "")
		if err != nil {
			return nil, err
		}
		if domains == "" {
			return nil, errors.New("at least one domain is required")
		}
		return [][]string{{"warp", "mode", "selected", "--selected", domains}}, nil
	}
}

// planCertificate renews the certificate, forcing a reissue when asked.
func (a *application) planCertificate(ui *termui.UI) ([][]string, error) {
	force, err := ui.Confirm("Reissue even if the current certificate is not due?", false)
	if err != nil {
		return nil, err
	}
	if force {
		return [][]string{{"renew-cert", "--force"}}, nil
	}
	return [][]string{{"renew-cert"}}, nil
}

// planDecoy regenerates the decoy site, optionally with another archetype.
func (a *application) planDecoy(ui *termui.UI) ([][]string, error) {
	template, err := ui.Prompt("Archetype: product or portal (empty keeps the current one)", "")
	if err != nil {
		return nil, err
	}
	switch template {
	case "":
		return [][]string{{"renew-site"}}, nil
	case "product", "portal":
		return [][]string{{"renew-site", "--template", template}}, nil
	default:
		return nil, fmt.Errorf("%q is neither product nor portal", template)
	}
}

// planUninstall is the one destructive path, so it asks twice and names what --purge takes.
func (a *application) planUninstall(ui *termui.UI) ([][]string, error) {
	purge, err := ui.Confirm("Also delete configuration, state, certificates and the service user?", false)
	if err != nil {
		return nil, err
	}
	what := "the units, the core and the nginx vhost"
	if purge {
		what = "the units, the core, the nginx vhost, and every secret and certificate"
	}
	sure, err := ui.Confirm("Really remove "+what+"?", false)
	if err != nil {
		return nil, err
	}
	if !sure {
		return nil, nil
	}
	if purge {
		return [][]string{{"uninstall", "--purge"}}, nil
	}
	return [][]string{{"uninstall"}}, nil
}

// menuConfigPath is the configuration the menu inspects: the default one in production, whatever a
// test pinned otherwise.
func (a *application) menuConfigPath() string {
	if a.configPath != "" {
		return a.configPath
	}
	return configPathDefault()
}

// stdin is the input stream; os.Stdin unless a test replaced it.
func (a *application) stdin() io.Reader {
	if a.in != nil {
		return a.in
	}
	return os.Stdin
}

// menuStyle is colour on a terminal, plain text otherwise.
func (a *application) menuStyle() termui.Style {
	if a.interactiveFn != nil {
		if !a.interactiveFn() {
			return termui.Style{}
		}
		return termui.Colour()
	}
	return termui.PlainColour(os.Stdout)
}

// interactive reports whether the bare binary should open the menu: both streams are terminals.
func (a *application) interactive() bool {
	if a.interactiveFn != nil {
		return a.interactiveFn()
	}
	return termui.Interactive(os.Stdin, os.Stdout)
}
