// Command clean removes what a build, a test run or a lab session leaves behind in the working tree.
//
// The tree has three kinds of ignored content and they are not equal: produced artifacts (`dist/`, a
// stray `*.exe`, logs), the scratch directory `.spike-out/`, and the toolchain and caches that live
// inside that scratch directory and cost minutes or a download to rebuild. This command deletes the
// first two and keeps the third unless it is asked otherwise, because "clean up" must not mean
// "re-download Go".
//
// It never guesses: the removable set is exactly the artifact names below plus everything in
// `.spike-out/` that is not on the keep list. Anything it does not recognise is reported and left alone,
// and a directory that contains its own `.git` is never touched.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// scratchDir is the gitignored directory for one-off experiments. Its contents are scratch by
// definition, which is why the keep list is what has to be spelled out.
const scratchDir = ".spike-out"

// artifacts are the build outputs at the top level: the release directory, cross-builds, and the local
// development state `termit-mn init --data-dir ./.dev/mn` writes.
var artifacts = []string{"dist", "build", ".dev"}

// artifactPatterns are top-level files a build leaves behind.
var artifactPatterns = []string{"*.exe", "*.test", "*.out", "coverage.*"}

// scratchKeep is what survives inside the scratch directory: the toolchain, the caches that make the
// next build fast, the lab's own notes and token, and the two small directories that hold source or a
// public key rather than output.
var scratchKeep = map[string]bool{
	"tools":          true, // the portable Go toolchain
	"gocache":        true, // build cache
	"gomodcache":     true, // module cache
	"gopath":         true, // installed tools, including golangci-lint
	"golangci":       true, // the linter's own cache
	".golangci":      true,
	"lintcache":      true,
	"lab.md":         true, // lab coordinates: never in the repository, never regenerable
	"mn-admin.token": true, // the lab's admin credential
	"ssh":            true, // the lab's public key
	"shotgen":        true, // a source file, not output
}

// caches are kept by default and removed by --all: they are big and merely slow to rebuild.
var caches = []string{"gocache", "gomodcache", "gopath", "golangci", ".golangci", "lintcache"}

type options struct {
	root      string
	dryRun    bool
	all       bool
	toolchain bool
	quiet     bool
	stdout    io.Writer
	// remove is os.RemoveAll in production; tests swap it when they need to see the order.
	remove func(string) error
	// stat is os.Lstat in production; it keeps the size report out of tests.
	size func(string) int64
}

func main() {
	root, err := findRoot(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "clean: %v\n", err)
		os.Exit(1)
	}
	opts := options{root: root, stdout: os.Stdout, remove: os.RemoveAll, size: treeSize}
	flags := flag.NewFlagSet("clean", flag.ExitOnError)
	flags.BoolVar(&opts.dryRun, "dry-run", false, "report what would be removed and change nothing")
	flags.BoolVar(&opts.all, "all", false, "also remove the build caches (the next build starts cold)")
	flags.BoolVar(&opts.toolchain, "toolchain", false, "also remove .spike-out/tools; Go must be installed again")
	flags.BoolVar(&opts.quiet, "quiet", false, "print the summary only")
	_ = flags.Parse(os.Args[1:])

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "clean: %v\n", err)
		os.Exit(1)
	}
}

// run removes the planned paths and reports what it did.
func run(opts options) error {
	plan, err := plan(opts.root, opts.all, opts.toolchain)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		fmt.Fprintln(opts.stdout, "nothing to remove: the tree is already clean")
		return nil
	}

	var freed int64
	for _, path := range plan {
		rel, err := filepath.Rel(opts.root, path)
		if err != nil {
			return err
		}
		size := opts.size(path)
		verb := "removed"
		if opts.dryRun {
			verb = "would remove"
		} else if err := opts.remove(path); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		freed += size
		if !opts.quiet {
			fmt.Fprintf(opts.stdout, "%-13s %-40s %s\n", verb, filepath.ToSlash(rel), humanSize(size))
		}
	}
	verb := "freed"
	if opts.dryRun {
		verb = "would free"
	}
	fmt.Fprintf(opts.stdout, "%d path(s), %s %s\n", len(plan), humanSize(freed), verb)
	if opts.dryRun {
		fmt.Fprintln(opts.stdout, "nothing was changed: --dry-run")
	}
	return nil
}

// plan lists the paths to remove, deepest last: an artifact directory goes as a whole.
func plan(root string, all, toolchain bool) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	// A repository without .git is not this repository: refuse rather than delete by directory name
	// wherever the command happens to be pointed.
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil, fmt.Errorf("%s is not a git checkout", root)
	}

	var out []string
	for _, name := range artifacts {
		if path := filepath.Join(root, name); exists(path) {
			out = append(out, path)
		}
	}
	for _, pattern := range artifactPatterns {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, err
		}
		out = append(out, matches...)
	}

	scratch := filepath.Join(root, scratchDir)
	entries, err := os.ReadDir(scratch)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Nothing to clean in the scratch directory; the artifacts above still count.
	case err != nil:
		return nil, err
	default:
		for _, entry := range entries {
			name := entry.Name()
			switch {
			case name == "tools":
				if toolchain {
					out = append(out, filepath.Join(scratch, name))
				}
			case scratchKeep[name]:
				if all && isCache(name) {
					out = append(out, filepath.Join(scratch, name))
				}
			case entry.IsDir() && exists(filepath.Join(scratch, name, ".git")):
				// A checkout inside the scratch directory is somebody's work, not output.
			default:
				out = append(out, filepath.Join(scratch, name))
			}
		}
	}

	sort.Strings(out)
	return out, nil
}

// findRoot walks up from start to the module the command belongs to.
func findRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if exists(filepath.Join(dir, "go.mod")) && exists(filepath.Join(dir, ".git")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod and .git above the working directory")
		}
		dir = parent
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isCache(name string) bool {
	for _, cache := range caches {
		if name == cache {
			return true
		}
	}
	return false
}

// treeSize is what will be freed; a file it cannot read counts as nothing rather than failing the run.
func treeSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil //nolint:nilerr // an unreadable subtree is not a reason to refuse to clean
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return strings.TrimSpace(fmt.Sprintf("%.1f PB", value))
}
