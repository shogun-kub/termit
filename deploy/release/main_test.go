package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetNamesAreTheInstallerContract(t *testing.T) {
	t.Parallel()
	// deploy/install/uv.sh builds this name from --version and the architecture; a rename here is a
	// broken installation, so the shape is pinned.
	for _, tc := range []struct {
		command, version, arch, want string
	}{
		{"uv", "v0.1.0", "amd64", "termit-uv_v0.1.0_linux_amd64"},
		{"uv", "v0.1.0", "arm64", "termit-uv_v0.1.0_linux_arm64"},
		{"mn", "v1.2.3", "amd64", "termit-mn_v1.2.3_linux_amd64"},
		{"mn", "v1.2.3-rc1", "arm64", "termit-mn_v1.2.3-rc1_linux_arm64"},
	} {
		if got := assetName(tc.command, tc.version, tc.arch); got != tc.want {
			t.Errorf("assetName(%q, %q, %q) = %q, want %q", tc.command, tc.version, tc.arch, got, tc.want)
		}
	}
}

func TestVersionPattern(t *testing.T) {
	t.Parallel()
	accepted := []string{"v0.1.0", "v1.2.3", "v10.20.30", "v0.1.0-rc1", "v0.1.0-beta.2"}
	for _, version := range accepted {
		if !versionPattern.MatchString(version) {
			t.Errorf("%q was refused", version)
		}
	}
	// A version that does not match would produce asset names the installer's parser cannot be
	// trusted with; refusing is the only safe answer.
	refused := []string{"", "0.1.0", "v1", "v1.2", "latest", "v1.2.3/../../x", "v1.2.3 ", "V1.2.3"}
	for _, version := range refused {
		if versionPattern.MatchString(version) {
			t.Errorf("%q was accepted", version)
		}
	}
}

func TestChecksumsAreSortedAndParseable(t *testing.T) {
	t.Parallel()
	artifacts := []artifact{
		{Name: "termit-uv_v1_linux_arm64", Sum: strings.Repeat("b", 64)},
		{Name: "SHA256SUMS", Sum: strings.Repeat("c", 64)},
		{Name: "uv.sh", Sum: strings.Repeat("a", 64)},
	}
	body, err := checksums(artifacts)
	if err != nil {
		t.Fatalf("checksums: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	want := []string{
		strings.Repeat("c", 64) + "  SHA256SUMS",
		strings.Repeat("b", 64) + "  termit-uv_v1_linux_arm64",
		strings.Repeat("a", 64) + "  uv.sh",
	}
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d:\n%s", len(lines), len(want), body)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestChecksumsRefuseAnEmptyDigest(t *testing.T) {
	t.Parallel()
	if _, err := checksums([]artifact{{Name: "x"}}); err == nil {
		t.Fatal("an artifact without a digest was accepted")
	}
}

func TestDescribeHashesTheFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(path, []byte("termit\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	entry, err := describe(path)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	// SHA-256 of the seven bytes "termit\n".
	const want = "7d0dd55e4eaa1164440dd4fe04db59fab63deed908bafc541d8fae5081b2cbae"
	if entry.Sum != want {
		t.Errorf("digest = %q, want %q", entry.Sum, want)
	}
	if entry.Bytes != 7 {
		t.Errorf("bytes = %d, want 7", entry.Bytes)
	}
	if entry.Name != "asset" {
		t.Errorf("name = %q, want asset", entry.Name)
	}
}

func TestRunRefusesAMissingVersion(t *testing.T) {
	t.Parallel()
	var out, errs bytes.Buffer
	if err := run(nil, &out, &errs); err == nil {
		t.Fatal("run succeeded without --version")
	}
	if err := run([]string{"--version", "0.1.0"}, &out, &errs); err == nil {
		t.Fatal("run accepted a version that is not a tag")
	}
}

func TestRunRefusesADirectoryWithoutGoMod(t *testing.T) {
	t.Parallel()
	var out, errs bytes.Buffer
	err := run([]string{"--version", "v0.1.0", "--root", t.TempDir()}, &out, &errs)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("run = %v, want a refusal about the root", err)
	}
}
