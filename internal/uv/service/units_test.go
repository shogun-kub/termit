package service_test

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/service"
)

func TestXrayUnit(t *testing.T) {
	t.Parallel()
	unit, err := service.Xray("/usr/lib/termit/bin/xray", "/etc/termit/uv/xray.json")
	if err != nil {
		t.Fatalf("Xray: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"ExecStart=/usr/lib/termit/bin/xray run -config /etc/termit/uv/xray.json",
		"User=termit",
		"Restart=on-failure",
		// The hardening the design promises: no privilege, no writable system, no home, no devices.
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"PrivateDevices=yes",
		"CapabilityBoundingSet=",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX",
		"MemoryDenyWriteExecute=yes",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit is missing %q", want)
		}
	}
	// A config path that does not come from the configuration would make the unit unportable.
	if !strings.Contains(text, "Documentation=") {
		t.Error("unit has no Documentation entry")
	}
}

func TestXrayUnitRefusesRelativePaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ binary, config string }{
		{"xray", "/etc/termit/uv/xray.json"},
		{"/usr/lib/termit/bin/xray", "xray.json"},
		{"", ""},
	} {
		if _, err := service.Xray(tc.binary, tc.config); err == nil {
			t.Errorf("Xray(%q, %q) accepted a relative path", tc.binary, tc.config)
		}
	}
}

func TestNginxDropIn(t *testing.T) {
	t.Parallel()
	dropIn, err := service.NginxDropIn()
	if err != nil {
		t.Fatalf("NginxDropIn: %v", err)
	}
	text := string(dropIn)
	for _, want := range []string{"[Service]", "LimitNOFILE=65535", "PrivateTmp=yes", "ProtectHome=yes"} {
		if !strings.Contains(text, want) {
			t.Errorf("drop-in is missing %q", want)
		}
	}
	// The drop-in must not try to change nginx's user, ports or binary: Termit does not own that unit.
	for _, forbidden := range []string{"ExecStart=", "User=", "CapabilityBoundingSet="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("drop-in contains %q, which would override the distribution's unit", forbidden)
		}
	}
}

func TestAgentUnit(t *testing.T) {
	t.Parallel()
	unit, err := service.Agent("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml",
		[]string{"/etc/termit/uv", "/var/lib/termit/uv"}, []string{"/root"})
	if err != nil {
		t.Fatalf("Agent: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"ExecStart=/usr/bin/termit-uv agent -v --config /etc/termit/uv/config.yaml",
		"Restart=on-failure",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=/etc/termit/uv /var/lib/termit/uv",
		"ReadOnlyPaths=/root",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("agent unit is missing %q", want)
		}
	}
	// /root is hidden by ProtectHome, so the hardening would make the CA file unreadable. A unit that
	// cannot read its own config input must not pretend to be hardened.
	if strings.Contains(text, "ProtectHome=yes") {
		t.Error("agent unit sets ProtectHome while it must read a file under /root")
	}
	plain, err := service.Agent("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml",
		[]string{"/etc/termit/uv"}, []string{"/etc/termit/uv/ca.pem"})
	if err != nil {
		t.Fatalf("Agent: %v", err)
	}
	if !strings.Contains(string(plain), "ProtectHome=yes") {
		t.Error("agent unit dropped ProtectHome for a readable path outside home")
	}
	// The agent writes the core config and asks systemd to restart the core; it must not run without
	// a writable path for either.
	if _, err := service.Agent("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml", []string{"relative/path"}, nil); err == nil {
		t.Error("Agent accepted a relative writable path")
	}
	if _, err := service.Agent("termit-uv", "/etc/termit/uv/config.yaml", nil, nil); err == nil {
		t.Error("Agent accepted a relative binary path")
	}
}

func TestRenewUnit(t *testing.T) {
	t.Parallel()
	unit, err := service.Renew("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml")
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"Type=oneshot",
		"ExecStart=/usr/bin/termit-uv renew-cert --config /etc/termit/uv/config.yaml",
		"NoNewPrivileges=yes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("renew unit is missing %q", want)
		}
	}
	// A oneshot that restarts itself would fight the timer.
	if strings.Contains(text, "Restart=") {
		t.Error("renew unit sets Restart, which the timer already handles")
	}
	if _, err := service.Renew("termit-uv", "/etc/termit/uv/config.yaml"); err == nil {
		t.Error("Renew accepted a relative binary path")
	}
}

func TestRenewTimer(t *testing.T) {
	t.Parallel()
	timer, err := service.RenewTimer()
	if err != nil {
		t.Fatalf("RenewTimer: %v", err)
	}
	text := string(timer)
	for _, want := range []string{
		"OnCalendar=daily",
		"RandomizedDelaySec=",
		"Persistent=true",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("timer is missing %q", want)
		}
	}
}
