package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/service"
)

// TestBlocklistUnit: the oneshot runs the refresh and nothing else, and it does not need root beyond
// what nft already requires from the operator's own command.
func TestBlocklistUnit(t *testing.T) {
	t.Parallel()
	unit, err := service.Blocklist("/usr/bin/termit-uv", "/etc/termit/uv/config.yaml")
	if err != nil {
		t.Fatalf("Blocklist: %v", err)
	}
	text := string(unit)
	for _, want := range []string{
		"Type=oneshot",
		"ExecStart=/usr/bin/termit-uv firewall refresh --config /etc/termit/uv/config.yaml",
		"TimeoutStartSec=5min",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit is missing %q:\n%s", want, text)
		}
	}
	if _, err := service.Blocklist("termit-uv", "/etc/termit/uv/config.yaml"); err == nil {
		t.Error("a relative binary path was accepted")
	}
}

// TestBlocklistTimer pins the schedule: soon after the timer starts, then once per interval, with jitter
// and across reboots.
func TestBlocklistTimer(t *testing.T) {
	t.Parallel()
	timer, err := service.BlocklistTimer(24 * time.Hour)
	if err != nil {
		t.Fatalf("BlocklistTimer: %v", err)
	}
	text := string(timer)
	for _, want := range []string{
		"OnActiveSec=1min",
		"OnUnitActiveSec=86400s",
		"RandomizedDelaySec=3600s",
		"Persistent=true",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("timer is missing %q:\n%s", want, text)
		}
	}
	// A six-hourly refresh is allowed and jitters by less.
	six, err := service.BlocklistTimer(6 * time.Hour)
	if err != nil {
		t.Fatalf("BlocklistTimer: %v", err)
	}
	if !strings.Contains(string(six), "RandomizedDelaySec=2160s") {
		t.Errorf("a 6h refresh jitters by %q", string(six))
	}
	// Anything shorter than an hour would hammer a public list for no gain.
	if _, err := service.BlocklistTimer(30 * time.Minute); err == nil {
		t.Error("a half-hourly refresh was accepted")
	}
	if _, err := service.BlocklistTimer(0); err == nil {
		t.Error("a zero interval was accepted")
	}
}
