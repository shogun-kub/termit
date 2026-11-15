package service

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// GuardUnitName is the ban engine. It is a separate long-running unit rather than a timer because it
// has to react within a cycle and keep counters in memory between them; systemd owns its lifetime, not
// its clock.
const GuardUnitName = "termit-uv-guard.service"

// Guard renders the ban engine's unit.
//
// It runs as root because adding an element to the firewall's table is a privileged operation, and it
// is confined to exactly what it needs: the state directory (bans.json) to write, its configuration and
// the web server's log directory to read. It never renders or applies the table itself — that is the
// operator's `firewall` command — so a compromised guard cannot open or close the host.
func Guard(binary, config, stateDir string, readable ...string) ([]byte, error) {
	if !filepath.IsAbs(binary) && !strings.HasPrefix(binary, "/") {
		return nil, fmt.Errorf("service: binary %q must be an absolute path", binary)
	}
	if !filepath.IsAbs(config) && !strings.HasPrefix(config, "/") {
		return nil, fmt.Errorf("service: config %q must be an absolute path", config)
	}
	if !filepath.IsAbs(stateDir) && !strings.HasPrefix(stateDir, "/") {
		return nil, fmt.Errorf("service: state dir %q must be an absolute path", stateDir)
	}
	readOnly, err := absolutePaths(readable)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	fmt.Fprintf(&b, `
[Unit]
Description=Termit ban engine
Documentation=https://github.com/shogun-kub/termit
After=network-online.target termit-xray.service
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=10

[Service]
Type=simple
ExecStart=%s guard run --config %s
Restart=on-failure
RestartSec=30s
Nice=10

# Root for nft, nothing else: writes go to the state directory, reads to the configuration and the log.
# AF_NETLINK is not optional in RestrictAddressFamilies below: nft talks to the kernel through it, and
# without it every cycle fails with "Unable to initialize Netlink socket", which is how the lab found it.
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=%s
`, binary, config, path.Clean(stateDir))
	if len(readOnly) > 0 {
		fmt.Fprintf(&b, "ReadOnlyPaths=%s\n", strings.Join(readOnly, " "))
	}
	if hidesAReadablePath(readOnly) {
		b.WriteString("# ProtectHome is off: a readable path lives under a home directory.\n")
	} else {
		b.WriteString("ProtectHome=yes\n")
	}
	b.WriteString(`PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`)
	return []byte(b.String()), nil
}
