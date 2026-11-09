// Command mn is the Termit management server and admin CLI.
package main

import (
	"fmt"
	"os"

	"github.com/shogun-kub/termit/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}

	switch args[0] {
	case "version":
		fmt.Println(version.String("termit-mn"))
		return 0
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mn: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `termit-mn — Termit management server and admin CLI

Usage:
  mn <command> [flags]

Server:
  init         create config, database, master key and the first invite
  serve        run the HTTP API, subscription endpoint and scheduler

Admin:
  invite       create, list and revoke invite codes
  uv           list, show, approve, block, unblock and remove nodes
  check        run health checks now
  policy       read and change scheduler thresholds
  sub          show and rotate subscription tokens
  backup       write a consistent snapshot
  restore      restore from a snapshot
  status       show server, scheduler and prober health

  version      print build and protocol version
  help         show this help

Admin commands talk to the running server over a unix socket; they never open the
database directly.
`)
}
