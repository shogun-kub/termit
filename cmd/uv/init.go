package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"runtime"

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
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv init: unexpected argument %q\n", flags.Arg(0))
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

	installer, err := install.New(a.installOptions(cfg))
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
	if len(pendingWork) > 0 {
		fmt.Fprintf(a.stdout, "not yet installed by this build: %s\n", joinWords(pendingWork))
	}
	return exitOK
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
