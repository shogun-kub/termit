package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
)

// fakeFileInfo is the minimum os.FileInfo the checks need.
type fakeFileInfo struct{ dir bool }

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }
func (f fakeFileInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}

// healthy is a fake host that passes every check.
func healthyChecks(t *testing.T, exec *fakeExecutor, cfg *config.Config) PreflightOptions {
	t.Helper()
	acmeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"newNonce":"https://ca/new-nonce"}`))
	}))
	t.Cleanup(acmeServer.Close)
	cfg.ACME.Directory = acmeServer.URL + "/dir"

	return PreflightOptions{
		Config: cfg,
		Exec:   exec,
		Arch:   "amd64",
		EUID:   func() int { return 0 },
		ReadFile: func(name string) ([]byte, error) {
			switch name {
			case "/etc/os-release":
				return []byte("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\n"), nil
			case "/proc/meminfo":
				return []byte("MemTotal:        2048000 kB\nMemFree: 1000 kB\n"), nil
			}
			return nil, os.ErrNotExist
		},
		Stat: func(name string) (os.FileInfo, error) {
			switch name {
			case "/run/systemd/system", "/dev/net/tun":
				return fakeFileInfo{dir: name == "/run/systemd/system"}, nil
			}
			return nil, os.ErrNotExist
		},
		DiskFree:   func(string) (uint64, error) { return 8 << 30, nil },
		LookupHost: func(context.Context, string) ([]string, error) { return []string{"203.0.113.7"}, nil },
	}
}

func statuses(report PreflightReport) map[string]CheckStatus {
	out := make(map[string]CheckStatus, len(report.Checks))
	for _, check := range report.Checks {
		out[check.Name] = check.Status
	}
	return out
}

func TestPreflightHealthyHost(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	exec := &fakeExecutor{handler: func(cmd Cmd) (Result, error) {
		switch cmd.Name {
		case "timedatectl":
			return okResult("yes\n")
		case "ss":
			return okResult("")
		case "apt-get":
			return okResult("apt 3.0.16 (amd64)\n")
		}
		return failedResult(cmd, 127, "not found")
	}}

	report := RunPreflight(context.Background(), healthyChecks(t, exec, cfg))
	got := statuses(report)

	want := map[string]CheckStatus{
		"root":    StatusOK,
		"os":      StatusOK,
		"systemd": StatusOK,
		"arch":    StatusOK,
		"clock":   StatusOK,
		"ports":   StatusOK,
		"dns":     StatusWarn, // a public address is not one of this host's own
		"acme":    StatusOK,
		"tun":     StatusOK,
		"disk":    StatusOK,
		"memory":  StatusOK,
		"apt":     StatusOK,
	}
	for name, wantStatus := range want {
		if got[name] != wantStatus {
			t.Errorf("check %s = %q, want %q", name, got[name], wantStatus)
		}
	}
	if !report.OK() {
		t.Errorf("healthy host reported as not installable: %+v", report.Failures())
	}
}

func TestPreflightRejectsNonDebian(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	opts := healthyChecks(t, &fakeExecutor{handler: func(cmd Cmd) (Result, error) { return okResult("") }}, cfg)
	opts.ReadFile = func(name string) ([]byte, error) {
		if name == "/etc/os-release" {
			return []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), nil
		}
		return nil, os.ErrNotExist
	}

	report := RunPreflight(context.Background(), opts)
	if report.OK() {
		t.Fatal("Ubuntu was accepted as an install target")
	}
	if got := statuses(report)["os"]; got != StatusFail {
		t.Errorf("os check = %q, want fail", got)
	}
}

func TestPreflightWarnsOnDebian12(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	opts := healthyChecks(t, &fakeExecutor{handler: func(cmd Cmd) (Result, error) { return okResult("yes") }}, cfg)
	opts.ReadFile = func(name string) ([]byte, error) {
		switch name {
		case "/etc/os-release":
			return []byte("ID=debian\nVERSION_ID=\"12\"\n"), nil
		case "/proc/meminfo":
			return []byte("MemTotal: 2048000 kB\n"), nil
		}
		return nil, os.ErrNotExist
	}

	report := RunPreflight(context.Background(), opts)
	if !report.OK() {
		t.Fatalf("Debian 12 should be installable with a warning: %+v", report.Failures())
	}
	if got := statuses(report)["os"]; got != StatusWarn {
		t.Errorf("os check = %q, want warn", got)
	}
}

func TestPreflightFailsOnPortConflict(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	exec := &fakeExecutor{handler: func(cmd Cmd) (Result, error) {
		switch cmd.Name {
		case "ss":
			return okResult("LISTEN 0 511 0.0.0.0:443 0.0.0.0:* users:((\"apache2\",pid=42,fd=6))\n")
		}
		return okResult("yes")
	}}

	report := RunPreflight(context.Background(), healthyChecks(t, exec, cfg))
	if report.OK() {
		t.Fatal("a conflicting listener was accepted")
	}
	var detail string
	for _, check := range report.Checks {
		if check.Name == "ports" {
			detail = check.Detail
		}
	}
	if !strings.Contains(detail, "apache2") {
		t.Errorf("ports detail = %q, want the owning process named", detail)
	}
}

// WARP is on by default, so a container without TUN is the normal case, not a broken host: the
// preflight warns, the install refuses WARP with a reason, and the node runs direct.
func TestPreflightWarnsWhenWarpCannotRun(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	cfg.Warp.Mode = config.WarpAll
	opts := healthyChecks(t, &fakeExecutor{handler: func(cmd Cmd) (Result, error) { return okResult("yes") }}, cfg)
	opts.Stat = func(name string) (os.FileInfo, error) {
		if name == "/run/systemd/system" {
			return fakeFileInfo{dir: true}, nil
		}
		return nil, os.ErrNotExist // /dev/net/tun is absent
	}

	report := RunPreflight(context.Background(), opts)
	if got := statuses(report)["tun"]; got != StatusWarn {
		t.Errorf("tun check = %q, want warn", got)
	}
	if !report.OK() {
		t.Error("a host without TUN is a working node and must pass the preflight")
	}
}

func TestParseListeners(t *testing.T) {
	t.Parallel()
	output := strings.Join([]string{
		`LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=1,fd=6))`,
		`LISTEN 0 128 [::]:443 [::]:* users:(("nginx",pid=1,fd=7))`,
		`LISTEN 0 128 127.0.0.1:8443 0.0.0.0:*`,
		`ESTAB 0 0 127.0.0.1:22 127.0.0.1:1234`,
		`garbage`,
	}, "\n")

	got := parseListeners(output)
	if len(got) != 3 {
		t.Fatalf("parsed %d listeners, want 3: %+v", len(got), got)
	}
	if got[0].Port != 80 || got[0].Process != "nginx" {
		t.Errorf("first listener = %+v", got[0])
	}
	if got[1].Port != 443 || got[1].Process != "nginx" {
		t.Errorf("second listener = %+v", got[1])
	}
	if got[2].Port != 8443 || got[2].Process != "" {
		t.Errorf("third listener = %+v", got[2])
	}
}

func TestParseOSRelease(t *testing.T) {
	t.Parallel()
	values := parseOSRelease([]byte("# comment\nID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME='Debian 13'\nbroken\n"))
	if values["ID"] != "debian" {
		t.Errorf("ID = %q", values["ID"])
	}
	if values["VERSION_ID"] != "13" {
		t.Errorf("VERSION_ID = %q", values["VERSION_ID"])
	}
	if values["PRETTY_NAME"] != "Debian 13" {
		t.Errorf("PRETTY_NAME = %q", values["PRETTY_NAME"])
	}
}

func TestParseMemTotal(t *testing.T) {
	t.Parallel()
	total, ok := parseMemTotal([]byte("MemTotal:        2048000 kB\n"))
	if !ok || total != 2048000*1024 {
		t.Errorf("parseMemTotal = %d, %v", total, ok)
	}
	if _, ok := parseMemTotal([]byte("nothing here\n")); ok {
		t.Error("parseMemTotal accepted garbage")
	}
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	cases := map[uint64]string{
		512:     "512 B",
		2048:    "2.0 KiB",
		1 << 30: "1.0 GiB",
	}
	for input, want := range cases {
		if got := humanBytes(input); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", input, got, want)
		}
	}
}
