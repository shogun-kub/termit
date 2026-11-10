package main

import (
	"strings"
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

func TestHumanUptime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "0m"},
		{12 * time.Minute, "12m"},
		{2*time.Hour + 14*time.Minute, "2h14m"},
		{3*24*time.Hour + 5*time.Hour, "3d5h"},
		{-time.Minute, "0m"},
	} {
		if got := humanUptime(tc.in); got != tc.want {
			t.Errorf("humanUptime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{4 << 20, "4.0 MiB"},
		{3 << 30, "3.0 GiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDescribeProber(t *testing.T) {
	t.Parallel()
	if got := describeProber(true, "/usr/lib/termit/bin/xray"); got != "ready (/usr/lib/termit/bin/xray)" {
		t.Errorf("a ready prober = %q", got)
	}
	if got := describeProber(true, ""); got != "ready" {
		t.Errorf("a ready prober without a path = %q", got)
	}
	// The line that matters: a broken prober must not read as a quiet mesh.
	got := describeProber(false, "prober.xray_binary is not set")
	if !strings.Contains(got, "unknown") || !strings.Contains(got, "prober.xray_binary is not set") {
		t.Errorf("an unavailable prober = %q, want the reason and the consequence", got)
	}
	if !strings.Contains(describeProber(false, ""), "no probe worker") {
		t.Error("an unavailable prober without a reason does not say so")
	}
}

func TestDescribeQueue(t *testing.T) {
	t.Parallel()
	next := time.Date(2026, 11, 11, 12, 34, 0, 0, time.UTC)
	late := time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC)

	idle := admin.ServerState{}
	idle.Queue.NextDueAt = &next
	if got := describeQueue(idle); got != "0 due, next 2026-11-11 12:34 UTC" {
		t.Errorf("an empty queue = %q", got)
	}

	backlog := admin.ServerState{}
	backlog.Queue.Due = 3
	backlog.Queue.OldestDueAt = &late
	backlog.Queue.OldestDueSeconds = int64(2 * time.Hour / time.Second)
	backlog.Queue.NextDueAt = &next
	if got := describeQueue(backlog); got != "3 due, oldest 2h0m late, next 2026-11-11 12:34 UTC" {
		t.Errorf("a backlog = %q", got)
	}

	// Due with no timestamp: enrolled and never checked. It must not claim a lateness it cannot know.
	fresh := admin.ServerState{}
	fresh.Queue.Due = 1
	if got := describeQueue(fresh); got != "1 due (never checked), nothing scheduled" {
		t.Errorf("a never-checked queue = %q", got)
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
