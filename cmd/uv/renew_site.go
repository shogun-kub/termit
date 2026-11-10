package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/shogun-kub/termit/internal/uv/decoy"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/state"
)

func (a *application) runRenewSite(args []string) int {
	flags := a.newFlagSet("renew-site", `Regenerates the decoy site from the node's seed and rewrites the nginx fragment that
serves its sign-in form. Other secrets are untouched; --seed and --template override the
persisted values.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	template := flags.String("template", "", "archetype to generate: "+strings.Join(decoy.Templates(), ", "))
	seed := flags.String("seed", "", "new seed (at least 8 characters); every other secret stays")
	dryRun := flags.Bool("dry-run", false, "show what would change without touching the host")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv renew-site: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", *configFlag)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv renew-site: %v\n", err)
		return exitFailure
	}

	configChanged := false
	if *template != "" {
		if !decoy.KnownTemplate(*template) {
			fmt.Fprintf(a.stderr, "termit-uv renew-site: unknown template %q (known: %s)\n",
				*template, strings.Join(decoy.Templates(), ", "))
			return exitUsage
		}
		if cfg.Decoy.Template != *template {
			cfg.Decoy.Template = *template
			configChanged = true
		}
	}
	if *seed != "" && len(*seed) < 8 {
		fmt.Fprintln(a.stderr, "termit-uv renew-site: --seed must be at least 8 characters")
		return exitUsage
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv renew-site: %v\n", err)
		return exitUsage
	}

	ctx, cancel := a.context()
	defer cancel()

	installer, err := install.New(a.installOptions(cfg, *configFlag))
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv renew-site: %v\n", err)
		return exitFailure
	}
	node := installer.State()
	if node.Decoy.Seed == "" {
		fmt.Fprintln(a.stderr, "termit-uv renew-site: the node has no decoy seed; run `termit-uv init` first")
		return exitFailure
	}
	if *seed != "" {
		node.Decoy.Seed = *seed
		node.Touch()
	}
	if configChanged && !*dryRun {
		if err := cfg.Save(*configFlag); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv renew-site: %v\n", err)
			return exitFailure
		}
	}

	report, err := installer.Run(ctx, install.RunOptions{
		Command: "renew-site",
		Only:    []string{"decoy"},
		Force:   []string{"decoy"},
		DryRun:  *dryRun,
	})
	if report != nil {
		a.printSteps(report)
		fmt.Fprintf(a.stdout, "\n%s\n", summarizeReport(report))
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "\nuv renew-site: %v\n", err)
		return exitFailure
	}
	if *dryRun {
		return exitOK
	}

	fmt.Fprintf(a.stdout, "\ntemplate: %s\nseed:     %s\ndigest:   %s\nsite:     %s\nfragment: %s\n",
		node.Decoy.Template,
		state.Fingerprint(node.Decoy.Seed),
		node.Decoy.Digest,
		cfg.Paths.SiteDir,
		cfg.Paths.DecoySnippet)
	a.reloadWebTier(ctx, cfg.Paths.NginxSite)
	return exitOK
}

// reloadWebTier asks nginx to pick up the rewritten fragment, but only once Termit manages the vhost.
// Until then the fragment is written and the caller is told so, instead of reloading a server whose
// configuration does not include it yet.
func (a *application) reloadWebTier(ctx context.Context, siteConfig string) {
	if _, err := os.Stat(siteConfig); err != nil {
		fmt.Fprintf(a.stdout, "\nnginx: %s does not exist yet, so nothing was reloaded; the generated vhost will include the fragment\n", siteConfig)
		return
	}
	if _, err := a.exec.Run(ctx, install.Cmd{Name: "nginx", Args: []string{"-t"}}); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv renew-site: nginx -t failed after the rewrite: %v\n", err)
		return
	}
	if _, err := a.exec.Run(ctx, install.Cmd{Name: "systemctl", Args: []string{"reload", "nginx"}}); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv renew-site: reload nginx: %v\n", err)
		return
	}
	fmt.Fprintln(a.stdout, "\nnginx: reloaded")
}
