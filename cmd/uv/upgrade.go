package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/version"
)

// releaseBase is where the project publishes its binaries, the same layout uv.sh reads: one directory per
// version, the versioned binary name, and a SHA256SUMS that covers everything in it.
const releaseBase = "https://github.com/shogun-kub/termit/releases/download"

// releaseVersion matches a published release and nothing else: not a pre-release (v0.7.0p1), not a tag
// somebody made for a branch. A node follows releases, which is what the server reports and what the
// checksums are published for.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// maxBinaryBytes caps a download: a release binary is tens of megabytes, and anything past this is not one.
const maxBinaryBytes = 64 << 20

func (a *application) runUpgrade(args []string) int {
	flags := a.newFlagSet("upgrade", `Replaces this node's binary with another published release and lets
the new binary converge the node. Without --to it installs the version the management server reported at
the last sync, never "latest": a node follows its server, not the release feed.

The digest is checked against the release's SHA256SUMS before anything is replaced, the previous binary is
kept beside it, and a failure to converge puts the previous binary back.`)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	to := flags.String("to", "", "release to install, e.g. v0.7.0 (default: the server's version)")
	check := flags.Bool("check", false, "say what a run would do, and download nothing")
	base := flags.String("release-base", releaseBase, "where releases are published; a private mirror is allowed")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := config.Load(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %v\n", err)
		return exitFailure
	}
	target, code := a.upgradeTarget(cfg, *to, *check)
	if code != exitOK || target == "" {
		return code
	}
	if *check {
		fmt.Fprintf(a.stdout, "would install %s over %s\n", target, version.Version)
		return exitOK
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: find the running binary: %v\n", err)
		return exitFailure
	}
	ctx, cancel := a.context()
	defer cancel()
	previous, err := upgradeBinary(ctx, a.http, *base, exe, target, runtime.GOARCH)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(a.stdout, "installed %s (was %s); the previous binary is at %s\n", target, version.Version, previous)

	// The new binary converges the node: it is the one that knows what its own release needs. Running the
	// old code here would converge to the old shape and then call it done.
	if err := a.runConverge(ctx, exe); err != nil {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %s did not converge the node: %v\n", target, err)
		if restoreErr := restoreBinary(previous, exe); restoreErr != nil {
			fmt.Fprintf(a.stderr, "termit-uv upgrade: and the previous binary could not be put back: %v\n", restoreErr)
			return exitFailure
		}
		fmt.Fprintf(a.stderr, "termit-uv upgrade: the previous binary is back in place; nothing else changed\n")
		return exitFailure
	}
	return exitOK
}

// upgradeTarget resolves what the node should install, and reports the reasons it should not install
// anything. It returns the version to install, or an exit code.
func (a *application) upgradeTarget(cfg *config.Config, requested string, check bool) (string, int) {
	target := strings.TrimSpace(requested)
	if target == "" {
		node, err := state.LoadInstalled(cfg.Paths.StatePath())
		switch {
		case errors.Is(err, state.ErrNotInstalled):
			fmt.Fprintln(a.stderr, "termit-uv upgrade: this node is not installed; run init first")
			return "", exitUsage
		case err != nil:
			fmt.Fprintf(a.stderr, "termit-uv upgrade: %v\n", err)
			return "", exitFailure
		}
		if node.ServerVersion == "" {
			fmt.Fprintln(a.stderr, "termit-uv upgrade: the server has not reported its version yet; "+
				"run sync (or name the release with --to)")
			return "", exitUsage
		}
		target = node.ServerVersion
	}
	if !releaseVersion.MatchString(target) {
		fmt.Fprintf(a.stderr, "termit-uv upgrade: %q is not a release; a node follows vX.Y.Z, never a "+
			"pre-release or a branch\n", target)
		return "", exitUsage
	}
	if target == version.Version {
		fmt.Fprintf(a.stdout, "already at %s\n", target)
		return "", exitOK
	}
	if check {
		return target, exitOK
	}
	return target, exitOK
}

// upgradeBinary downloads one release binary, checks it against the release's SHA256SUMS, and swaps it in
// beside the running binary, keeping the old file. It returns the path of the previous binary.
func upgradeBinary(ctx context.Context, client *http.Client, base, exe, ver, arch string) (string, error) {
	asset := fmt.Sprintf("termit-uv_%s_linux_%s", ver, arch)
	sums, err := fetchRelease(ctx, client, base+"/"+ver+"/SHA256SUMS")
	if err != nil {
		return "", fmt.Errorf("read SHA256SUMS: %w", err)
	}
	want, err := checksumFor(string(sums), asset)
	if err != nil {
		return "", err
	}
	body, err := fetchRelease(ctx, client, base+"/"+ver+"/"+asset)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	if got := digestOfBytes(body); got != want {
		return "", fmt.Errorf("%s does not match the release's SHA256SUMS: got %s, want %s", asset, got, want)
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".termit-uv-"+ver+"-")
	if err != nil {
		return "", fmt.Errorf("write beside the running binary: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
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
		if restoreErr := os.Rename(previous, exe); restoreErr != nil {
			return "", fmt.Errorf("install %s: %w; and the previous binary could not be put back: %v", asset, err, restoreErr)
		}
		return "", fmt.Errorf("install %s: %w", asset, err)
	}
	return previous, nil
}

// checksumFor finds one asset's digest in a SHA256SUMS file: "<digest>  <name>" per line, as sha256sum
// writes it.
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

// fetchRelease reads one small file from the release directory.
func fetchRelease(ctx context.Context, client *http.Client, url string) ([]byte, error) {
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
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBinaryBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func digestOfBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// restoreBinary puts the previous binary back.
func restoreBinary(previous, exe string) error {
	if err := os.Remove(exe); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(previous, exe)
}

// runConverge runs the freshly installed binary. It is a seam: production replaces the process with
// `exe init`, and a test says it would have.
func (a *application) runConverge(ctx context.Context, exe string) error {
	if a.convergeFn != nil {
		return a.convergeFn(ctx, exe)
	}
	cmd := exec.CommandContext(ctx, exe, "init", "--config", a.configPath)
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	return cmd.Run()
}
