package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/release"
	"github.com/shogun-kub/termit/internal/termui"
)

// releaseServer publishes one release the way GitHub does: a directory per version, the versioned binary
// name, and a SHA256SUMS that covers it. body is what the "binary" contains, so a test can tell the swap
// worked by reading the file.
func releaseServer(t *testing.T, ver, body string) *httptest.Server {
	t.Helper()
	asset := "termit-uv_" + ver + "_linux_amd64"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + ver + "/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n%s  termit-mn_%s_linux_amd64\n", release.Digest([]byte(body)), asset,
				strings.Repeat("a", 64), ver)
		case "/" + ver + "/" + asset:
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestUpgradeReplacesTheBinaryAndKeepsTheOld(t *testing.T) {
	t.Parallel()
	server := releaseServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-uv")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	previous, err := upgradeBinary(context.Background(), server.Client(), server.URL, exe, "v9.9.9", "amd64")
	if err != nil {
		t.Fatalf("upgradeBinary: %v", err)
	}
	installed, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(installed) != "the new build" {
		t.Errorf("installed %q, want the release's bytes", installed)
	}
	// Windows has no executable bit; the mode is asserted where it means something.
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(exe); err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("the installed binary is not executable (err %v)", err)
		}
	}
	kept, err := os.ReadFile(previous)
	if err != nil {
		t.Fatalf("read the previous binary: %v", err)
	}
	if string(kept) != "the old build" {
		t.Errorf("kept %q, want the previous binary", kept)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".termit-uv-*")); len(matches) != 0 {
		t.Errorf("the download left %v behind", matches)
	}
}

// A digest that does not match is the one thing that must never reach /usr/bin.
func TestUpgradeRefusesABadChecksum(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			fmt.Fprintf(w, "%s  termit-uv_v9.9.9_linux_amd64\n", strings.Repeat("0", 64))
			return
		}
		fmt.Fprint(w, "the new build")
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-uv")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := upgradeBinary(context.Background(), server.Client(), server.URL, exe, "v9.9.9", "amd64")
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v, want a checksum refusal", err)
	}
	kept, readErr := os.ReadFile(exe)
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if string(kept) != "the old build" {
		t.Errorf("a refused upgrade replaced the binary with %q", kept)
	}
}

func TestUpgradeRefusesAReleaseWithoutTheAsset(t *testing.T) {
	t.Parallel()
	server := releaseServer(t, "v9.9.9", "the new build")
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-uv")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := upgradeBinary(context.Background(), server.Client(), server.URL, exe, "v9.9.9", "arm64")
	if err == nil || !strings.Contains(err.Error(), "lists no") {
		t.Fatalf("err = %v, want a missing-asset refusal", err)
	}
}

// A node follows releases: a pre-release tag or a branch is refused before anything is fetched.
func TestUpgradeOnlyFollowsReleases(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"v0.7.0":     true,
		"v0.7.0p1":   false,
		"0.7.0":      false,
		"latest":     false,
		"v0.7":       false,
		"v0.7.0-dev": false,
	}
	for version, want := range cases {
		if got := release.Version.MatchString(version); got != want {
			t.Errorf("%q accepted = %v, want %v", version, got, want)
		}
	}
}

// The menu offers nothing where the command would find nothing: no server version, a pre-release, or the
// same build.
func TestUpgradeOfferSaysWhenThereIsNothingToDo(t *testing.T) {
	t.Parallel()
	app, _, configPath := optionsFixture(t, "")
	app.configPath = configPath

	entry := entryNamed(t, app, "upgrade this node")
	plan, err := entry.plan(termui.New(app.in, &strings.Builder{}, termui.Style{}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan != nil {
		t.Errorf("a node whose server never reported a version planned %q", plan)
	}
}

// The new binary has to converge the configuration the operator named: without the path it treats an
// installed node as a fresh machine and asks for a domain that was answered long ago. The lab found this
// on the first real upgrade, and the rollback that followed is what kept the node serving.
func TestConvergeRunsInitOnTheNamedConfiguration(t *testing.T) {
	t.Parallel()
	got := convergeArgs("/etc/termit/uv/config.yaml")
	if strings.Join(got, " ") != "init --config /etc/termit/uv/config.yaml" {
		t.Errorf("converge args = %v", got)
	}
	if plain := convergeArgs(""); strings.Join(plain, " ") != "init" {
		t.Errorf("converge args without a path = %v", plain)
	}
}
