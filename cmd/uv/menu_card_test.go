package main

import (
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/termui"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
)

// The card's lines are what an owner reads to answer "what does this node do": the transports it serves,
// where traffic leaves, and whether the build is behind the server's.
func TestCardLinesNameWhatTheNodeDoes(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if got := servesLine(cfg); got != "TLS/443 only" {
		t.Errorf("serves = %q, want the TLS profile alone", got)
	}
	cfg.Hysteria = &config.Hysteria{}
	if got := servesLine(cfg); !strings.Contains(got, "UDP/443") {
		t.Errorf("serves = %q, want the QUIC port", got)
	}

	cfg.Warp.Mode = config.WarpAll
	cfg.Warp.Transport = config.WarpTransportUsque
	if got := egressLine(cfg); got != "all via usque" {
		t.Errorf("egress = %q, want the mode and the transport", got)
	}
	cfg.Warp.Mode = config.WarpOff
	if got := egressLine(cfg); got != "off (direct)" {
		t.Errorf("egress = %q, want a mode that leaves directly", got)
	}
}

func TestServerLineNotesWhenTheNodeIsBehind(t *testing.T) {
	t.Parallel()
	if got := serverLine(version.Version); got != version.Version {
		t.Errorf("server = %q, want the same build named plainly", got)
	}
	if got := serverLine("v9.9.9"); !strings.Contains(got, "behind") {
		t.Errorf("server = %q, want it to say the node is behind", got)
	}
}

func TestPeersLineCountsDegraded(t *testing.T) {
	t.Parallel()
	peers := []state.Peer{{ID: "a"}, {ID: "b", Degraded: true}, {ID: "c"}}
	if got := peersLine(peers); got != "3 (1 degraded)" {
		t.Errorf("peers = %q, want the count and the degraded one", got)
	}
	if got := peersLine(peers[:1]); got != "1" {
		t.Errorf("peers = %q, want a plain count when nothing is degraded", got)
	}
}

// The card's sync line is read at a glance, so an age says "2h ago" and not "2h0m0s ago".
func TestHumanAgeReadsLikeAPerson(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		then time.Time
		want string
	}{
		{now.Add(-40 * time.Second), "40s ago"},
		{now.Add(-90 * time.Second), "2m ago"},
		{now.Add(-2 * time.Hour), "2h ago"},
		{now.Add(-150 * time.Minute), "2h30m ago"},
		{now.Add(5 * time.Minute), "in 5m"},
	} {
		if got := humanAge(now, tc.then); got != tc.want {
			t.Errorf("humanAge(%v) = %q, want %q", tc.then, got, tc.want)
		}
	}
}

// The header is drawn on every screen, so it must be the card and not a status report: local values only,
// and a mesh that is not there yet is not an error.
func TestMenuHeaderDrawsTheCard(t *testing.T) {
	t.Parallel()
	app, _, configPath := optionsFixture(t, "")
	app.configPath = configPath
	out := &strings.Builder{}
	app.menuHeader(termui.New(strings.NewReader(""), out, termui.Style{}))
	rendered := out.String()
	for _, want := range []string{"node.example.com", "serves", "TLS/443", "egress", "identity"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the card does not show %q:\n%s", want, rendered)
		}
	}
}
