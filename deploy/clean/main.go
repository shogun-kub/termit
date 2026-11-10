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

// buildCaches hold compiled output: removing them costs time, because the next build recompiles, but
// nothing is downloaded. They are the honest answer to "the disk is full and I still want to work".
var buildCaches = []string{"gocache", "golangci", ".golangci", "lintcache"}

// moduleCaches hold downloaded and extracted modules: removing them costs network, and a build on a
// host without it simply fails. Kept apart from the build caches for that reason.
var moduleCaches = []string{"gomodcache"}

type options struct {
	root        string
	dryRun      bool
	buildCache  bool
	moduleCache bool
	all         bool
	toolchain   bool
	quiet       bool
	stdout      io.Writer
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
	flags.BoolVar(&opts.buildCache, "build-cache", false,
		"also remove the build caches: the next build recompiles, nothing is downloaded")
	flags.BoolVar(&opts.moduleCache, "module-cache", false,
		"also remove the module cache: the next build downloads the modules again")
	flags.BoolVar(&opts.all, "all", false, "also remove every cache (build and modules)")
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
	plan, err := plan(opts.root, opts)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		fmt.Fprintln(opts.stdout, "nothing to remove: the tree is already clean")
		return nil
	}

	exe, _ := os.Executable()
	var freed int64
	var failed []error
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
			// `go run` compiles this command into the build cache and runs it from there, so the running
			// image is the one thing that cannot be deleted: the cache goes, its own binary does not. That
			// is a property of the toolchain, not a failure — say so and leave the rest of the run clean.
			if within(exe, path) {
				if !opts.quiet {
					fmt.Fprintf(opts.stdout, "%-13s %-40s %s\n", "left", filepath.ToSlash(rel),
						"in use: this is the running command; build it outside the cache to remove it")
				}
				continue
			}
			failed = append(failed, fmt.Errorf("%s: %w", rel, err))
			continue
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
	if opts.buildCache || opts.all {
		fmt.Fprintln(opts.stdout, "the next build recompiles; nothing is downloaded")
	}
	if opts.moduleCache || opts.all {
		fmt.Fprintln(opts.stdout, "the next build downloads the modules again")
	}
	if opts.toolchain {
		fmt.Fprintln(opts.stdout, "the Go toolchain is gone: install it under .spike-out/tools before the next build")
	}
	if opts.dryRun {
		fmt.Fprintln(opts.stdout, "nothing was changed: --dry-run")
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	return nil
}

// within reports whether path is the running executable or one of its parents.
func within(exe, path string) bool {
	if exe == "" {
		return false
	}
	exe, err := filepath.Abs(exe)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(path, exe)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// plan lists the paths to remove, deepest last: an artifact directory goes as a whole.
func plan(root string, opts options) ([]string, error) {
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

	// A set, because two patterns can match one file: `coverage.out` is both `*.out` and `coverage.*`,
	// and removing it twice would count its size twice in the report.
	planned := map[string]struct{}{}
	add := func(path string) { planned[path] = struct{}{} }
	for _, name := range artifacts {
		if path := filepath.Join(root, name); exists(path) {
			add(path)
		}
	}
	for _, pattern := range artifactPatterns {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			add(match)
		}
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
				if opts.toolchain {
					add(filepath.Join(scratch, name))
				}
			case scratchKeep[name]:
				if opts.removeCache(name) {
					add(filepath.Join(scratch, name))
				}
			case entry.IsDir() && exists(filepath.Join(scratch, name, ".git")):
				// A checkout inside the scratch directory is somebody's work, not output.
			default:
				add(filepath.Join(scratch, name))
			}
		}
	}

	out := make([]string, 0, len(planned))
	for path := range planned {
		out = append(out, path)
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

// removeCache answers whether this cache was asked for, and by which flag. The two families are kept
// apart because one costs time and the other costs network.
func (o options) removeCache(name string) bool {
	if contains(buildCaches, name) {
		return o.buildCache || o.all
	}
	if contains(moduleCaches, name) {
		return o.moduleCache || o.all
	}
	return false
}

func contains(list []string, name string) bool {
	for _, item := range list {
		if item == name {
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
