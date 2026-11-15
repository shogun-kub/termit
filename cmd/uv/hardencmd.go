package main

import "github.com/shogun-kub/termit/internal/hardencmd"

// The security commands are the shared ones: the table, the bans, the guard and the blocklist work the
// same way on both binaries, and a difference between them would be a bug nobody would notice. What is
// node-specific — the table name, the ports, the units, the access log, the management server in the
// never-ban set — lives in harden.go and rollback.go.

func (a *application) runFirewall(args []string) int { return hardencmd.Firewall(a.harden(), args) }

func (a *application) runBans(args []string) int { return hardencmd.Bans(a.harden(), args) }

func (a *application) runGuard(args []string) int { return hardencmd.Guard(a.harden(), args) }
