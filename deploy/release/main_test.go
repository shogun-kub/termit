package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
)

// The server's installer fetches its probe worker by version, and the node's Go pin is the other half
// of the same decision: a release that moved one and not the other would install two different cores
// on one mesh.
func TestTheServerInstallerPinsTheSameXrayRelease(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("..", "install", "mn.sh"))
	if err != nil {
		t.Fatalf("read mn.sh: %v", err)
	}
	if !strings.Contains(string(body), "PROBER_VERSION="+config.DefaultXrayVersion) {
		t.Errorf("mn.sh does not pin xray %s, which is what the node installs", config.DefaultXrayVersion)
	}
}

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
	accepted := []string{"v0.1.0", "v1.2.3", "v10.20.30", "v0.1.0-rc1", "v0.1.0-beta.2", "v0.3.2p1", "v0.3.2p12"}
	for _, version := range accepted {
		if !versionPattern.MatchString(version) {
			t.Errorf("%q was refused", version)
		}
	}
	// A version that does not match would produce asset names the installer's parser cannot be
	// trusted with; refusing is the only safe answer.
	refused := []string{"", "0.1.0", "v1", "v1.2", "latest", "v1.2.3/../../x", "v1.2.3 ", "V1.2.3", "v0.3.2p", "v0.3.2p0x"}
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

// TestWriteNotesTakesTheSectionForTheVersion: the release body is the changelog's own section, and a
// section that is missing or still marked unreleased must fail the release rather than publish a
// placeholder.
func TestWriteNotesTakesTheSectionForTheVersion(t *testing.T) {
	t.Parallel()
	changelog := "# Changelog\n\n## 0.3.0 — unreleased\n\nnot yet\n\n## 0.2.1 — 2026-11-14\n\nfirst\n\n- added a thing\n\n### Fixed\n\n- fixed a thing\n\n## 0.2.0 — 2026-11-10\n\nolder\n"
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte(changelog), 0o644); err != nil {
		t.Fatalf("write the changelog: %v", err)
	}

	var out bytes.Buffer
	if err := writeNotes(path, "v0.2.1", "", &out); err != nil {
		t.Fatalf("writeNotes: %v", err)
	}
	got := out.String()
	for _, want := range []string{"## 0.2.1 — 2026-11-14", "added a thing", "fixed a thing"} {
		if !strings.Contains(got, want) {
			t.Errorf("the notes are missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"## 0.3.0", "## 0.2.0", "older"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the notes carry %q from another section:\n%s", unwanted, got)
		}
	}

	out.Reset()
	if err := writeNotes(path, "v9.9.9", "", &out); err == nil || !strings.Contains(err.Error(), "no") {
		t.Errorf("a version with no section = %v, want a refusal", err)
	}
	out.Reset()
	if err := writeNotes(path, "v0.3.0", "", &out); err == nil || !strings.Contains(err.Error(), "unreleased") {
		t.Errorf("an unreleased section = %v, want a refusal", err)
	}
	// A pre-release of a version is described by that version's section: the counter would otherwise
	// need a changelog entry per build.
	out.Reset()
	if err := writeNotes(path, "v0.2.1p4", "", &out); err != nil {
		t.Fatalf("a pre-release of 0.2.1 = %v, want the 0.2.1 section", err)
	}
	if !strings.Contains(out.String(), "## 0.2.1 — 2026-11-14") {
		t.Errorf("the pre-release notes are not 0.2.1's:\n%s", out.String())
	}
}

// TestWriteNotesCountsBackToTheLastRelease: a release that follows versions published only as
// pre-releases must talk about all of them, because the person installing it skipped each one.
func TestWriteNotesCountsBackToTheLastRelease(t *testing.T) {
	t.Parallel()
	changelog := "# Changelog\n\n## 0.3.2 — 2026-11-16\n\nsecond\n\n## 0.3.1 — 2026-11-15\n\nfirst patch\n\n" +
		"## 0.3.0 — 2026-11-15\n\nbig one\n\n## 0.2.1 — 2026-11-14\n\nreleased\n\n"
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte(changelog), 0o644); err != nil {
		t.Fatalf("write the changelog: %v", err)
	}

	var out bytes.Buffer
	if err := writeNotes(path, "v0.3.2", "v0.2.1", &out); err != nil {
		t.Fatalf("writeNotes: %v", err)
	}
	got := out.String()
	for _, want := range []string{"## 0.3.2 — 2026-11-16", "second", "## 0.3.1", "first patch", "## 0.3.0", "big one"} {
		if !strings.Contains(got, want) {
			t.Errorf("the notes are missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"## 0.2.1", "released"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the notes carry %q from the version that was actually released:\n%s", unwanted, got)
		}
	}

	// A typo in --since must not silently publish everything, and a --since newer than the release would
	// produce an empty body.
	out.Reset()
	if err := writeNotes(path, "v0.3.2", "v0.2.0", &out); err == nil || !strings.Contains(err.Error(), "cannot count") && !strings.Contains(err.Error(), "no") {
		t.Errorf("a --since with no section = %v, want a refusal", err)
	}
	out.Reset()
	if err := writeNotes(path, "v0.3.2", "v0.3.2", &out); err == nil || !strings.Contains(err.Error(), "not older") {
		t.Errorf("a --since equal to the release = %v, want a refusal", err)
	}
}

// TestExtraFilesExist: every file the release copies beside the binaries has to be in the repository,
// and under the name the manifest will show. A renamed server unit would ship a release in which
// `termit-mn firewall on` refuses forever, because the rollback timer it insists on is not there.
func TestExtraFilesExist(t *testing.T) {
	t.Parallel()
	for _, extra := range extraFiles {
		if _, err := os.Stat(filepath.Join("..", "..", extra.source)); err != nil {
			t.Errorf("extra file %s: %v", extra.source, err)
		}
		if extra.name != filepath.Base(extra.name) {
			t.Errorf("extra file %q is not a base name", extra.name)
		}
	}
}

// TestReleaseRefusesAVersionPastAnAcceptedAdvisory: the exclusion list is a deadline, not a permission.
// A version at or past it must not be cut, and the refusal has to carry the action that clears it -
// otherwise the next person reads "refused" and moves the deadline.
func TestReleaseRefusesAVersionPastAnAcceptedAdvisory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatalf("make deploy: %v", err)
	}
	exclusions := `{"exclusions": [{"id": "GO-1", "module": "example.com/x", "must_be_gone_by": "0.6.0", "action": "bump the module"}]}`
	if err := os.WriteFile(filepath.Join(root, "deploy", "vulnerabilities.json"), []byte(exclusions), 0o600); err != nil {
		t.Fatalf("write exclusions: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"--version", "v0.6.0", "--root", root}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "GO-1") || !strings.Contains(err.Error(), "bump the module") {
		t.Fatalf("a version at the deadline = %v, want a refusal naming the advisory and the action", err)
	}
}
