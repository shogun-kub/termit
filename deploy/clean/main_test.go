package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree builds a working tree with one of everything: artifacts, scratch, the toolchain, caches, the lab
// notes and the two directories that must survive because they are input rather than output.
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, size int) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("%v", err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatalf("%v", err)
		}
	}
	for _, dir := range []string{".git", "cmd/uv", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatalf("%v", err)
		}
	}
	write("go.mod", 32)
	write("cmd/uv/main.go", 16)
	write("dist/v0.2.0/termit-uv", 4096)
	write("build/termit-uv-arm64", 2048)
	write("uv.exe", 1024)
	write("mn.exe", 1024)
	write("coverage.out", 64)
	write(".dev/mn/termit.db", 512)
	write(".spike-out/tools/go/bin/go", 8192)
	write(".spike-out/gocache/cache", 1024)
	write(".spike-out/lab.md", 256)
	write(".spike-out/mn-admin.token", 43)
	write(".spike-out/ssh/lab.pub", 128)
	write(".spike-out/shotgen/main.go", 200)
	write(".spike-out/dist/v0.1.1/termit-mn", 2048)
	write(".spike-out/lab/termit-uv", 1024)
	write(".spike-out/msg-1.txt", 40)
	write(".spike-out/lab-run.log", 100)
	write("captures/decoy-samples/index.html", 64)
	return root
}

func relative(t *testing.T, root string, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("%v", err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func TestPlanRemovesArtifactsAndScratchButKeepsTheToolchain(t *testing.T) {
	t.Parallel()
	root := tree(t)
	paths, err := plan(root, false, false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	got := strings.Join(relative(t, root, paths), " ")

	want := []string{
		".dev", ".spike-out/dist", ".spike-out/lab", ".spike-out/lab-run.log", ".spike-out/msg-1.txt",
		"build", "coverage.out", "dist", "mn.exe", "uv.exe",
	}
	for _, path := range want {
		if !strings.Contains(" "+got+" ", " "+path+" ") {
			t.Errorf("%q is not planned for removal: %s", path, got)
		}
	}
	// The toolchain, the caches, the lab's own notes and the input directories stay.
	for _, path := range []string{
		".spike-out/tools", ".spike-out/gocache", ".spike-out/lab.md", ".spike-out/mn-admin.token",
		".spike-out/ssh", ".spike-out/shotgen", "captures", "cmd", "docs", "go.mod",
	} {
		if strings.Contains(" "+got+" ", " "+path+" ") {
			t.Errorf("%q would be removed: %s", path, got)
		}
	}
}

func TestAllAlsoDropsTheCachesButNotGo(t *testing.T) {
	t.Parallel()
	root := tree(t)
	paths, err := plan(root, true, false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	got := strings.Join(relative(t, root, paths), " ")
	if !strings.Contains(got, ".spike-out/gocache") {
		t.Errorf("--all left the build cache behind: %s", got)
	}
	if strings.Contains(got, ".spike-out/tools") {
		t.Errorf("--all removed the toolchain without --toolchain: %s", got)
	}

	paths, err = plan(root, true, true)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := strings.Join(relative(t, root, paths), " "); !strings.Contains(got, ".spike-out/tools") {
		t.Errorf("--toolchain kept the toolchain: %s", got)
	}
}

// A repository that is not this repository is refused: the command deletes by name, and a name is not an
// identity.
func TestPlanRefusesWhatIsNotACheckout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := plan(dir, false, false); err == nil {
		t.Fatal("a directory without .git was accepted")
	}
}

// The removal itself is idempotent: running it twice is not an error, and the second run finds nothing.
func TestRunRemovesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	root := tree(t)
	out := &strings.Builder{}
	opts := options{root: root, stdout: out, remove: os.RemoveAll, size: treeSize}
	if err := run(opts); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist")); !os.IsNotExist(err) {
		t.Error("dist survived the run")
	}
	if _, err := os.Stat(filepath.Join(root, ".spike-out", "tools", "go", "bin", "go")); err != nil {
		t.Errorf("the toolchain did not survive: %v", err)
	}
	if !strings.Contains(out.String(), "path(s)") {
		t.Errorf("no summary:\n%s", out.String())
	}

	out.Reset()
	if err := run(opts); err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out.String(), "already clean") {
		t.Errorf("the second run did not report a clean tree:\n%s", out.String())
	}
}

// --dry-run is what an operator runs first: it must change nothing.
func TestDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	root := tree(t)
	out := &strings.Builder{}
	opts := options{root: root, dryRun: true, stdout: out, remove: os.RemoveAll, size: treeSize}
	if err := run(opts); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "v0.2.0", "termit-uv")); err != nil {
		t.Errorf("--dry-run removed an artifact: %v", err)
	}
	if !strings.Contains(out.String(), "would remove") || !strings.Contains(out.String(), "--dry-run") {
		t.Errorf("the report does not say nothing was changed:\n%s", out.String())
	}
}

// A checkout that happens to live in the scratch directory is somebody's work.
func TestPlanKeepsNestedCheckouts(t *testing.T) {
	t.Parallel()
	root := tree(t)
	nested := filepath.Join(root, ".spike-out", "vendor-test", ".git")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("%v", err)
	}
	paths, err := plan(root, false, false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := strings.Join(relative(t, root, paths), " "); strings.Contains(got, "vendor-test") {
		t.Errorf("a nested checkout was planned for removal: %s", got)
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()
	cases := map[int64]string{0: "0 B", 999: "999 B", 1024: "1.0 KB", 1536: "1.5 KB", 5 << 20: "5.0 MB"}
	for bytes, want := range cases {
		if got := humanSize(bytes); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", bytes, got, want)
		}
	}
}

func TestFindRootWalksUp(t *testing.T) {
	t.Parallel()
	root := tree(t)
	deep := filepath.Join(root, "cmd", "uv")
	found, err := findRoot(deep)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if found != root {
		t.Errorf("findRoot(%q) = %q, want %q", deep, found, root)
	}
}

// Nothing outside the working tree is ever a target, and the tree itself never is.
func TestRunStaysInsideTheTree(t *testing.T) {
	t.Parallel()
	root := tree(t)
	var seen []string
	opts := options{
		root: root, quiet: true, stdout: io.Discard, size: func(string) int64 { return 0 },
		remove: func(path string) error { seen = append(seen, path); return nil },
	}
	if err := run(opts); err != nil {
		t.Fatalf("%v", err)
	}
	for _, path := range seen {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			t.Errorf("%q is not a path inside the tree", path)
		}
	}
}
