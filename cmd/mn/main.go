// Command mn is the Termit management server and admin CLI.
package main

import (
	"fmt"
	"os"

	"github.com/shogun-kub/termit/internal/termui"
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
	// On a terminal the bare binary is the menu; anywhere else it stays the usage text, so a script
	// that runs `mn` by mistake gets exit 2 and a short page, not a prompt waiting for input.
	if len(args) == 0 && termui.Interactive(os.Stdin, os.Stdout) {
		return runMenu(stdout, stderr, os.Stdin, termui.PlainColour(stdout))
	}
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
	case "policy":
		return runPolicy(args[1:], stdout, stderr)
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
	case "check":
		return runServerCheck(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "menu":
		return runMenu(stdout, stderr, os.Stdin, termui.PlainColour(stdout))
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
  check        run health checks now (one node, or the whole mesh with --all)
  policy       read and change scheduler thresholds
  sub          show and rotate subscription tokens
  status       show server, nodes and the check schedule

  version      print build and protocol version
  menu         the interactive menu (what the bare command does on a terminal)
  help         show this help

Admin commands talk to the running server over a unix socket; they never open the
database directly.
`)
}
