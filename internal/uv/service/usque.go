package service

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// UsqueUnitName is the unit that runs the MASQUE sidecar.
const UsqueUnitName = "termit-usque.service"

// UsqueUser is the account the sidecar runs as. It reads the registered device key and listens on
// loopback, so it needs neither the core's account nor the certificate's.
const UsqueUser = "termit-usque"

// Usque renders the MASQUE sidecar's unit. It carries UDP through its own userspace stack, so the core
// hands it both protocols and no host route or TUN device is involved.
//
// The trade against the in-core WireGuard outbound is measured and recorded in IMPLEMENTATION_PLAN.md
// §10.1: a userspace stack costs about a third of the throughput, and it is the only way to keep WARP's
// address for UDP on a host whose WARP client cannot (warp-cli's SOCKS5 has no UDP).
func Usque(binary, config, socks string) ([]byte, error) {
	if !filepath.IsAbs(binary) && !strings.HasPrefix(binary, "/") ||
		!filepath.IsAbs(config) && !strings.HasPrefix(config, "/") {
		return nil, fmt.Errorf("service: binary %q and config %q must be absolute paths", binary, config)
	}
	host, port := "127.0.0.1", "40001"
	if socks != "" {
		parsedHost, parsedPort, err := net.SplitHostPort(socks)
		if err != nil {
			return nil, fmt.Errorf("service: usque socks address %q: %w", socks, err)
		}
		host, port = parsedHost, parsedPort
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	fmt.Fprintf(&b, `
[Unit]
Description=Termit WARP sidecar (MASQUE)
Documentation=https://github.com/shogun-kub/termit
After=network-online.target termit-xray.service
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User=%s
Group=%s
ExecStart=%s socks -b %s -p %s -c %s
Restart=always
RestartSec=3s
LimitNOFILE=65535
UMask=0077
# The sidecar emulates a network stack in userspace for every proxied flow. The ceiling makes a leak in
# it a restart of the sidecar instead of an OOM that takes the node's other services with it.
MemoryMax=256M

# It listens on loopback and writes nothing: no capability, no writable path, no new privileges.
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`, UsqueUser, UsqueUser, binary, host, port, config)
	return []byte(b.String()), nil
}
