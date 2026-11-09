package install

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shogun-kub/termit/internal/uv/config"
)

const (
	// xrayEntryName is the binary's name inside the release archive.
	xrayEntryName = "xray"
	// maxArchiveBytes bounds what is downloaded and what is extracted from an archive. The 26.11.9
	// amd64 archive is 35 MiB; anything far above that is a mirror serving the wrong file.
	maxArchiveBytes = 128 << 20
)

// archivePin is one release archive: its file name and the SHA-256 of its bytes.
type archivePin struct {
	Name   string
	SHA256 string
}

// releasePin pins the archives of one Xray-core release per GOARCH. The digests come from the
// release's .dgst files and are the reason a mirror cannot substitute a different binary.
type releasePin struct {
	Version  string
	Archives map[string]archivePin
}

// pinnedReleases is the table the installer trusts. Adding a release means adding both digests,
// taken from https://github.com/XTLS/Xray-core/releases/<tag>/<asset>.dgst.
var pinnedReleases = map[string]releasePin{
	"26.11.9": {
		Version: "26.11.9",
		Archives: map[string]archivePin{
			"amd64": {Name: "Xray-linux-64.zip", SHA256: "b651f7e1d9966ee2c1b1ee1bf8b52c72771824d3065dce96bbc8bcc19ed95f82"},
			"arm64": {Name: "Xray-linux-arm64-v8a.zip", SHA256: "64ed6858982a7f5eb85d88b811089063bd4f17561f4c8f44e0f1dc8e6f6430be"},
		},
	},
}

