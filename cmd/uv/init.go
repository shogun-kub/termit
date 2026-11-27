package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// appliedLacksQUIC reports whether a table is in force and does not carry the QUIC port. The hint after
// `init` is only true then: a node with nothing applied has nothing to re-render, and a table that
// already carries the port needs no second apply.
func appliedLacksQUIC(cfg *config.Config) bool {
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil || applied == nil || !applied.RemovedAt.IsZero() {
		return false
	}
	return !slices.Contains(applied.UDPPorts, proto.HysteriaPort)
}

func (a *application) runInit(args []string) int {
	flags := a.newFlagSet("init", `Installs this node: host checks, packages, the pinned proxy core and the node
identity. Re-running converges: steps whose effect is already present do nothing, and no
credential is ever regenerated.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	domain := flags.String("domain", "", "node domain name; required on the first run")
	mnURL := flags.String("mn", "", "management server URL, the server's API prefix included")
	email := flags.String("email", "", "ACME account contact address")
	locale := flags.String("locale", "", "language for the menu and the page of client links: ru or en")
	happFragment := flags.String("happ-fragment", "",
		"fragmentation this node writes into its own links (length,interval,packets[,maxSplit]; empty: off)")
	acmeCA := flags.String("acme-ca", "", "ACME directory URL")
	acmeInsecure := flags.Bool("acme-skip-tls-verify", false, "lab only: do not verify the ACME server certificate")
	template := flags.String("decoy-template", "", "decoy site archetype")
	warpMode := flags.String("warp", "", "WARP egress: all (default), selected or off")
	flags.Bool("no-warp", false, "opt out of WARP and exit directly")
	warpSelected := flags.String("warp-selected", "", "domains that use WARP in selected mode, comma separated")
	warpTransport := flags.String("warp-transport", "",
		"how WARP carries traffic: wireguard (inside the core, TCP and UDP), usque (MASQUE sidecar, TCP and UDP) or proxy (warp-cli's SOCKS, TCP only)")
	warpFamily := flags.String("warp-family", "",
		"address family the WARP tunnel carries: ipv4 (default) or ipv6; destinations of the other family are refused")
	xrayVersion := flags.String("xray-version", "", "Xray-core release to install")
	xrayBase := flags.String("xray-download-base", "", "release directory: an http(s) URL, a file URL or a local path")
	dryRun := flags.Bool("dry-run", false, "report what would change without touching the host")
	only := flags.String("only", "", "run only these steps, comma separated")
	skip := flags.String("skip", "", "skip these steps, comma separated")
	harden := flags.Bool("harden", false, "configure the host firewall and the ban engine for this node (it does not apply the table)")
	flags.Bool("hysteria", false,
		"serve the QUIC transport too: Hysteria2 on UDP/443, plus that port in the firewall's UDP list")
	flags.Bool("no-hysteria", false, "stop serving the QUIC transport and remove it from this node")
	relocate := flags.Bool("relocate", false, "install a rebuilt node: reuse the agent key, mint a new identity, do not enroll")
	agentKeyFile := flags.String("agent-key-file", "",
		"0600 file holding the agent key of the node being rebuilt (required with --relocate)")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv init: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *relocate != (*agentKeyFile != "") {
		fmt.Fprintln(a.stderr, "termit-uv init: --relocate and --agent-key-file are used together")
		return exitUsage
	}

	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })

	cfg, err := config.Load(*configFlag)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = config.Default()
	case err != nil:
		fmt.Fprintf(a.stderr, "termit-uv init: %v\n", err)
		return exitFailure
	}

	// Flags win over the file, and a flag that was not given leaves the file's value alone.
	if set["domain"] {
		cfg.Domain = *domain
	}
	if set["mn"] {
		cfg.MN.URL = strings.TrimRight(strings.TrimSpace(*mnURL), "/")
	}
	if set["email"] {
		cfg.Email = *email
	}
	if set["happ-fragment"] {
		value := strings.TrimSpace(*happFragment)
		if value != "" && !proto.ValidHappFragment(value) {
			fmt.Fprintf(a.stderr, "termit-uv init: --happ-fragment %q must look like length,interval,packets[,maxSplit]\\n", value)
			return exitUsage
		}
		cfg.HappFragment = value
	}
	if set["locale"] {
		lang, ok := i18n.Parse(*locale)
		if !ok {
			fmt.Fprintf(a.stderr, "termit-uv init: --locale %q is not one this build speaks (ru, en)\n", *locale)
			return exitUsage
		}
		cfg.Locale = lang.String()
	}
	if set["acme-ca"] {
		cfg.ACME.Directory = *acmeCA
	}
	if set["acme-skip-tls-verify"] {
		cfg.ACME.SkipTLSVerify = *acmeInsecure
	}
	if set["decoy-template"] {
		cfg.Decoy.Template = *template
	}
	switch {
	case set["warp"] && set["no-warp"]:
		fmt.Fprintln(a.stderr, "termit-uv init: --warp and --no-warp contradict each other")
		return exitUsage
	case set["no-warp"]:
		cfg.Warp.Mode = config.WarpOff
	case set["warp"]:
		cfg.Warp.Mode = strings.ToLower(strings.TrimSpace(*warpMode))
	}
	if set["warp-selected"] {
		cfg.Warp.Selected = nil
		for _, domain := range strings.Split(*warpSelected, ",") {
			if domain = strings.TrimSpace(domain); domain != "" {
				cfg.Warp.Selected = append(cfg.Warp.Selected, domain)
			}
		}
	}
	if set["warp-transport"] {
		cfg.Warp.Transport = strings.ToLower(strings.TrimSpace(*warpTransport))
	}
	if set["warp-family"] {
		cfg.Warp.Family = strings.ToLower(strings.TrimSpace(*warpFamily))
	}
	if set["xray-version"] {
		cfg.Xray.Version = *xrayVersion
	}
	if set["xray-download-base"] {
		cfg.Xray.DownloadBase = *xrayBase
	}
	if set["harden"] {
		// Only the configuration: the table is applied by `firewall on` and nothing else, because an
		// install that silently turned on a default-deny table would be a lockout with no operator
		// present to confirm it.
		cfg.Security.Firewall.Enabled = *harden
		cfg.Security.Guard.Enabled = *harden
	}
	switch {
	case set["hysteria"] && set["no-hysteria"]:
		fmt.Fprintln(a.stderr, "termit-uv init: --hysteria and --no-hysteria contradict each other")
		return exitUsage
	case set["no-hysteria"]:
		enableTransport(cfg, false)
	case set["hysteria"]:
		enableTransport(cfg, true)
	}

	if cfg.Domain == "" {
		fmt.Fprintln(a.stderr, "termit-uv init: --domain is required on the first run")
		return exitUsage
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv init: %v\n", err)
		return exitUsage
	}

	var relocateKey string
	if *relocate {
		// Refuse a live node: --relocate mints an identity, and doing that under a working node would
		// rewrite its core config and orphan the credentials the server still knows.
		node, err := state.LoadInstalled(cfg.Paths.StatePath())
		switch {
		case errors.Is(err, state.ErrNotInstalled):
			// A rebuilt box: exactly the case this mode exists for.
		case err != nil:
			fmt.Fprintf(a.stderr, "termit-uv init: %v\n", err)
			return exitFailure
		case node.Identity.SelfUUID != "":
			fmt.Fprintln(a.stderr,
				"termit-uv init: this node already has an identity; --relocate is for a rebuilt box whose state is gone")
			return exitFailure
		}
		relocateKey, err = readAgentKeyFile(*agentKeyFile)
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv init: %v\n", err)
			return exitFailure
		}
	}

	ctx, cancel := a.context()
	defer cancel()

	preflight := install.RunPreflight(ctx, install.PreflightOptions{
		Config: cfg,
		Exec:   a.exec,
		HTTP:   a.http,
		Arch:   runtime.GOARCH,
		Now:    a.now,
	})
	checks := preflight.Checks
	a.printChecks(checks)
	fmt.Fprintf(a.stdout, "\n%s\n\n", summarizeChecks(checks))
	for _, check := range checks {
		if check.Status == install.StatusFail {
			fmt.Fprintf(a.stderr, "termit-uv init: %s: %s\n", check.Name, check.Detail)
		}
	}
	if !preflight.OK() {
		fmt.Fprintln(a.stderr, "termit-uv init: host checks failed; nothing was changed")
		return exitFailure
	}

	if !*dryRun {
		if err := cfg.Save(*configFlag); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv init: %v\n", err)
			return exitFailure
		}
	}
	fmt.Fprintf(a.stdout, "config: %s\n\n", *configFlag)

	installer, err := install.New(a.installOptions(cfg, *configFlag))
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv init: %v\n", err)
		return exitFailure
	}
	report, err := installer.Run(ctx, install.RunOptions{
		Command:   "init",
		Only:      splitList(*only),
		Skip:      splitList(*skip),
		DryRun:    *dryRun,
		KeepGoing: false,
	})
	if report != nil {
		a.printSteps(report)
		fmt.Fprintf(a.stdout, "\n%s\n", summarizeReport(report))
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "\nuv init: %v\n", err)
		if report != nil && report.Run != nil {
			fmt.Fprintf(a.stderr, "journal: %s\n", cfg.Paths.JournalPath())
		}
		return exitFailure
	}

	node := installer.State()
	if node.Installed() {
		fmt.Fprintf(a.stdout, "\nidentity: %s  (sha256 prefix; the value itself is in %s)\n",
			state.Fingerprint(node.Identity.SelfUUID), cfg.Paths.StatePath())
	}
	if *relocate && !*dryRun {
		// The key goes in last, after the install steps have created the state file: an interrupted
		// init leaves either no credentials or a complete node, never a half-trusted one.
		if err := a.storeRebuiltCredentials(cfg, relocateKey); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv init: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, "\nrelocated: a new identity is installed; the server still holds the old one.")
		fmt.Fprintln(a.stdout, "next: termit-uv unblock                              (the node keeps its domain)")
		fmt.Fprintln(a.stdout, "      termit-uv migrate --relocate                   (the node moved to a new domain)")
	}
	if cfg.Hysteria != nil && cfg.Security.Firewall.Enabled && !*dryRun && appliedLacksQUIC(cfg) {
		fmt.Fprintf(a.stdout, "\nQUIC transport: UDP/%d is in the configuration; the table already in force was rendered without it.\n", proto.HysteriaPort)
		fmt.Fprintln(a.stdout, "next: termit-uv firewall on        (it removes itself in five minutes unless you confirm)")
	}
	if set["harden"] && cfg.Security.Firewall.Enabled && !*dryRun {
		fmt.Fprintln(a.stdout, "\nhardening: the configuration and the ban engine are in place; the table is not applied.")
		fmt.Fprintln(a.stdout, "next: termit-uv firewall on        (it removes itself in five minutes unless you confirm)")
	}
	if (set["hysteria"] || set["no-hysteria"]) && !*dryRun {
		// What this node serves travels to the server in a sync request and in nothing else. Without this,
		// the mesh goes on handing out the old set — a second link that no longer exists, or none for a
		// transport that was just turned on — until the node's next cycle, which can be a day away.
		if st := installer.State(); st.Credentials != nil && st.Credentials.AgentKey != "" {
			fmt.Fprintln(a.stdout, "\nwhat this node serves travels with a sync, so it runs now:")
			if code := a.runSync([]string{"--config", *configFlag}); code != exitOK {
				fmt.Fprintln(a.stderr, "termit-uv init: the sync failed; run `termit-uv sync` yourself, or the mesh keeps the old set")
			}
		} else {
			fmt.Fprintln(a.stdout, "\nnot enrolled: the server learns the transport at the first sync after enrollment")
		}
	}
	if len(pendingWork) > 0 {
		fmt.Fprintf(a.stdout, "not yet installed by this build: %s\n", joinWords(pendingWork))
	}
	if !*dryRun {
		// The decoy step rewrites its tree, so the owner's page has to be put back. It is only put
		// back, never created here: a node whose owner never asked for a page stays without one.
		if err := a.refreshOptions(cfg); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv init: the client options page could not be refreshed: %v\n", err)
		}
	}
	return exitOK
}

// storeRebuiltCredentials puts the saved agent key back into a fresh state, so the box can ask the
// server to re-admit it. The node keeps its id and every pair credential the server derives from it;
// only its own identity, path and certificate are new.
func (a *application) storeRebuiltCredentials(cfg *config.Config, key string) error {
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		return fmt.Errorf("the install finished but its state is unreadable: %w", err)
	}
	node.Credentials = &state.Credentials{AgentKey: key, EnrolledAt: a.now().UTC()}
	node.Touch()
	if err := node.Save(cfg.Paths.StatePath()); err != nil {
		return fmt.Errorf("store the agent key: %w", err)
	}
	return nil
}

// readAgentKeyFile reads the agent key a rebuilt node presents. The permission check is part of the
// contract: a key file that group or others can read leaks through a backup or a misconfigured home
// directory, and the operator is told now rather than after an incident.
func readAgentKeyFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read the agent key: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("the agent key file %s is %04o; it must not be readable by group or others", path, perm)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the operator named the file.
	if err != nil {
		return "", fmt.Errorf("read the agent key: %w", err)
	}
	return agentKeyFromBytes(data)
}

func agentKeyFromBytes(data []byte) (string, error) {
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", errors.New("the agent key file is empty")
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return "", errors.New("the agent key file must hold exactly one key")
	}
	return key, nil
}

// enableTransport turns the QUIC transport on or off in the configuration. Turning it on publishes the
// port it listens on: that is the part an operator forgets, and an unpublished UDP port is counted as
// scanning and then banned — `cfg.Validate` refuses the combination, so the switch satisfies the rule it
// would otherwise fail on. Turning it off leaves the port alone: it is the operator's line, and a node
// that stopped serving the transport may still publish it for something else.
func enableTransport(cfg *config.Config, on bool) {
	if !on {
		cfg.Hysteria = nil
		return
	}
	if cfg.Hysteria == nil {
		cfg.Hysteria = &config.Hysteria{}
	}
	cfg.Security.Firewall.UDPPorts = withPort(cfg.Security.Firewall.UDPPorts, proto.HysteriaPort)
}

// withPort returns ports with port present, in ascending order and without duplicates: the flag that
// adds one must not make the configuration different every time it runs.
func withPort(ports []int, port int) []int {
	if slices.Contains(ports, port) {
		return ports
	}
	out := append(append([]int(nil), ports...), port)
	slices.Sort(out)
	return out
}

// joinWords renders a list as "a, b and c".
func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return fmt.Sprintf("%s and %s", joinComma(words[:len(words)-1]), words[len(words)-1])
}

func joinComma(words []string) string {
	out := ""
	for i, word := range words {
		if i > 0 {
			out += ", "
		}
		out += word
	}
	return out
}
