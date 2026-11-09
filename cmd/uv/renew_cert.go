package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"time"

	"github.com/shogun-kub/termit/internal/uv/install"
)

// runRenewCert renews the node's certificate.
//
// Without --force it is the same check `uv init` makes: the certificate is renewed only when it is
// inside the renewal window. With --force it is replaced regardless, which is what a drill or a
// recovered CA wants. The decoy is not touched either way.
func (a *application) runRenewCert(args []string) int {
	flags := flag.NewFlagSet("renew-cert", flag.ContinueOnError)
	flags.SetOutput(a.stderr)
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	force := flags.Bool("force", false, "issue a new certificate even if the current one is not due")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := a.loadConfig(*configPath)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stdout, "termit-uv is not installed: no configuration at %s\n", *configPath)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "uv renew-cert: %v\n", err)
		return exitFailure
	}

	ctx, cancel := a.context()
	defer cancel()
	installer, err := install.New(a.installOptions(cfg, *configPath))
	if err != nil {
		fmt.Fprintf(a.stderr, "uv renew-cert: %v\n", err)
		return exitFailure
	}

	opts := install.RunOptions{Command: "renew-cert", Only: []string{"certificate"}}
	if *force {
		opts.Force = []string{"certificate"}
	}
	report, err := installer.Run(ctx, opts)
	a.printSteps(report)
	if err != nil {
		fmt.Fprintf(a.stderr, "uv renew-cert: %v\n", err)
		return exitFailure
	}
	if node := installer.State(); node.Certificate != nil {
		days := int(time.Until(node.Certificate.NotAfter).Hours() / 24)
		fmt.Fprintf(a.stdout, "certificate valid until %s (%d days)\n",
			node.Certificate.NotAfter.UTC().Format(time.RFC3339), days)
	}
	return exitOK
}
