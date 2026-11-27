package main

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/install"
)

// Every check lands on the screen that describes it: the machine, the node on it, or its tunnel.
func TestCheckGroupSplitsTheReport(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"root": groupHost, "os": groupHost, "systemd": groupHost, "disk": groupHost, "apt": groupHost,
		"config": groupNode, "state": groupNode, "support": groupNode, "services": groupNode,
		"certificate": groupNode, "decoy": groupNode, "proxy config": groupNode,
		"transport": groupTunnel, "warp": groupTunnel, "warp-egress": groupTunnel,
	}
	for name, want := range cases {
		if got := checkGroup(name); got != want {
			t.Errorf("%s is in %q, want %q", name, got, want)
		}
	}
}

// The last line answers "is anything wrong" without the reader scanning the table, and it never calls a
// failure "ok with warnings" or the reverse.
func TestVerdictNamesTheWorstThingFound(t *testing.T) {
	t.Parallel()
	ok := install.Check{Status: install.StatusOK}
	warn := install.Check{Status: install.StatusWarn}
	skip := install.Check{Status: install.StatusSkip}
	fail := install.Check{Status: install.StatusFail}

	cases := []struct {
		name   string
		checks []install.Check
		want   string
	}{
		{"all ok", []install.Check{ok}, "ok"},
		{"warnings only", []install.Check{ok, warn}, "ok with warnings"},
		{"a failure", []install.Check{ok, warn, fail}, "problems found"},
		{"nothing measured", []install.Check{skip, skip}, "ok"},
	}
	for _, tc := range cases {
		if got := verdict(tc.checks); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: verdict = %q, want it to start with %q", tc.name, got, tc.want)
		}
	}
}
