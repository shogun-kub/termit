package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// stubExecutor fails every command, which is what a non-Debian or under-privileged host looks like.
type stubExecutor struct {
	handler func(cmd install.Cmd) (install.Result, error)
}

func (s stubExecutor) Run(ctx context.Context, cmd install.Cmd) (install.Result, error) {
	if err := ctx.Err(); err != nil {
		return install.Result{}, err
	}
	if s.handler != nil {
		return s.handler(cmd)
	}
	res := install.Result{ExitCode: 127}
	return res, &install.ExitError{Cmd: cmd, Result: res}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newTestApp builds a CLI whose host is fake and whose network always answers 200.
//
// The host is declared as one that cannot run WARP. Left to the machine, these tests would pass on a
// developer's Windows box and fail on a Linux runner that happens to have /dev/net/tun, which is how
// the WARP suite first reached CI broken. A test that needs the runnable path sets warpHost itself.
func newTestApp(t *testing.T) (*application, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return &application{
		stdout:   stdout,
		stderr:   stderr,
		now:      func() time.Time { return time.Unix(0, 0).UTC() },
		exec:     stubExecutor{},
		warpHost: &warp.Host{GOOS: "windows", GOARCH: "amd64"},
		// Tests declare the host: without this, "no arguments" would open the menu when the suite
		// happens to run on a terminal and print usage when it does not.
		interactiveFn: func() bool { return false },
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"newNonce":"https://ca/new-nonce"}`)),
			}, nil
		})},
	}, stdout, stderr
}

func TestDispatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want int
		out  string
	}{
		{"version", []string{"version"}, exitOK, "termit-uv"},
		{"help", []string{"help"}, exitOK, "Usage:"},
		{"long help", []string{"--help"}, exitOK, "Usage:"},
		{"no command", nil, exitUsage, "Usage:"},
		{"unknown", []string{"nope"}, exitUsage, "unknown command"},
		{"warp without a configuration", []string{"warp", "--config", filepath.Join(t.TempDir(), "absent.yaml")}, exitFailure, "uv warp:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, stdout, stderr := newTestApp(t)
			if got := app.run(tc.args); got != tc.want {
				t.Fatalf("run(%v) = %d, want %d", tc.args, got, tc.want)
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, tc.out) {
				t.Errorf("output %q does not contain %q", combined, tc.out)
			}
		})
	}
}

// warpReport runs `uv warp --json` and decodes it, optionally with an application built by the caller.
func warpReport(t *testing.T, app *application, path string) jsonWarp {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	app.stdout, app.stderr = stdout, stderr
	if got := app.run([]string{"warp", "--config", path, "--json"}); got != exitOK {
		t.Fatalf("run = %d, want %d: %s", got, exitOK, stderr.String())
	}
	var report jsonWarp
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout.String())
	}
	return report
}

// tunHost is a host that can run WARP: a Linux machine with a tun device that exists on this one
// (a temp file stands in, because the check is "is there a device node", not "can it carry packets").
func tunHost(t *testing.T) *warp.Host {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return &warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: path}
}

// WARP is on by default, so `uv warp status` has to say "off" clearly when the owner opted out and
// has to say where traffic leaves in every other case — including "nowhere yet", which is what a
// fresh node before the package is installed looks like.
func TestWarpStatusReportsTheMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	cfg.Warp.Mode = config.WarpOff
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	app, stdout, stderr := newTestApp(t)
	if got := app.run([]string{"warp", "--config", path}); got != exitOK {
		t.Fatalf("run = %d, want %d: %s", got, exitOK, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"mode:    off", "egress:  off", "exits directly"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}

	cfg.Warp.Mode = config.WarpAll
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A host that cannot run WARP says so instead of claiming a tunnel that does not exist.
	if report := warpReport(t, mustApp(t), path); report.State != "unavailable" {
		t.Errorf("state = %q, want unavailable", report.State)
	} else if !strings.Contains(report.Detail, "not available here") {
		t.Errorf("detail = %q, want the refusal reason", report.Detail)
	}
}

// mustApp is newTestApp without a caller that wants the buffers.
func mustApp(t *testing.T) *application {
	t.Helper()
	app, _, _ := newTestApp(t)
	return app
}

// A node that has not installed the package yet is a normal state: `uv warp status` reports
// "not-installed" rather than failing, which is what makes it useful on a fresh box.
func TestWarpStatusSaysNotInstalled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	cfg.Warp.Mode = config.WarpAll
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	app := mustApp(t)
	app.warpHost = tunHost(t)
	app.exec = stubExecutor{handler: func(cmd install.Cmd) (install.Result, error) {
		// What the real executor returns when the binary is not on the host: a wrapped exec.Error.
		return install.Result{}, fmt.Errorf("%s: %w", cmd.String(), &exec.Error{Name: cmd.Name, Err: exec.ErrNotFound})
	}}

	report := warpReport(t, app, path)
	if report.State != "not-installed" {
		t.Fatalf("state = %q, want not-installed (detail %q)", report.State, report.Detail)
	}
	if !strings.Contains(report.Detail, "not installed") {
		t.Errorf("detail = %q, want it to say the package is missing", report.Detail)
	}
}

// warp-cli can be installed and connected while the tunnel carries nothing useful; the status has to
// say where traffic actually leaves, not what the client claims.
func TestWarpStatusReportsAFailedProbe(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	cfg.Warp.Mode = config.WarpAll
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	app := mustApp(t)
	app.warpHost = tunHost(t)
	app.exec = stubExecutor{handler: func(cmd install.Cmd) (install.Result, error) {
		switch {
		case len(cmd.Args) == 1 && cmd.Args[0] == "--version":
			return install.Result{Stdout: "warp-cli 2026.1"}, nil
		case len(cmd.Args) == 2 && cmd.Args[1] == "status":
			return install.Result{Stdout: "Status update: Connected"}, nil
		default:
			return install.Result{}, fmt.Errorf("%s: unexpected command", cmd.String())
		}
	}}

	// Nothing listens on the SOCKS port in a test, so the trace fails and the node must not be
	// reported as "on" on the strength of what the client says alone.
	report := warpReport(t, app, path)
	if report.State != "direct" {
		t.Errorf("state = %q, want direct", report.State)
	}
	if !strings.Contains(report.Detail, "probe failed") {
		t.Errorf("detail = %q, want the probe failure", report.Detail)
	}
}

func TestInitRequiresDomain(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if got := app.run([]string{"init", "--config", configPath}); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(stderr.String(), "--domain") {
		t.Errorf("stderr %q does not mention --domain", stderr.String())
	}
}

func TestInitWritesNothingWhenChecksFail(t *testing.T) {
	t.Parallel()
	app, stdout, _ := newTestApp(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if got := app.run([]string{"init", "--domain", "vpn.example.com", "--config", configPath}); got != exitFailure {
		t.Fatalf("exit = %d, want %d\n%s", got, exitFailure, stdout.String())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("a failed preflight wrote %s", configPath)
	}
	if !strings.Contains(stdout.String(), "root") {
		t.Errorf("preflight output %q does not list checks", stdout.String())
	}
}

func TestDoctorJSON(t *testing.T) {
	t.Parallel()
	app, stdout, _ := newTestApp(t)
	// The suite must assert the same thing whether it runs as root (a maintainer on a server) or as a
	// normal user (CI), so the uid is declared rather than read.
	app.euid = func() int { return 1000 }
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	if got := app.run([]string{"doctor", "--json", "--config", configPath}); got != exitFailure {
		t.Fatalf("exit = %d, want %d", got, exitFailure)
	}
	var checks []jsonCheck
	if err := json.Unmarshal(stdout.Bytes(), &checks); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout.String())
	}
	seen := map[string]string{}
	for _, check := range checks {
		seen[check.Name] = check.Status
	}
	if seen["config"] == "" || seen["root"] == "" || seen["os"] == "" {
		t.Errorf("report is missing core checks: %v", seen)
	}
	if seen["root"] != string(install.StatusFail) {
		t.Errorf("root check = %q, want fail on a mock host", seen["root"])
	}
}

func TestSplitList(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"":                   nil,
		"  ":                 nil,
		"dirs":               {"dirs"},
		"dirs, packages":     {"dirs", "packages"},
		"dirs,,xray-binary,": {"dirs", "xray-binary"},
	}
	for input, want := range cases {
		got := splitList(input)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("splitList(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestConfigPathDefault(t *testing.T) {
	t.Setenv(config.PathEnv, "")
	if got := configPathDefault(); got != config.DefaultPath {
		t.Errorf("configPathDefault() = %q, want %q", got, config.DefaultPath)
	}
	t.Setenv(config.PathEnv, "/tmp/termit.yaml")
	if got := configPathDefault(); got != "/tmp/termit.yaml" {
		t.Errorf("configPathDefault() = %q, want the environment override", got)
	}
}

func TestJoinWords(t *testing.T) {
	t.Parallel()
	cases := map[int]string{}
	_ = cases
	if got := joinWords(nil); got != "" {
		t.Errorf("joinWords(nil) = %q", got)
	}
	if got := joinWords([]string{"a"}); got != "a" {
		t.Errorf("joinWords([a]) = %q", got)
	}
	if got := joinWords([]string{"a", "b", "c"}); got != "a, b and c" {
		t.Errorf("joinWords([a b c]) = %q", got)
	}
}

// The documentation has always shown `uv warp status`, so the command accepts it; `uv warp` alone
// stays the short form.
func TestWarpAcceptsTheDocumentedStatusSubcommand(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	cfg.Warp.Mode = config.WarpAll
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	app, stdout, stderr := newTestApp(t)
	if got := app.run([]string{"warp", "status", "--config", path}); got != exitOK {
		t.Fatalf("run = %d, want %d: %s", got, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "egress:") {
		t.Errorf("output %q does not report the egress", stdout.String())
	}
}
