package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
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
func newTestApp(t *testing.T) (*application, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return &application{
		stdout: stdout,
		stderr: stderr,
		now:    func() time.Time { return time.Unix(0, 0).UTC() },
		exec:   stubExecutor{},
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

// WARP is on by default, so `uv warp status` has to say "off" clearly when the owner opted out and
// has to say where traffic leaves in every other case.
func TestWarpStatusReportsTheMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
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

	// The default is all; on a host that cannot run WARP, the command says so instead of claiming a
	// tunnel that does not exist.
	cfg.Warp.Mode = config.WarpAll
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	app, stdout, stderr = newTestApp(t)
	if got := app.run([]string{"warp", "--config", path, "--json"}); got != exitOK {
		t.Fatalf("run = %d, want %d: %s", got, exitOK, stderr.String())
	}
	var report struct {
		Mode   string `json:"mode"`
		State  string `json:"state"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout.String())
	}
	if report.Mode != config.WarpAll {
		t.Errorf("mode = %q, want %q", report.Mode, config.WarpAll)
	}
	if report.State != "on" && report.State != "unavailable" && report.State != "direct" && report.State != "not-installed" {
		t.Errorf("state = %q, want one of the real answers", report.State)
	}
	if report.State == "unavailable" && !strings.Contains(report.Detail, "not available here") {
		t.Errorf("detail = %q, want the refusal reason", report.Detail)
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
