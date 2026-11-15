package main

import (
	"os"
	"time"

	"github.com/shogun-kub/termit/internal/hardencmd"
	"github.com/shogun-kub/termit/internal/uv/install"
)

// The security commands are the same code the node runs: the table name, the ports, the units and the
// access log are what differ, and they live in harden.go.
func runFirewall(args []string, stdout, stderr *os.File) int {
	return hardencmd.Firewall(newHarden(stdout, stderr), args)
}

func runBans(args []string, stdout, stderr *os.File) int {
	return hardencmd.Bans(newHarden(stdout, stderr), args)
}

func runGuard(args []string, stdout, stderr *os.File) int {
	return hardencmd.Guard(newHarden(stdout, stderr), args)
}

// newHarden is the server's view of this process.
func newHarden(stdout, stderr *os.File) *hardencmd.Env {
	h := &mnHarden{exec: install.OSExecutor{}, now: time.Now, root: func() bool { return os.Geteuid() == 0 }}
	return h.env(stdout, stderr)
}