// PinnedVersions lists the release versions this build can verify.
func PinnedVersions() []string {
	versions := make([]string, 0, len(pinnedReleases))
	for version := range pinnedReleases {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
}

// lookupPin returns the archive for a release and architecture.
func lookupPin(version, arch string) (archivePin, error) {
	release, ok := pinnedReleases[version]
	if !ok {
		return archivePin{}, fmt.Errorf(
			"xray %s is not a pinned release (known: %s); pin its archive digest in internal/uv/install or set xray.sha256",
			version, strings.Join(PinnedVersions(), ", "))
	}
	pin, ok := release.Archives[arch]
	if !ok {
		return archivePin{}, fmt.Errorf("xray %s has no pinned archive for %s", version, arch)
	}
	return pin, nil
}

// archiveExpectation resolves the archive name and expected digest, honouring a per-arch override
// from the configuration.
func archiveExpectation(cfg *config.Config, arch string) (archivePin, error) {
	pin, err := lookupPin(cfg.Xray.Version, arch)
	if err != nil {
		return archivePin{}, err
	}
	if override, ok := cfg.XrayArchiveDigest(arch); ok {
		pin.SHA256 = override
	}
	return pin, nil
}

// archiveSource resolves where the archive is read from. An HTTP(S) base is a release directory; a
// file URL or an absolute path is a local directory with the same layout.
func archiveSource(base, version, name string) (string, bool, error) {
	if filepath.IsAbs(base) || strings.HasPrefix(base, "/") {
		return filepath.Join(base, "v"+version, name), false, nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", false, fmt.Errorf("xray.download_base %q: %w", base, err)
	}
	if u.Scheme == "file" {
		return filepath.Join(u.Path, "v"+version, name), false, nil
	}
	return strings.TrimSuffix(base, "/") + "/v" + version + "/" + name, true, nil
}

// fetchArchive makes sure a verified copy of the archive is in the cache and returns its path.
// A cached copy with the right digest is reused, so a resumed install does not download 35 MiB
// again.
func fetchArchive(ctx context.Context, client *http.Client, cfg *config.Config, arch, cacheDir string) (string, error) {
	pin, err := archiveExpectation(cfg, arch)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("xray: create cache directory: %w", err)
	}
	dst := filepath.Join(cacheDir, pin.Name)

	if digest, err := fileSHA256(dst); err == nil {
		if digest == pin.SHA256 {
			return dst, nil
		}
		// A stale or truncated cache entry is replaced rather than trusted.
		if err := os.Remove(dst); err != nil {
			return "", fmt.Errorf("xray: remove stale cache entry %s: %w", dst, err)
		}
	}

	source, remote, err := archiveSource(cfg.Xray.DownloadBase, cfg.Xray.Version, pin.Name)
	if err != nil {
		return "", err
	}
	if remote {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return "", fmt.Errorf("xray: build request for %s: %w", source, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("xray: download %s: %w", source, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("xray: download %s: %s", source, resp.Status)
		}
		if err := copyVerified(dst, resp.Body, pin.SHA256); err != nil {
			return "", err
		}
		return dst, nil
	}

	file, err := os.Open(source) // #nosec G304 -- the path is built from configuration.
	if err != nil {
		return "", fmt.Errorf("xray: open local archive %s: %w", source, err)
	}
	defer func() { _ = file.Close() }()
	if err := copyVerified(dst, file, pin.SHA256); err != nil {
		return "", err
	}
	return dst, nil
}

// copyVerified writes src to dst atomically and refuses the result if its digest is not want.
func copyVerified(dst string, src io.Reader, want string) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".part*")
	if err != nil {
		return fmt.Errorf("xray: create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(src, maxArchiveBytes+1))
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("xray: write archive: %w", err)
	}
	if written > maxArchiveBytes {
		_ = tmp.Close()
		return fmt.Errorf("xray: archive is larger than %d MiB", maxArchiveBytes>>20)
	}
	got := hex.EncodeToString(digest.Sum(nil))
	if got != want {
		_ = tmp.Close()
		return fmt.Errorf("xray: archive digest mismatch: got %s, expected %s", got, want)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("xray: sync archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("xray: close archive: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("xray: rename archive: %w", err)
	}
	return nil
}

// extractXray writes the archive's single binary to dstDir/xray and returns its path.
func extractXray(archivePath, dstDir string) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("xray: open archive %s: %w", archivePath, err)
	}
	defer func() { _ = reader.Close() }()

	var entry *zip.File
	var names []string
	for _, file := range reader.File {
		names = append(names, file.Name)
		if file.Name == xrayEntryName {
			entry = file
			break
		}
	}
	if entry == nil {
		return "", fmt.Errorf("xray: archive has no %q entry (has %s)", xrayEntryName, strings.Join(names, ", "))
	}
	if entry.UncompressedSize64 > maxArchiveBytes {
		return "", fmt.Errorf("xray: %q in the archive is larger than %d MiB", xrayEntryName, maxArchiveBytes>>20)
	}

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", fmt.Errorf("xray: create %s: %w", dstDir, err)
	}
	src, err := entry.Open()
	if err != nil {
		return "", fmt.Errorf("xray: open %q in the archive: %w", xrayEntryName, err)
	}
	defer func() { _ = src.Close() }()

	dst := filepath.Join(dstDir, xrayEntryName)
	tmp, err := os.CreateTemp(dstDir, ".xray.part*")
	if err != nil {
		return "", fmt.Errorf("xray: create temporary file in %s: %w", dstDir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, io.LimitReader(src, maxArchiveBytes)); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("xray: extract binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("xray: chmod binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("xray: sync binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("xray: close binary: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", fmt.Errorf("xray: install binary: %w", err)
	}
	return dst, nil
}

// binaryVersion asks the installed core to identify itself.
func binaryVersion(ctx context.Context, exec Executor, binary string) (string, error) {
	res, err := exec.Run(ctx, Cmd{Name: binary, Args: []string{"version"}})
	if err != nil {
		return "", fmt.Errorf("xray: run %s version: %w", binary, err)
	}
	version, err := parseXrayVersion(res.Stdout)
	if err != nil {
		return "", err
	}
	return version, nil
}

// parseXrayVersion reads the first line, "Xray 26.11.9 (Xray, Penetrates Everything.) …".
func parseXrayVersion(output string) (string, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "Xray" {
		return "", fmt.Errorf("xray: unexpected version output %q", firstLine(line))
	}
	return fields[1], nil
}

func fileSHA256(filename string) (string, error) {
	file, err := os.Open(filename) // #nosec G304 -- the caller passes a path it built.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
