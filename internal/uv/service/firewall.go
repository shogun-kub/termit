package service

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// The rollback pair is what makes `firewall on` safe to run over the connection it might cut: the
// timer fires once, after ConfirmWindow, and the oneshot removes the table unless `firewall confirm`
// ran in the meantime. Both units exist only while an apply is unconfirmed — `confirm` and `off`
// disable and delete them.
const (
	FirewallRollbackUnitName  = "termit-uv-firewall-rollback.service"
	FirewallRollbackTimerName = "termit-uv-firewall-rollback.timer"
	// FirewallBootUnitName is the oneshot that puts a confirmed table back after a reboot.
	FirewallBootUnitName = "termit-uv-firewall.service"
)

// ConfirmWindow is how long an applied firewall waits for `termit-uv firewall confirm`.
const ConfirmWindow = firewall.ConfirmWindow

// FirewallBoot renders the boot oneshot: it re-applies the table the last `firewall on` put in place, if
// the record says it was confirmed, and arms the rollback timer for one that was still waiting.
//
// It is ordered before network-pre.target so the table is up before the services that listen, and it never
// fails the boot: a host that never applied a table, or one whose operator took it down, exits 0 with a
// line in the journal.
func FirewallBoot(binary, config, stateDir, unitDir string) ([]byte, error) {
	if !filepath.IsAbs(binary) && !strings.HasPrefix(binary, "/") {
		return nil, fmt.Errorf("service: binary %q must be an absolute path", binary)
	}
	if !filepath.IsAbs(config) && !strings.HasPrefix(config, "/") {
		return nil, fmt.Errorf("service: config %q must be an absolute path", config)
	}
	if !filepath.IsAbs(stateDir) && !strings.HasPrefix(stateDir, "/") {
		return nil, fmt.Errorf("service: state dir %q must be an absolute path", stateDir)
	}
	if !filepath.IsAbs(unitDir) && !strings.HasPrefix(unitDir, "/") {
		return nil, fmt.Errorf("service: unit dir %q must be an absolute path", unitDir)
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	fmt.Fprintf(&b, `
[Unit]
Description=Restore the Termit firewall table a confirmed apply left behind
Documentation=https://github.com/shogun-kub/termit
DefaultDependencies=no
Wants=network-pre.target
Before=network-pre.target
After=local-fs.target

[Service]
# No RemainAfterExit: this is an action, not a state. A unit that stays "active (exited)" makes a later
# systemctl start a no-op — and an operator who deleted the table by hand would get nothing back.
Type=oneshot
ExecStart=%s firewall restore --config %s
TimeoutStartSec=60s
# Root for nft, nothing else: it reads the configuration and the record and touches the kernel.
# The unit directory is writable because restoring an apply that was still waiting arms the rollback
# timer, and arming it writes the timer's units — a read-only /etc/systemd/system made that path fail at
# boot with "read-only file system".
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=%s %s
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
# nft talks to the kernel over netlink; without AF_NETLINK every run fails with "Unable to initialize
# Netlink socket".
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
`, binary, config, path.Clean(stateDir), path.Clean(unitDir))
	return []byte(b.String()), nil
}

// FirewallRollback renders the oneshot that takes the table down unless it was confirmed.
func FirewallRollback(binary, config string) ([]byte, error) {
	if !filepath.IsAbs(binary) && !strings.HasPrefix(binary, "/") {
		return nil, fmt.Errorf("service: binary %q must be an absolute path", binary)
	}
	if !filepath.IsAbs(config) && !strings.HasPrefix(config, "/") {
		return nil, fmt.Errorf("service: config %q must be an absolute path", config)
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	fmt.Fprintf(&b, `
[Unit]
Description=Remove the Termit firewall because it was not confirmed
# No ordering against the network: this runs to undo a change, not to make one.
After=termit-uv-firewall-rollback.timer

[Service]
Type=oneshot
ExecStart=%s firewall rollback --config %s
TimeoutStartSec=60s
`, binary, config)
	return []byte(b.String()), nil
}

// FirewallRollbackTimer renders the one-shot timer. OnActiveSec counts from the moment the timer is
// started, which is the apply, so a re-armed firewall gets a fresh window and an untouched one cannot
// fire late and remove something the operator confirmed hours ago.
func FirewallRollbackTimer(window time.Duration) ([]byte, error) {
	if window <= 0 {
		return nil, fmt.Errorf("service: the rollback window must be positive, got %s", window)
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	fmt.Fprintf(&b, `
[Unit]
Description=Remove the Termit firewall unless it is confirmed within %s

[Timer]
OnActiveSec=%s
AccuracySec=1s
RemainAfterElapse=no
`, window, formatSeconds(window))
	return []byte(b.String()), nil
}

// formatSeconds renders a duration as whole seconds, which is the only form OnActiveSec accepts
// without a unit suffix.
func formatSeconds(d time.Duration) string {
	return fmt.Sprintf("%ds", int64(d/time.Second))
}
