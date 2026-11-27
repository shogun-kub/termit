package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/release"
)

// updateServer answers the release API and one release directory, so an update test never reaches GitHub.
func updateServer(t *testing.T, tag, body string) *httptest.Server {
	t.Helper()
	asset := release.Asset("termit-mn", tag, "amd64")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/releases/tag/"):
			fmt.Fprint(w, "the release page")
		case r.URL.Path == "/"+tag+"/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n", release.Digest([]byte(body)), asset)
		case r.URL.Path == "/"+tag+"/"+asset:
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// updaterFor points the command at a test server, a temporary binary, a backup beside it and a restart it
// can record.
func updaterFor(t *testing.T, server *httptest.Server, exe string, restarted *bool) updater {
	t.Helper()
	return updater{
		restart: func(context.Context) error {
			*restarted = true
			return nil
		},
		exe: func() (string, error) { return exe, nil },
		backup: func(context.Context, string) (string, error) {
			return filepath.Join(filepath.Dir(exe), "backup.tar.gz"), nil
		},
		latestURL: server.URL + "/latest",
		client:    server.Client(),
	}
}

func TestUpdateCheckInstallsNothing(t *testing.T) {
	t.Parallel()
	server := updateServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-mn")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	restarted := false
	var out, errOut strings.Builder
	code := updaterFor(t, server, exe, &restarted).run([]string{"--release-base", server.URL}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "run `termit-mn update --apply`") {
		t.Errorf("output = %q, want the next step", out.String())
	}
	if restarted {
		t.Error("a check restarted the server")
	}
	if kept, _ := os.ReadFile(exe); string(kept) != "the old build" {
		t.Error("a check replaced the binary")
	}
}

func TestUpdateApplyReplacesAndRestarts(t *testing.T) {
	t.Parallel()
	server := updateServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-mn")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	restarted := false
	var out, errOut strings.Builder
	code := updaterFor(t, server, exe, &restarted).run([]string{"--apply", "--release-base", server.URL}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if !restarted {
		t.Error("the update did not restart the server")
	}
	installed, err := os.ReadFile(exe)
	if err != nil || string(installed) != "the new build" {
		t.Fatalf("installed %q (%v)", installed, err)
	}
	if kept, _ := os.ReadFile(exe + ".prev"); string(kept) != "the old build" {
		t.Error("the previous binary was not kept")
	}
}

// A server that would not restart must not be left with a binary nothing has run: the previous one goes
// back, and the message says what to do.
func TestUpdateRollsBackWhenTheRestartFails(t *testing.T) {
	t.Parallel()
	server := updateServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-mn")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	u := updaterFor(t, server, exe, new(bool))
	u.restart = func(context.Context) error { return fmt.Errorf("unit not found") }
	var out, errOut strings.Builder
	code := u.run([]string{"--apply", "--release-base", server.URL}, &out, &errOut)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "previous binary is back in place") {
		t.Errorf("stderr = %q, want the rollback", errOut.String())
	}
	back, err := os.ReadFile(exe)
	if err != nil || string(back) != "the old build" {
		t.Fatalf("after the rollback the binary is %q (%v)", back, err)
	}
}

// The registry is captured before the binary is swapped: a migration the old build cannot read is only
// recoverable from an archive written before it, so the order is the whole point of the test.
func TestUpdateBacksUpBeforeItSwapsTheBinary(t *testing.T) {
	t.Parallel()
	server := updateServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-mn")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	restarted := false
	u := updaterFor(t, server, exe, &restarted)
	archive := filepath.Join(dir, "backup.tar.gz")
	sawBinary := ""
	u.backup = func(_ context.Context, configPath string) (string, error) {
		if configPath != defaultConfigPath {
			t.Errorf("backup config = %q, want the default path", configPath)
		}
		kept, err := os.ReadFile(exe)
		if err != nil {
			t.Fatalf("read the binary during the backup: %v", err)
		}
		sawBinary = string(kept)
		return archive, nil
	}
	var out, errOut strings.Builder
	if code := u.run([]string{"--apply", "--release-base", server.URL}, &out, &errOut); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	if sawBinary != "the old build" {
		t.Errorf("the backup ran with %q in place, want the old build", sawBinary)
	}
	if !strings.Contains(out.String(), "backup:    "+archive) {
		t.Errorf("output = %q, want the archive path", out.String())
	}
	if !restarted {
		t.Error("the update did not restart the server")
	}
}

// A backup that cannot be written stops the update before anything is replaced: taking it is the only
// reason a failed migration is recoverable, so without it there is nothing to gain from proceeding.
func TestUpdateRefusesWithoutABackup(t *testing.T) {
	t.Parallel()
	server := updateServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-mn")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	restarted := false
	u := updaterFor(t, server, exe, &restarted)
	u.backup = func(context.Context, string) (string, error) { return "", fmt.Errorf("no space left") }
	var out, errOut strings.Builder
	code := u.run([]string{"--apply", "--release-base", server.URL}, &out, &errOut)
	if code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if restarted {
		t.Error("the server was restarted without a backup")
	}
	if kept, _ := os.ReadFile(exe); string(kept) != "the old build" {
		t.Error("the binary was replaced without a backup")
	}
	if !strings.Contains(errOut.String(), "nothing was changed") {
		t.Errorf("stderr = %q, want the reassurance", errOut.String())
	}
}

func TestUpdateRefusesContradictoryFlags(t *testing.T) {
	t.Parallel()
	server := updateServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-mn")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	restarted := false
	var out, errOut strings.Builder
	code := updaterFor(t, server, exe, &restarted).run([]string{"--check", "--apply"}, &out, &errOut)
	if code != exitUsage {
		t.Fatalf("exit = %d, want a usage error for the contradictory flags", code)
	}
}
