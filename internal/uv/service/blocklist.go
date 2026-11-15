package service

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The blocklist pair fetches the network list on a schedule. It is a timer rather than a sleep loop for
// the same reason as certificate renewal: a daily oneshot is easier to inspect than a goroutine, and
// Persistent makes a missed run happen after a reboot instead of being skipped.
const (
	BlocklistUnitName  = "termit-uv-blocklist.service"
	BlocklistTimerName = "termit-uv-blocklist.timer"
)

// Blocklist renders the oneshot that runs `firewall refresh`. It changes sets inside the table the
// operator applied and never creates one, so it is safe to run whether or not the firewall is on.
func Blocklist(binary, config string) ([]byte, error) {
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
Description=Refresh the Termit network blocklist
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s firewall refresh --config %s
Nice=10
TimeoutStartSec=5min
`, binary, config)
	return []byte(b.String()), nil
}

// BlocklistTimer renders the schedule: soon after the timer starts (which is boot, or `init`), then once
// per refresh interval. The jitter keeps a fleet from hitting one mirror in the same second.
func BlocklistTimer(refresh time.Duration) ([]byte, error) {
	if refresh < time.Hour {
		return nil, fmt.Errorf("service: the blocklist refresh (%s) is shorter than an hour", refresh)
	}
	jitter := refresh / 10
	if jitter > time.Hour {
		jitter = time.Hour
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	fmt.Fprintf(&b, `
[Unit]
Description=Refresh the Termit network blocklist every %s

[Timer]
OnActiveSec=1min
OnUnitActiveSec=%s
RandomizedDelaySec=%s
AccuracySec=1min
Persistent=true

[Install]
WantedBy=timers.target
`, refresh, formatSeconds(refresh), formatSeconds(jitter))
	return []byte(b.String()), nil
}
