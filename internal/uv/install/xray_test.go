package install

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
)

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

// buildZip returns an archive with the given entries.
func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("zip: create %s: %v", entry.name, err)
		}
		if _, err := file.Write([]byte(entry.body)); err != nil {
			t.Fatalf("zip: write %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip: close: %v", err)
	}
	return buf.Bytes()
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestLookupPin(t *testing.T) {
	t.Parallel()
	pin, err := lookupPin("26.11.9", "amd64")
	if err != nil {
		t.Fatalf("lookupPin: %v", err)
	}
	if pin.Name != "Xray-linux-64.zip" {
		t.Errorf("Name = %q", pin.Name)
	}
	if pin.SHA256 != "b651f7e1d9966ee2c1b1ee1bf8b52c72771824d3065dce96bbc8bcc19ed95f82" {
		t.Errorf("amd64 digest changed: %q", pin.SHA256)
	}

	if _, err := lookupPin("1.2.3", "amd64"); err == nil || !strings.Contains(err.Error(), "not a pinned release") {
		t.Errorf("unknown version error = %v", err)
	}
	if _, err := lookupPin("26.11.9", "386"); err == nil {
		t.Error("unsupported architecture was accepted")
	}
	if got := PinnedVersions(); len(got) == 0 || got[0] != "26.11.9" {
		t.Errorf("PinnedVersions = %v", got)
	}
}

func TestArchiveExpectationHonoursOverride(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Xray.Version = "26.11.9"
	cfg.Xray.SHA256 = map[string]string{"amd64": strings.Repeat("c", 64)}

	pin, err := archiveExpectation(cfg, "amd64")
	if err != nil {
		t.Fatalf("archiveExpectation: %v", err)
	}
	if pin.SHA256 != strings.Repeat("c", 64) {
		t.Errorf("digest = %q, want the override", pin.SHA256)
	}
	if pin.Name != "Xray-linux-64.zip" {
		t.Errorf("archive name = %q", pin.Name)
	}
}

func TestArchiveSource(t *testing.T) {
	t.Parallel()
	const (
		version = "26.11.9"
		name    = "Xray-linux-64.zip"
	)
	remote := []struct{ base, want string }{
		{"https://example.com/rel", "https://example.com/rel/v26.11.9/Xray-linux-64.zip"},
		{"https://example.com/rel/", "https://example.com/rel/v26.11.9/Xray-linux-64.zip"},
	}
	for _, tc := range remote {
		got, isRemote, err := archiveSource(tc.base, version, name)
		if err != nil {
			t.Errorf("archiveSource(%q): %v", tc.base, err)
			continue
		}
		if got != tc.want || !isRemote {
			t.Errorf("archiveSource(%q) = %q, %v; want %q, true", tc.base, got, isRemote, tc.want)
		}
	}

	// Local bases are filesystem paths, so the expected value follows the host's separators.
	local := []string{"/srv/xray", "file:///srv/xray", filepath.Join(t.TempDir(), "release")}
	for _, base := range local {
		got, isRemote, err := archiveSource(base, version, name)
		if err != nil {
			t.Errorf("archiveSource(%q): %v", base, err)
			continue
		}
		dir := strings.TrimPrefix(base, "file://")
		want := filepath.Join(dir, "v"+version, name)
		if got != want || isRemote {
			t.Errorf("archiveSource(%q) = %q, %v; want %q, false", base, got, isRemote, want)
		}
	}
}

func TestParseXrayVersion(t *testing.T) {
	t.Parallel()
	version, err := parseXrayVersion("Xray 26.11.9 (Xray, Penetrates Everything.) 676eaf6 (go1.27.3 linux/amd64)\n")
	if err != nil || version != "26.11.9" {
		t.Fatalf("parseXrayVersion = %q, %v", version, err)
	}
	if _, err := parseXrayVersion("Xray"); err == nil {
		t.Error("truncated output accepted")
	}
	if _, err := parseXrayVersion(""); err == nil {
		t.Error("empty output accepted")
	}
}

func TestExtractXray(t *testing.T) {
	t.Parallel()
	archive := buildZip(t, []zipEntry{
		{name: "geoip.dat", body: "geo"},
		{name: "xray", body: "#!/bin/sh\necho hi\n", mode: 0o755},
		{name: "README.md", body: "readme"},
	})
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "Xray-linux-64.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	outDir := filepath.Join(dir, "bin")
	installed, err := extractXray(archivePath, outDir)
	if err != nil {
		t.Fatalf("extractXray: %v", err)
	}
	if installed != filepath.Join(outDir, "xray") {
		t.Errorf("installed = %q", installed)
	}
	body, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(body), "#!/bin/sh") {
		t.Errorf("binary content = %q", body)
	}
	if entries, err := os.ReadDir(outDir); err != nil || len(entries) != 1 {
		t.Errorf("output directory holds %d entries (%v), want only the binary", len(entries), err)
	}
}

func TestExtractXrayMissingEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.zip")
	archive := buildZip(t, []zipEntry{{name: "README.md", body: "readme"}})
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := extractXray(archivePath, filepath.Join(dir, "bin")); err == nil ||
		!strings.Contains(err.Error(), `no "xray" entry`) {
		t.Fatalf("error = %v, want a missing entry error", err)
	}
}

func TestCopyVerifiedRejectsWrongDigest(t *testing.T) {
	t.Parallel()
	dst := filepath.Join(t.TempDir(), "archive.zip")
	err := copyVerified(dst, strings.NewReader("bytes"), strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error = %v, want a digest mismatch", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("a rejected download left %s behind", dst)
	}
}

func TestFetchArchiveFromDirectoryAndCache(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Xray.Version = "26.11.9"
	archive := buildZip(t, []zipEntry{{name: "xray", body: "binary", mode: 0o755}})

	base := t.TempDir()
	releaseDir := filepath.Join(base, "v26.11.9")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	archivePath := filepath.Join(releaseDir, "Xray-linux-64.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg.Xray.DownloadBase = base
	cfg.Xray.SHA256 = map[string]string{"amd64": digestOf(archive)}

	cache := filepath.Join(t.TempDir(), "cache")
	first, err := fetchArchive(context.Background(), http.DefaultClient, cfg, "amd64", cache)
	if err != nil {
		t.Fatalf("fetchArchive: %v", err)
	}
	if first != filepath.Join(cache, "Xray-linux-64.zip") {
		t.Errorf("cached at %q", first)
	}

	// With the source gone, a second call must be served from the cache.
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	second, err := fetchArchive(context.Background(), http.DefaultClient, cfg, "amd64", cache)
	if err != nil {
		t.Fatalf("second fetchArchive: %v", err)
	}
	if second != first {
		t.Errorf("second fetch returned %q", second)
	}
}

func TestFetchArchiveRejectsWrongDigest(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Xray.Version = "26.11.9"
	archive := buildZip(t, []zipEntry{{name: "xray", body: "binary", mode: 0o755}})

	base := t.TempDir()
	releaseDir := filepath.Join(base, "v26.11.9")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "Xray-linux-64.zip"), archive, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg.Xray.DownloadBase = base
	cfg.Xray.SHA256 = map[string]string{"amd64": strings.Repeat("d", 64)}

	_, err := fetchArchive(context.Background(), http.DefaultClient, cfg, "amd64", filepath.Join(t.TempDir(), "cache"))
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error = %v, want a digest mismatch", err)
	}
}
