// Command uv is the installer, agent and CLI for one Termit node.
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
		fmt.Println(version.String("termit-uv"))
		return 0
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "uv: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `termit-uv — Termit node installer, agent and CLI

Usage:
  uv <command> [flags]

Commands:
  init         install and configure this node (idempotent)
  doctor       run pre-flight and health diagnostics
  status       show node state, peers and last sync
  sync         reconcile peers with MN now
  unblock      request unblocking after this node was marked blocked
  migrate      move this node's identity to a new domain or server
  renew-site   regenerate the decoy site
  renew-cert   force certificate renewal
  warp         manage the optional WARP egress
  uninstall    remove Termit from this node
  version      print build and protocol version
  help         show this help

Both this binary and xray-core run on the node; see docs/DESCRIPTION.md §5 for
port and service ownership.
`)
}
