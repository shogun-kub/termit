package service_test

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/service"
)

// TestGuardUnit: the unit is a long-running loop that can add elements to a table, so it must say what
// it is allowed to write and nothing more.
func TestGuardUnit(t *testing.T) {
	t.Parallel()
	unit, err := service.Guard("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml", "/var/lib/termit/uv",
		"/etc/termit/uv", "/var/log/nginx")
	if err != nil {
		t.Fatalf("Guard: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"Type=simple",
		"ExecStart=/usr/bin/termit-uv guard run --config /etc/termit/uv/config.yaml",
		"Restart=on-failure",
		"ReadWritePaths=/var/lib/termit/uv",
		"ReadOnlyPaths=/etc/termit/uv /var/log/nginx",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		// nft talks to the kernel over netlink: without AF_NETLINK every cycle fails, which is exactly
		// what the lab reported before this line was learned.
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit is missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"CapabilityBoundingSet", "ProtectHome=no"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("unit carries %q", forbidden)
		}
	}
	if _, err := service.Guard("termit-uv", "/etc/termit/uv/config.yaml", "/var/lib/termit/uv"); err == nil {
		t.Error("a relative binary path was accepted")
	}
	if _, err := service.Guard("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml", "state"); err == nil {
		t.Error("a relative state directory was accepted")
	}
}
