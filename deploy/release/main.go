// Command release builds the artifacts of one release: four binaries, the node bootstrap script, the
// management server's unit and a SHA256SUMS file that covers them.
//
// It is a maintenance tool, not a shipped binary: `go run ./deploy/release --version v0.1.0`. It exists
// because the release has to be reproducible by a person as well as by CI, and because the naming is a
// contract — the bootstrap script fetches
// `<base>/<version>/termit-uv_<version>_linux_<arch>` and `<base>/<version>/SHA256SUMS`, so a renamed
// asset is a broken installation.
//
// The layout it produces is what `deploy/install/uv.sh` expects:
//
//	dist/v0.1.0/
//	  termit-uv_v0.1.0_linux_amd64
//	  termit-uv_v0.1.0_linux_arm64
//	  termit-mn_v0.1.0_linux_amd64
//	  termit-mn_v0.1.0_linux_arm64
//	  uv.sh
//	  termit-mn.service
//	  SHA256SUMS
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// module is the import path the version is stamped into. It is a constant rather than derived from
// go.mod because a wrong value would silently leave "dev" in every binary.
const module = "github.com/shogun-kub/termit"

// platforms are the architectures the release ships. They match what the installer accepts.
var platforms = []string{"amd64", "arm64"}

// commands are the binaries, keyed by the directory under cmd/.
var commands = []string{"uv", "mn"}

// extraFiles are copied beside the binaries: the installer an operator runs, and the unit an operator
// installs. Both are read from the repository so the release cannot drift from it.
var extraFiles = []struct {
	source string
	name   string
	mode   os.FileMode
}{
	{"deploy/install/uv.sh", "uv.sh", 0o755},
	{"deploy/systemd/termit-mn.service", "termit-mn.service", 0o644},
}

// versionPattern is the shape of a release tag. A version that does not match would produce asset
// names the installer's parser cannot be trusted with, so it is refused rather than sanitised.
var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "release: %v\n", err)
		os.Exit(1)
	}
}

// artifact is one entry of the release directory.
type artifact struct {
	Name string
	Path string
	// Sum is the SHA-256 of the file, hex.
	Sum string
	// Bytes is the size, for the manifest.
	Bytes int64
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	version := flags.String("version", "", "release version, e.g. v0.1.0 (required)")
	out := flags.String("out", "dist", "output directory")
	root := flags.String("root", ".", "repository root (must contain go.mod)")
	goBinary := flags.String("go", "go", "go command to build with")
	commit := flags.String("commit", "", "commit to stamp (default: git rev-parse --short HEAD)")
	date := flags.String("date", "", "build date to stamp (default: now, UTC, RFC 3339)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *version == "" {
		return errors.New("--version is required; the release never guesses one")
	}
	if !versionPattern.MatchString(*version) {
		return fmt.Errorf("version %q is not a release tag like v0.1.0", *version)
	}

	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(absoluteRoot, "go.mod")); err != nil {
		return fmt.Errorf("%s does not look like the repository root: %w", absoluteRoot, err)
	}

	stampCommit := *commit
	if stampCommit == "" {
		stampCommit = gitCommit(absoluteRoot)
	}
	stampDate := *date
	if stampDate == "" {
		stampDate = time.Now().UTC().Format(time.RFC3339)
	}

	directory := filepath.Join(*out, *version)
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("clear %s: %w", directory, err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", directory, err)
	}

	fmt.Fprintf(stdout, "release:  %s\n", *version)
	fmt.Fprintf(stdout, "commit:   %s\n", stampCommit)
	fmt.Fprintf(stdout, "date:     %s\n", stampDate)
	fmt.Fprintf(stdout, "output:   %s\n\n", directory)

	var artifacts []artifact
	for _, arch := range platforms {
		for _, command := range commands {
			name := assetName(command, *version, arch)
			if err := build(*goBinary, absoluteRoot, directory, command, arch, *version, stampCommit, stampDate); err != nil {
				return err
			}
			entry, err := describe(filepath.Join(directory, name))
			if err != nil {
				return err
			}
			artifacts = append(artifacts, entry)
		}
	}
	for _, extra := range extraFiles {
		if err := copyFile(filepath.Join(absoluteRoot, extra.source), filepath.Join(directory, extra.name), extra.mode); err != nil {
			return err
		}
		entry, err := describe(filepath.Join(directory, extra.name))
		if err != nil {
			return err
		}
		artifacts = append(artifacts, entry)
	}

	sums, err := checksums(artifacts)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "SHA256SUMS"), sums, 0o644); err != nil {
		return fmt.Errorf("write SHA256SUMS: %w", err)
	}

	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	for _, entry := range artifacts {
		fmt.Fprintf(stdout, "  %s  %12d  %s\n", entry.Sum[:12], entry.Bytes, entry.Name)
	}
	fmt.Fprintf(stdout, "\nSHA256SUMS written; %d artifacts\n", len(artifacts))
	fmt.Fprintf(stdout, "next: attach %s/* to the %s release, and push the tag\n", directory, *version)
	return nil
}

