package service

import (
	"fmt"
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
)

// ConfirmWindow is how long an applied firewall waits for `termit-uv firewall confirm`.
const ConfirmWindow = firewall.ConfirmWindow

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
