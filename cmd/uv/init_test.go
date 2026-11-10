package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentKeyFromBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		data    string
		want    string
		wantErr string
	}{
		{name: "a key", data: "abc123", want: "abc123"},
		{name: "surrounding whitespace is a newline from an editor", data: "  abc123\n", want: "abc123"},
		{name: "empty", data: "", wantErr: "empty"},
		{name: "only whitespace", data: " \n\t", wantErr: "empty"},
		{name: "two keys", data: "abc\nxyz", wantErr: "exactly one key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := agentKeyFromBytes([]byte(tc.data))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("agentKeyFromBytes: %v", err)
			}
			if got != tc.want {
				t.Errorf("key = %q, want %q", got, tc.want)
			}
		})
	}
}

// The permission check is POSIX-only in practice: Windows does not model group and other bits, so the
// file tests are skipped there and CI (Linux) covers them.
func skipWithoutPOSIXPermissions(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not model POSIX permission bits")
	}
}

func TestReadAgentKeyFile(t *testing.T) {
	skipWithoutPOSIXPermissions(t)
	path := filepath.Join(t.TempDir(), "agent.key")
	if err := os.WriteFile(path, []byte("abc123\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	key, err := readAgentKeyFile(path)
	if err != nil {
		t.Fatalf("readAgentKeyFile: %v", err)
	}
	if key != "abc123" {
		t.Errorf("key = %q, want abc123", key)
	}
}

func TestReadAgentKeyFileRejectsLoosePermissions(t *testing.T) {
	skipWithoutPOSIXPermissions(t)
	path := filepath.Join(t.TempDir(), "agent.key")
	if err := os.WriteFile(path, []byte("abc123\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := readAgentKeyFile(path)
	if err == nil || !strings.Contains(err.Error(), "readable by group") {
		t.Fatalf("err = %v, want a complaint about the mode", err)
	}
}