// assetName is the contract with the installer. It is a function so a test can pin it.
func assetName(command, version, arch string) string {
	return fmt.Sprintf("termit-%s_%s_linux_%s", command, version, arch)
}

// build compiles one binary with the version stamped in. CGO is off: the release is fully static.
func build(goBinary, root, directory, command, arch, version, commit, date string) error {
	output := filepath.Join(directory, assetName(command, version, arch))
	ldflags := strings.Join([]string{
		"-s", "-w",
		"-X", module + "/internal/version.Version=" + version,
		"-X", module + "/internal/version.Commit=" + commit,
		"-X", module + "/internal/version.Date=" + date,
	}, " ")

	cmd := exec.Command(goBinary, "build", "-trimpath", "-ldflags", ldflags, "-o", output, "./cmd/"+command)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	// The toolchain's own output goes to the error message when the build fails; a successful build
	// is silent, which is what a manifest wants.
	if combined, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build %s: %w\n%s", output, err, strings.TrimSpace(string(combined)))
	}
	return nil
}

// describe reads one finished artifact: its size and its digest.
func describe(path string) (artifact, error) {
	file, err := os.Open(path) // #nosec G304 -- the path was built here, not taken from input.
	if err != nil {
		return artifact{}, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	bytes, err := io.Copy(hash, file)
	if err != nil {
		return artifact{}, fmt.Errorf("hash %s: %w", path, err)
	}
	return artifact{Name: filepath.Base(path), Path: path, Sum: hex.EncodeToString(hash.Sum(nil)), Bytes: bytes}, nil
}

// checksums renders the SHA256SUMS body in the format `sha256sum -c` and the bootstrap script both
// read: "<hex>  <name>", one per line, ordered by name.
func checksums(artifacts []artifact) ([]byte, error) {
	ordered := append([]artifact(nil), artifacts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	var body strings.Builder
	for _, entry := range ordered {
		if entry.Sum == "" || entry.Name == "" {
			return nil, fmt.Errorf("artifact %q has no digest", entry.Name)
		}
		fmt.Fprintf(&body, "%s  %s\n", entry.Sum, entry.Name)
	}
	return []byte(body.String()), nil
}

// copyFile copies source to destination with an explicit mode: a release asset an operator downloads
// has to be executable for the script to run, whatever the source checkout had.
func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source) // #nosec G304 -- repository files chosen here.
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) // #nosec G304
	if err != nil {
		return fmt.Errorf("create %s: %w", destination, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s: %w", source, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", destination, err)
	}
	// The mode passed to OpenFile is masked by the umask; set it explicitly so the asset is usable.
	return os.Chmod(destination, mode)
}

// gitCommit is the short HEAD, or "none" when the tool runs outside a repository.
func gitCommit(root string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "none"
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return "none"
	}
	return commit
}
