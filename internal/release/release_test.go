package release_test

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
)

// releaseServer publishes one version the way the project does: a directory, the asset, and sums.
func releaseServer(t *testing.T, version, body string) *httptest.Server {
	t.Helper()
	asset := release.Asset("termit-uv", version, "amd64")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + version + "/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n", release.Digest([]byte(body)), asset)
		case "/" + version + "/" + asset:
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAssetNameIsTheOneTheReleasePublishes(t *testing.T) {
	t.Parallel()
	if got := release.Asset("termit-uv", "v0.7.0", "arm64"); got != "termit-uv_v0.7.0_linux_arm64" {
		t.Errorf("asset = %q", got)
	}
}

func TestFetchVerifiesTheDigest(t *testing.T) {
	t.Parallel()
	server := releaseServer(t, "v9.9.9", "the new build")
	body, err := release.Fetch(context.Background(), server.Client(), server.URL, "termit-uv", "v9.9.9", "amd64")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != "the new build" {
		t.Errorf("body = %q", body)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			fmt.Fprintf(w, "%s  termit-uv_v9.9.9_linux_amd64\n", strings.Repeat("0", 64))
			return
		}
		fmt.Fprint(w, "the new build")
	}))
	t.Cleanup(bad.Close)
	if _, err := release.Fetch(context.Background(), bad.Client(), bad.URL, "termit-uv", "v9.9.9", "amd64"); err == nil ||
		!strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("err = %v, want a checksum refusal", err)
	}
}

func TestFetchRefusesAnAssetTheSumsDoNotList(t *testing.T) {
	t.Parallel()
	server := releaseServer(t, "v9.9.9", "the new build")
	_, err := release.Fetch(context.Background(), server.Client(), server.URL, "termit-uv", "v9.9.9", "arm64")
	if err == nil || !strings.Contains(err.Error(), "lists no") {
		t.Fatalf("err = %v, want a missing-asset refusal", err)
	}
}

// The swap keeps the previous binary, and a failed second rename leaves the first one in place.
func TestInstallKeepsThePreviousBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-uv")
	if err := os.WriteFile(exe, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	previous, err := release.Install([]byte("the new build"), exe)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	installed, err := os.ReadFile(exe)
	if err != nil || string(installed) != "the new build" {
		t.Fatalf("installed %q (%v)", installed, err)
	}
	kept, err := os.ReadFile(previous)
	if err != nil || string(kept) != "the old build" {
		t.Fatalf("kept %q (%v)", kept, err)
	}
	// Windows has no executable bit; the mode is asserted where it means something.
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(exe); err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Errorf("the installed binary is not executable (err %v)", err)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".termit-release-*")); len(leftovers) != 0 {
		t.Errorf("the install left %v behind", leftovers)
	}
}

func TestRestorePutsThePreviousBinaryBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := filepath.Join(dir, "termit-uv")
	previous := exe + ".prev"
	if err := os.WriteFile(previous, []byte("the old build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(exe, []byte("the new build"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := release.Restore(previous, exe); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	back, err := os.ReadFile(exe)
	if err != nil || string(back) != "the old build" {
		t.Fatalf("restored %q (%v)", back, err)
	}
}

// Newest follows the "latest" redirect and reads the tag it lands on; a redirect that lands on something
// that is not a release is refused rather than installed.
func TestNewestFollowsTheRedirect(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
			return
		}
		fmt.Fprint(w, "the release page")
	}))
	t.Cleanup(server.Close)
	got, err := release.Newest(context.Background(), server.Client(), server.URL+"/latest")
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if got != "v9.9.9" {
		t.Errorf("tag = %q", got)
	}

	odd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/untagged-abc", http.StatusFound)
	}))
	t.Cleanup(odd.Close)
	if _, err := release.Newest(context.Background(), odd.Client(), odd.URL+"/latest"); err == nil {
		t.Error("Newest accepted a tag that is not a release")
	}
}

// A node follows releases only: a pre-release tag, a branch or a bare number is not one.
func TestVersionAcceptsReleasesOnly(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"v0.7.0": true, "v10.0.1": true,
		"v0.7.0p1": false, "0.7.0": false, "latest": false, "v0.7": false, "v0.7.0-dev": false, "main": false,
	}
	for version, want := range cases {
		if got := release.Version.MatchString(version); got != want {
			t.Errorf("%q accepted = %v, want %v", version, got, want)
		}
	}
}
