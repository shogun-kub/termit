package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/shogun-kub/termit/internal/release"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
)

func (a *application) runUpgrade(args []string) int {
	flags := a.newFlagSet("upgrade", `Replaces this node's binary with another published release and lets
the new binary converge the node. Without --to it installs the version the management server reported at
the last sync, never "latest": a node follows its server, not the release feed.

The digest is checked against the release's SHA256SUMS before anything is replaced, the previous binary is
kept beside it, and a failure to converge puts the previous binary back.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	to := flags.String("to", "", "release to install, e.g. v0.7.0 (default: the server's version)")
	check := flags.Bool("check", false, "say what a run would do, and download nothing")
	base := flags.String("release-base", release.BaseURL, "where releases are published; a private mirror is allowed")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := config.Load(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %v\n", err)
		return exitFailure
	}
	target, code := a.upgradeTarget(cfg, *to)
	if code != exitOK || target == "" {
		return code
	}
	if *check {
		fmt.Fprintf(a.stdout, "would install %s over %s\n", target, version.Version)
		return exitOK
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: find the running binary: %v\n", err)
		return exitFailure
	}
	ctx, cancel := a.context()
	defer cancel()
	previous, err := upgradeBinary(ctx, a.http, *base, exe, target, runtime.GOARCH)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "installed %s (was %s); the previous binary is at %s\n", target, version.Version, previous)

	// The new binary converges the node: it is the one that knows what its own release needs. Running the
	// old code here would converge to the old shape and then call it done.
	if err := a.runConverge(ctx, exe); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %s did not converge the node: %v\n", target, err)
		if restoreErr := release.Restore(previous, exe); restoreErr != nil {
			fmt.Fprintf(a.stderr, "termit-uv upgrade: and the previous binary could not be put back: %v\n", restoreErr)
			return exitFailure
		}
		fmt.Fprintf(a.stderr, "termit-uv upgrade: the previous binary is back in place; nothing else changed\n")
		return exitFailure
	}
	return exitOK
}

// upgradeTarget resolves what the node should install, and reports the reasons it should not install
// anything. It returns the version to install, or an exit code.
func (a *application) upgradeTarget(cfg *config.Config, requested string) (string, int) {
	target := strings.TrimSpace(requested)
	if target == "" {
		node, err := state.LoadInstalled(cfg.Paths.StatePath())
		switch {
		case errors.Is(err, state.ErrNotInstalled):
			fmt.Fprintln(a.stderr, "termit-uv upgrade: this node is not installed; run init first")
			return "", exitUsage
		case err != nil:
			fmt.Fprintf(a.stderr, "termit-uv upgrade: %v\n", err)
			return "", exitFailure
		}
		if node.ServerVersion == "" {
			fmt.Fprintln(a.stderr, "termit-uv upgrade: the server has not reported its version yet; "+
				"run sync (or name the release with --to)")
			return "", exitUsage
		}
		target = node.ServerVersion
	}
	if !release.Version.MatchString(target) {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %q is not a release; a node follows vX.Y.Z, never a "+
			"pre-release or a branch\n", target)
		return "", exitUsage
	}
	if target == version.Version {
		fmt.Fprintf(a.stdout, "already at %s\n", target)
		return "", exitOK
	}
	return target, exitOK
}

// upgradeBinary fetches one verified release binary and swaps it in beside the running one.
func upgradeBinary(ctx context.Context, client *http.Client, base, exe, ver, arch string) (string, error) {
	body, err := release.Fetch(ctx, client, base, "termit-uv", ver, arch)
	if err != nil {
		return "", err
	}
	return release.Install(body, exe)
}

// runConverge runs the freshly installed binary. It is a seam: production replaces the process with
// `exe init`, and a test says it would have.
func (a *application) runConverge(ctx context.Context, exe string) error {
	if a.convergeFn != nil {
		return a.convergeFn(ctx, exe)
	}
	cmd := exec.CommandContext(ctx, exe, "init", "--config", a.configPath)
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	return cmd.Run()
}
