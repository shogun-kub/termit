package main

import (
	"context"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/install"
)

// nftApplier wires the firewall package to this host's executor. `status` asks the kernel one question
// with it; everything that changes the table goes through the shared commands.
func (a *application) nftApplier() firewall.Applier {
	return firewall.Applier{Runner: nftRunner{exec: a.exec}}
}

// nftRunner adapts the installer's executor to the firewall package's smaller interface.
type nftRunner struct{ exec install.Executor }

func (r nftRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, error) {
	res, err := r.exec.Run(ctx, install.Cmd{Name: name, Args: args, Stdin: stdin})
	return res.Stdout, err
}
