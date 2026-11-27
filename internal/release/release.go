// Package release is the one place that knows how this project publishes its binaries: a directory per
// version under a base URL, an asset named after the component, the version and the architecture, and a
// SHA256SUMS file that covers everything in the directory.
//
// Both binaries use it: the node to take the version its server names, the server to take the newest
// release. Neither trusts a byte that is not in the sums.
package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// BaseURL is where the project publishes its releases. A caller may replace it with a mirror.
const BaseURL = "https://github.com/shogun-kub/termit/releases/download"

// LatestURL is the page that redirects to the newest published release. It is a redirect rather than the
// JSON API on purpose: the API refuses an unauthenticated caller quickly (403 on a shared address) and
// needs a token for any real use, while this redirect is the same one uv.sh follows and works wherever
// github.com works. GitHub does not point it at a pre-release or a draft, which is the rule a binary wants.
const LatestURL = "https://github.com/shogun-kub/termit/releases/latest"

// MaxBytes caps one download: a release binary is tens of megabytes, and anything past this is not one.
const MaxBytes = 64 << 20

// Version matches a published release and nothing else: not a pre-release (v0.7.0p1), not a branch tag.
var Version = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Asset is the file name one component publishes for a version and architecture.
func Asset(component, version, arch string) string {
	return fmt.Sprintf("%s_%s_linux_%s", component, version, arch)
}

// Fetch downloads one asset and returns it only if its digest is the one the release's SHA256SUMS lists.
func Fetch(ctx context.Context, client *http.Client, base, component, version, arch string) ([]byte, error) {
	asset := Asset(component, version, arch)
	sums, err := get(ctx, client, base+"/"+version+"/SHA256SUMS")
	if err != nil {
		return nil, fmt.Errorf("read SHA256SUMS: %w", err)
	}
	want, err := checksumFor(string(sums), asset)
	if err != nil {
		return nil, err
	}
	body, err := get(ctx, client, base+"/"+version+"/"+asset)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	if got := Digest(body); got != want {
		return nil, fmt.Errorf("%s does not match the release's SHA256SUMS: got %s, want %s", asset, got, want)
	}
	return body, nil
}

// Newest follows the "latest" redirect and reads the tag it lands on. The URL is a parameter so a test (and
// a mirror that speaks the same shape) can answer instead of GitHub.
func Newest(ctx context.Context, client *http.Client, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "text/html")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", url, response.Status)
	}
	tag := path.Base(response.Request.URL.Path)
	if !Version.MatchString(tag) {
		return "", fmt.Errorf("%s points at %q, which is not a release", url, tag)
	}
	return tag, nil
}

// Install puts a verified binary beside the running one, keeping the previous file as "<exe>.prev". It
// returns that path, which is what a caller restores when the new binary does not work.
func Install(data []byte, exe string) (string, error) {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".termit-release-")
	if err != nil {
		return "", fmt.Errorf("write beside the running binary: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", fmt.Errorf("make %s executable: %w", tmp.Name(), err)
	}

	previous := exe + ".prev"
	if err := os.Rename(exe, previous); err != nil {
		return "", fmt.Errorf("keep the running binary: %w", err)
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		if restoreErr := Restore(previous, exe); restoreErr != nil {
			return "", fmt.Errorf("install the new binary: %w; and the previous one could not be put back: %v", err, restoreErr)
		}
		return "", fmt.Errorf("install the new binary: %w", err)
	}
	return previous, nil
}

// Restore puts the previous binary back where the running one lives.
func Restore(previous, exe string) error {
	if err := os.Remove(exe); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(previous, exe)
}

// Digest is the hex SHA-256 of data, the form SHA256SUMS uses.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// checksumFor finds one asset's digest in a SHA256SUMS file: "<digest>  <name>" per line, as sha256sum
// writes it. A leading "*" marks a binary-mode line and is not part of the name.
func checksumFor(sums, asset string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("the release's SHA256SUMS lists no %s", asset)
}

// get reads one small file from the release directory.
func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}
