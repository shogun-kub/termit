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

// updaterFor points the command at a test server, a temporary binary and a restart it can record.
func updaterFor(t *testing.T, server *httptest.Server, exe string, restarted *bool) updater {
	t.Helper()
	return updater{
		restart: func(context.Context) error {
			*restarted = true
			return nil
		},
		exe:       func() (string, error) { return exe, nil },
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
