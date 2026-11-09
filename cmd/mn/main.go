// Command mn is the Termit management server and admin CLI.
package main

import (
	"fmt"
	"os"

	"github.com/shogun-kub/termit/internal/version"
)

// Exit codes, shared with termit-uv so a script can treat both binaries the same.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}

	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "invite":
		return runInvite(args[1:], stdout, stderr)
	case "uv":
		return runUV(args[1:], stdout, stderr)
	case "sub":
		return runSub(args[1:], stdout, stderr)
	case "backup":
		return runBackup(args[1:], stdout, stderr)
	case "restore":
		return runRestore(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version.String("termit-mn"))
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	case "check", "policy", "status":
		fmt.Fprintf(stderr, "mn %s: not in this build yet\n", args[0])
		return exitFailure
	default:
		fmt.Fprintf(stderr, "mn: unknown command %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `termit-mn — Termit management server and admin CLI

Usage:
  mn <command> [flags]

Server:
  init         create config, database, master key and the first invite
  serve        run the HTTP API, subscription endpoint and scheduler
  backup       write a consistent snapshot
  restore      restore from a snapshot

Admin:
  invite       create, list and revoke invite codes
  uv           list, show, approve, block, unblock and remove nodes
  check        run health checks now
  policy       read and change scheduler thresholds
  sub          show and rotate subscription tokens
  status       show server, scheduler and prober health

  version      print build and protocol version
  help         show this help

Admin commands talk to the running server over a unix socket; they never open the
database directly.
`)
}
