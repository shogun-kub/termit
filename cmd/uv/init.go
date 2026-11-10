package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

func (a *application) runInit(args []string) int {
	flags := a.newFlagSet("init", `Installs this node: host checks, packages, the pinned proxy core and the node
identity. Re-running converges: steps whose effect is already present do nothing, and no
credential is ever regenerated.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	domain := flags.String("domain", "", "node domain name; required on the first run")
	email := flags.String("email", "", "ACME account contact address")
	acmeCA := flags.String("acme-ca", "", "ACME directory URL")
	acmeInsecure := flags.Bool("acme-skip-tls-verify", false, "lab only: do not verify the ACME server certificate")
	template := flags.String("decoy-template", "", "decoy site archetype")
	xrayVersion := flags.String("xray-version", "", "Xray-core release to install")
	xrayBase := flags.String("xray-download-base", "", "release directory: an http(s) URL, a file URL or a local path")
	dryRun := flags.Bool("dry-run", false, "report what would change without touching the host")
	only := flags.String("only", "", "run only these steps, comma separated")
	skip := flags.String("skip", "", "skip these steps, comma separated")
	relocate := flags.Bool("relocate", false, "install a rebuilt node: reuse the agent key, mint a new identity, do not enroll")
	agentKeyFile := flags.String("agent-key-file", "",
		"0600 file holding the agent key of the node being rebuilt (required with --relocate)")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv init: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *relocate != (*agentKeyFile != "") {
		fmt.Fprintln(a.stderr, "uv init: --relocate and --agent-key-file are used together")
		return exitUsage
	}

	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })

	cfg, err := config.Load(*configFlag)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = config.Default()
	case err != nil:
		fmt.Fprintf(a.stderr, "uv init: %v\n", err)
		return exitFailure
	}

	// Flags win over the file, and a flag that was not given leaves the file's value alone.
	if set["domain"] {
		cfg.Domain = *domain
	}
	if set["email"] {
		cfg.Email = *email
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
	if set["xray-version"] {
		cfg.Xray.Version = *xrayVersion
	}
	if set["xray-download-base"] {
		cfg.Xray.DownloadBase = *xrayBase
	}

	if cfg.Domain == "" {
		fmt.Fprintln(a.stderr, "uv init: --domain is required on the first run")
		return exitUsage
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(a.stderr, "uv init: %v\n", err)
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
			fmt.Fprintf(a.stderr, "uv init: %v\n", err)
			return exitFailure
		case node.Identity.SelfUUID != "":
			fmt.Fprintln(a.stderr,
				"uv init: this node already has an identity; --relocate is for a rebuilt box whose state is gone")
			return exitFailure
		}
		relocateKey, err = readAgentKeyFile(*agentKeyFile)
		if err != nil {
			fmt.Fprintf(a.stderr, "uv init: %v\n", err)
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
			fmt.Fprintf(a.stderr, "uv init: %s: %s\n", check.Name, check.Detail)
		}
	}
	if !preflight.OK() {
		fmt.Fprintln(a.stderr, "uv init: host checks failed; nothing was changed")
		return exitFailure
	}

	if !*dryRun {
		if err := cfg.Save(*configFlag); err != nil {
			fmt.Fprintf(a.stderr, "uv init: %v\n", err)
			return exitFailure
		}
	}
	fmt.Fprintf(a.stdout, "config: %s\n\n", *configFlag)

	installer, err := install.New(a.installOptions(cfg, *configFlag))
	if err != nil {
		fmt.Fprintf(a.stderr, "uv init: %v\n", err)
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
			fmt.Fprintf(a.stderr, "uv init: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, "\nrelocated: a new identity is installed; the server still holds the old one.")
		fmt.Fprintln(a.stdout, "next: termit-uv unblock   (the request carries the new endpoint built from this identity)")
	}
	if len(pendingWork) > 0 {
		fmt.Fprintf(a.stdout, "not yet installed by this build: %s\n", joinWords(pendingWork))
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
