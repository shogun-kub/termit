package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/shogun-kub/termit/internal/uv/install"
)

func (a *application) runUninstall(args []string) int {
	flags := a.newFlagSet("uninstall", `Removes what Termit created: the systemd units, the installed core and the generated
nginx vhost. --purge also removes the configuration, state and certificates and the service
user. Packages are never removed and no other site on nginx is touched.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	purge := flags.Bool("purge", false, "also remove configuration, state, certificates and the service user")
	dryRun := flags.Bool("dry-run", false, "show what would be removed without changing anything")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "uv uninstall: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := a.loadConfig(*configFlag)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", *configFlag)
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "uv uninstall: %v\n", err)
		return exitFailure
	}

	ctx, cancel := a.context()
	defer cancel()
	report, err := install.Uninstall(ctx, install.UninstallOptions{
		Config: cfg,
		Exec:   a.exec,
		Logger: slog.Default(),
		Now:    a.now,
		Purge:  *purge,
		DryRun: *dryRun,
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "uv uninstall: %v\n", err)
		return exitFailure
	}

	verb := "removed"
	if *dryRun {
		verb = "would remove"
	}
	if len(report.Removed) == 0 {
		fmt.Fprintf(a.stdout, "%s: nothing\n", verb)
	} else {
		fmt.Fprintf(a.stdout, "%s:\n", verb)
		for _, entry := range report.Removed {
			fmt.Fprintf(a.stdout, "  %s\n", entry)
		}
	}
	if len(report.Kept) > 0 {
		fmt.Fprintln(a.stdout, "kept:")
		for _, entry := range report.Kept {
			fmt.Fprintf(a.stdout, "  %s\n", entry)
		}
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(a.stderr, "warning: %s\n", warning)
	}
	if !*purge {
		fmt.Fprintln(a.stdout, "\nidentity and certificates were kept; use --purge to remove them as well")
	}
	return exitOK
}
