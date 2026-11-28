package service

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// UsqueUnitName is the unit that runs the MASQUE sidecar.
const UsqueUnitName = "termit-usque.service"

// UsqueRelayUnitName is the unit that runs the loopback relay in front of the sidecar: the core reaches
// the sidecar through it, because the sidecar keys a UDP association on the control connection's address.
const UsqueRelayUnitName = "termit-usque-relay.service"

// UsqueUser is the account the sidecar runs as. It reads the registered device key and listens on
// loopback, so it needs neither the core's account nor the certificate's.
const UsqueUser = "termit-usque"

// Usque renders the MASQUE sidecar's unit. It carries UDP through its own userspace stack, so the core
// hands it both protocols and no host route or TUN device is involved.
//
// The trade against the in-core WireGuard outbound is measured and recorded in IMPLEMENTATION_PLAN.md
// §10.1: a userspace stack costs about a third of the throughput, and it is the only way to keep WARP's
// address for UDP on a host whose WARP client cannot (warp-cli's SOCKS5 has no UDP).
func Usque(binary, config, socks string, restartEvery time.Duration) ([]byte, error) {
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
	// A sidecar that has run for hours carries a MASQUE connection to Cloudflare that has gone stale:
	// the first request after an idle period pays seconds for it, and a fresh process is the cheaper
	// cure. RuntimeMaxSec stops it, Restart=always starts the replacement.
	//
	// --always-reconnect covers the tunnel that is lost while nothing is flowing: without it the sidecar
	// rebuilds only on the next request, and one whose state machine still believes the old session is
	// open answers every new request from a dead connection until the process is restarted.
	var maxRun string
	if restartEvery > 0 {
		maxRun = "\nRuntimeMaxSec=" + systemdSpan(restartEvery)
	}
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
ExecStart=%s socks -b %s -p %s -c %s --always-reconnect
Restart=always
RestartSec=3s%s
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
`, UsqueUser, UsqueUser, binary, host, port, config, maxRun)
	return []byte(b.String()), nil
}

// systemdSpan renders a duration in the form a unit file takes, keeping whole hours and minutes readable.
func systemdSpan(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dmin", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// UsqueRelay renders the relay's unit. It is the address the core dials: the sidecar refuses a UDP
// datagram that does not come from the port of the connection that asked for the association, and the
// relay is what makes those equal. It runs as the sidecar's account and needs nothing more than loopback.
func UsqueRelay(binary, listen, upstream string) ([]byte, error) {
	if !filepath.IsAbs(binary) && !strings.HasPrefix(binary, "/") {
		return nil, fmt.Errorf("service: binary %q must be an absolute path", binary)
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return nil, fmt.Errorf("service: relay listen address %q: %w", listen, err)
	}
	if _, _, err := net.SplitHostPort(upstream); err != nil {
		return nil, fmt.Errorf("service: relay upstream address %q: %w", upstream, err)
	}
	var b strings.Builder
	b.WriteString(unitHeader)
	fmt.Fprintf(&b, `
[Unit]
Description=Termit WARP relay (loopback SOCKS5 in front of the MASQUE sidecar)
Documentation=https://github.com/shogun-kub/termit
After=network-online.target termit-usque.service
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User=%s
Group=%s
ExecStart=%s warp-relay --listen %s --upstream %s
Restart=always
RestartSec=3s
UMask=0077
MemoryMax=128M

# It listens on loopback and forwards datagrams: no capability, no writable path, no new privileges.
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
`, UsqueUser, UsqueUser, binary, listen, upstream)
	return []byte(b.String()), nil
}
