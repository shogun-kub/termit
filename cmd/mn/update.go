package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/shogun-kub/termit/internal/release"
	"github.com/shogun-kub/termit/internal/version"
)

// runUpdate replaces the server's binary with the newest published release. It is deliberately absent from
// `status` and `doctor`: the server answers questions about the mesh, and "an update exists" is not one of
// them — a check that reached GitHub on every status call would also be the only network dependency of a
// component that otherwise needs none but its own nodes.
func runUpdate(args []string, stdout, stderr io.Writer) int {
	return updater{restart: systemdRestart}.run(args, stdout, stderr)
}

// updater is the command with the seams a test needs: the service action, the executable it replaces and
// the endpoint it asks. Production leaves them nil.
type updater struct {
	restart   func(context.Context) error
	exe       func() (string, error)
	latestURL string
	client    *http.Client
}

func (u updater) run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("termit-mn update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "report whether a newer release exists, and install nothing (the default)")
	apply := flags.Bool("apply", false, "install the newest release and restart the server")
	base := flags.String("release-base", release.BaseURL, "where releases are published; a private mirror is allowed")
	if err := flags.Parse(args); err != nil {
		return flagExit(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn update: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *check && *apply {
		fmt.Fprintln(stderr, "termit-mn update: --check and --apply are different questions; pass one")
		return exitUsage
	}

	ctx := context.Background()
	client := u.client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	latest := u.latestURL
	if latest == "" {
		latest = release.LatestURL
	}
	target, err := release.Newest(ctx, client, latest)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn update: look up the newest release: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "this build: %s\nnewest:     %s\n", version.Version, target)
	if target == version.Version {
		fmt.Fprintln(stdout, "already at the newest release")
		return exitOK
	}
	if !*apply {
		fmt.Fprintln(stdout, "run `termit-mn update --apply` to install it")
		return exitOK
	}

	executable := u.exe
	if executable == nil {
		executable = os.Executable
	}
	exe, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn update: find the running binary: %v\n", err)
		return exitFailure
	}
	body, err := release.Fetch(ctx, client, *base, "termit-mn", target, runtime.GOARCH)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn update: %v\n", err)
		return exitFailure
	}
	previous, err := release.Install(body, exe)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn update: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "installed %s (was %s); the previous binary is at %s\n", target, version.Version, previous)

	if err := u.restart(ctx); err != nil {
		fmt.Fprintf(stderr, "termit-mn update: restart the server: %v\n", err)
		if restoreErr := release.Restore(previous, exe); restoreErr != nil {
			fmt.Fprintf(stderr, "termit-mn update: and the previous binary could not be put back: %v\n", restoreErr)
			return exitFailure
		}
		fmt.Fprintf(stderr, "termit-mn update: the previous binary is back in place; restart the server to run it\n")
		return exitFailure
	}
	fmt.Fprintln(stdout, "the server is running the new build")
	return exitOK
}

// systemdRestart is the one service action the update takes: the server's own unit, and nothing else.
func systemdRestart(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "systemctl", "restart", "termit-mn")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
