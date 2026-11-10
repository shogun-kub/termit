package main

import (
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
)

func TestSummarizeStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state []string
		want  string
	}{
		{"an empty mesh says so", nil, "none"},
		{"one node", []string{"active"}, "1 active"},
		{"counts keep the lifecycle order", []string{"blocked", "active", "active", "removed"}, "2 active, 1 blocked, 1 removed"},
		{"unavailable comes before blocked", []string{"blocked", "unavailable"}, "1 unavailable, 1 blocked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nodes := make([]admin.NodeView, 0, len(tc.state))
			for _, state := range tc.state {
				nodes = append(nodes, admin.NodeView{State: state})
			}
			if got := summarizeStates(nodes); got != tc.want {
				t.Errorf("summarizeStates = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHumanInterval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{24 * time.Hour, "24h"},
		{90 * time.Minute, "1h30m"},
		{30 * time.Minute, "30m"},
		{45 * time.Second, "1m"},
	} {
		if got := humanInterval(tc.in); got != tc.want {
			t.Errorf("humanInterval(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCheckTime(t *testing.T) {
	t.Parallel()
	if got := checkTime(nil); got != "-" {
		t.Errorf("a missing time = %q, want -", got)
	}
	zero := time.Time{}
	if got := checkTime(&zero); got != "-" {
		t.Errorf("a zero time = %q, want -", got)
	}
	at := time.Date(2026, 11, 10, 9, 5, 0, 0, time.FixedZone("MSK", 3*60*60))
	if got := checkTime(&at); got != "2026-11-10 06:05" {
		t.Errorf("checkTime = %q, want the UTC minute", got)
	}
}
