package atomicfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/atomicfile"
)

// skipWithoutPOSIXPermissions is for the assertions about mode bits: Windows reports neither the file's
// 0600 nor the directory's 0750, and the product only runs on Linux.
func skipWithoutPOSIXPermissions(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not model POSIX permission bits")
	}
}

// This package writes every credential and configuration file both binaries keep, so the properties worth
// testing are the ones a crash would otherwise expose: the permissions, the replace, and no leftover temp
// file a later backup would pick up.

func TestWriteLeavesTheFileWithTheAskedPermissions(t *testing.T) {
	t.Parallel()
	skipWithoutPOSIXPermissions(t)
	target := filepath.Join(t.TempDir(), "config.yaml")
	if err := atomicfile.Write(target, []byte("domain: a.example\n"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != "domain: a.example\n" {
		t.Errorf("the file holds %q", body)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

func TestWriteCreatesTheDirectory(t *testing.T) {
	t.Parallel()
	skipWithoutPOSIXPermissions(t)
	dir := filepath.Join(t.TempDir(), "state", "uv")
	target := filepath.Join(dir, "agent.key")
	if err := atomicfile.Write(target, []byte("key\n"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat the directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("directory mode = %o, want 0750", got)
	}
}

// Replacing is the point: a reader either sees the old content or the new one, and the old file is gone.
func TestWriteReplacesWhatWasThere(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "master.key")
	if err := atomicfile.Write(target, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := atomicfile.Write(target, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(body) != "new\n" {
		t.Errorf("the file holds %q, want the replacement", body)
	}
}

func TestWriteLeavesNoTemporaryFileBehind(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := atomicfile.Write(filepath.Join(dir, "config.yaml"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Errorf("a temporary file survived: %s", entry.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("%d entries, want only the target", len(entries))
	}
}

// A path that cannot be a file is an error, not a silent success: the caller reports it and stops.
func TestWriteRefusesAPathItCannotUse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the blocker: %v", err)
	}
	if err := atomicfile.Write(filepath.Join(blocker, "config.yaml"), []byte("x\n"), 0o644); err == nil {
		t.Fatal("Write accepted a directory under a file")
	}
}
