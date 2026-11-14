package main

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFlagsFirst(t *testing.T) {
	t.Parallel()
	boolean := map[string]bool{"--force": true, "-f": true}
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flags before the argument are left alone",
			in:   []string{"--config", "a.yaml", "archive.gz"},
			want: []string{"--config", "a.yaml", "archive.gz"},
		},
		{
			name: "a flag after the argument is moved in front of it",
			in:   []string{"archive.gz", "--config", "a.yaml"},
			want: []string{"--config", "a.yaml", "archive.gz"},
		},
		{
			name: "a boolean flag takes no value",
			in:   []string{"archive.gz", "--force"},
			want: []string{"--force", "archive.gz"},
		},
		{
			name: "an equals form keeps its value",
			in:   []string{"archive.gz", "--config=a.yaml"},
			want: []string{"--config=a.yaml", "archive.gz"},
		},
		{
			name: "several arguments keep their order",
			in:   []string{"one", "--force", "two", "--config", "a.yaml"},
			want: []string{"--force", "--config", "a.yaml", "one", "two"},
		},
		{
			name: "no arguments at all",
			in:   []string{"--force"},
			want: []string{"--force"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := flagsFirst(tc.in, boolean); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("flagsFirst(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Every admin command takes the token from a file in a real deployment, and the natural way to write it
// is after the node: `uv show node1 --token-file /path`. Two subcommands parsed their flags in a way that
// made that "one node id or domain is required" instead.
func TestFlagsMayFollowTheArgument(t *testing.T) {
	t.Parallel()
	missing := t.TempDir() + "/no-such-token"
	for _, run := range []func([]string, io.Writer, io.Writer) int{runUVShow, runInviteRevoke} {
		stderr := &strings.Builder{}
		code := run([]string{"node1.example.com", "--token-file", missing}, io.Discard, stderr)
		if code == exitUsage && strings.Contains(stderr.String(), "is required") {
			t.Errorf("the argument was lost: %s", stderr.String())
		}
		if !strings.Contains(stderr.String(), "token") {
			t.Errorf("stderr = %q, want the token file to be the problem", stderr.String())
		}
	}
}

// An override is a value that differs from the configuration: init seeds the settings table, so "stored"
// alone must not print the note.
func TestRuntimeInterval(t *testing.T) {
	t.Parallel()
	configured := 24 * time.Hour
	cases := []struct {
		name    string
		stored  string
		want    time.Duration
		overrid bool
	}{
		{"unset", "", configured, false},
		{"same as the file", "24h0m0s", configured, false},
		{"different", "1h", time.Hour, true},
		{"unreadable", "sometimes", configured, false},
	}
	for _, tc := range cases {
		got, overridden := runtimeInterval(configured, tc.stored)
		if got != tc.want || overridden != tc.overrid {
			t.Errorf("%s: runtimeInterval(%q) = %v, %v; want %v, %v",
				tc.name, tc.stored, got, overridden, tc.want, tc.overrid)
		}
	}
}
