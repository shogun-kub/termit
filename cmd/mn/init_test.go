package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/config"
)

// initIn runs `termit-mn init` against a fresh temporary directory and returns its output.
func initIn(t *testing.T, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	full := append([]string{"--config", configPath, "--domain", "mn.example.net", "--data-dir", dir}, args...)
	out := &strings.Builder{}
	code := runInit(full, out, out)
	return out.String(), code
}

var prefixLine = regexp.MustCompile(`(?m)^api prefix:\s+(/\S+)$`)

// A fresh install must not answer on a path anybody can guess: /api/v1 is the first thing a scanner
// tries, and a prefix that is always the same is not a prefix, it is a name.
func TestInitGeneratesARandomAPIPrefix(t *testing.T) {
	t.Parallel()
	out, code := initIn(t)
	if code != exitOK {
		t.Fatalf("init = %d:\n%s", code, out)
	}
	match := prefixLine.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no api prefix in the output:\n%s", out)
	}
	prefix := match[1]
	if prefix == config.DefaultAPIPrefix {
		t.Errorf("the fresh install kept the default prefix %q", prefix)
	}
	if !regexp.MustCompile(`^/[a-z2-9]{12}$`).MatchString(prefix) {
		t.Errorf("prefix %q is not one random path segment", prefix)
	}
	if !strings.Contains(out, "--api-prefix") && !strings.Contains(out, "termit-uv enroll --mn https://mn.example.net"+prefix) {
		t.Errorf("the output does not tell the operator where the prefix belongs:\n%s", out)
	}
}

// Two installs must not share a prefix, or the randomness is decorative.
func TestInitPrefixesDiffer(t *testing.T) {
	t.Parallel()
	first, code := initIn(t)
	if code != exitOK {
		t.Fatalf("first init = %d:\n%s", code, first)
	}
	second, code := initIn(t)
	if code != exitOK {
		t.Fatalf("second init = %d:\n%s", code, second)
	}
	a := prefixLine.FindStringSubmatch(first)[1]
	b := prefixLine.FindStringSubmatch(second)[1]
	if a == b {
		t.Errorf("two installs share the prefix %q", a)
	}
}

func TestInitTakesAnExplicitPrefix(t *testing.T) {
	t.Parallel()
	out, code := initIn(t, "--api-prefix", "/api/v1")
	if code != exitOK {
		t.Fatalf("init = %d:\n%s", code, out)
	}
	if got := prefixLine.FindStringSubmatch(out)[1]; got != "/api/v1" {
		t.Errorf("prefix = %q, want the one that was asked for", got)
	}
}

func TestInitRefusesAnInvalidPrefix(t *testing.T) {
	t.Parallel()
	out, code := initIn(t, "--api-prefix", "api")
	if code == exitOK {
		t.Fatalf("a prefix without a leading slash was accepted:\n%s", out)
	}
}

// Moving the prefix on an installed server would cut every node off, so init refuses instead of
// rewriting it behind the operator's back.
func TestInitRefusesToMoveAnInstalledPrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	out := &strings.Builder{}
	if code := runInit([]string{"--config", configPath, "--domain", "mn.example.net", "--data-dir", dir,
		"--api-prefix", "/first"}, out, out); code != exitOK {
		t.Fatalf("first init = %d:\n%s", code, out.String())
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	out.Reset()
	if code := runInit([]string{"--config", configPath, "--api-prefix", "/second"}, out, out); code == exitOK {
		t.Fatalf("init moved the prefix of an installed server:\n%s", out.String())
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("the refused init rewrote the configuration")
	}
}

// Re-running init on an installed server keeps the prefix it already has: the nodes encode it in their
// MN URL.
func TestInitKeepsThePrefixOnARerun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	out := &strings.Builder{}
	if code := runInit([]string{"--config", configPath, "--domain", "mn.example.net", "--data-dir", dir,
		"--api-prefix", "/chosen1"}, out, out); code != exitOK {
		t.Fatalf("first init = %d:\n%s", code, out.String())
	}
	out.Reset()
	if code := runInit([]string{"--config", configPath}, out, out); code != exitOK {
		t.Fatalf("second init = %d:\n%s", code, out.String())
	}
	if got := prefixLine.FindStringSubmatch(out.String())[1]; got != "/chosen1" {
		t.Errorf("prefix = %q after a re-run, want the original", got)
	}
}

func TestRandomAPIPrefixShape(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		prefix, err := randomAPIPrefix()
		if err != nil {
			t.Fatalf("randomAPIPrefix: %v", err)
		}
		if !regexp.MustCompile(`^/[abcdefghijkmnopqrstuvwxyz23456789]{12}$`).MatchString(prefix) {
			t.Fatalf("prefix %q is outside the alphabet", prefix)
		}
		if seen[prefix] {
			t.Fatalf("prefix %q repeated", prefix)
		}
		seen[prefix] = true
	}
}
