package install

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// TestUsqueAssetURL pins the shape of the download URL. The base used to carry the tag as well and
// archiveSource adds "/v<version>" itself, so the first production switch asked for
// .../download/v4.2.1/v4.2.1/usque_4.2.1_linux_amd64.zip and got a 404.
func TestUsqueAssetURL(t *testing.T) {
	t.Parallel()
	pin, err := usquePin("amd64")
	if err != nil {
		t.Fatalf("usquePin(amd64): %v", err)
	}
	got, remote, err := archiveSource(usqueReleaseBase, usqueVersion, pin.Name)
	if err != nil {
		t.Fatalf("archiveSource: %v", err)
	}
	want := "https://github.com/Diniboy1123/usque/releases/download/v4.2.1/usque_4.2.1_linux_amd64.zip"
	if got != want || !remote {
		t.Errorf("archiveSource = %q, remote %v; want %q, remote true", got, remote, want)
	}
}

// TestUsqueRegisterAcceptsTheTerms pins what the sidecar is told. `usque register` prompts for
// Cloudflare's terms on stdin unless --accept-tos is passed and an install has no stdin, so the first
// production switch stopped at "failed to read user input: EOF"; and the device file has to end up
// readable by the unit's user, which is why the registration is followed by a chown.
func TestUsqueRegisterAcceptsTheTerms(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Warp.Transport = config.WarpTransportUsque
	cfg.Paths.UsqueConfig = filepath.Join(cfg.Paths.ConfigDir, "usque.json")

	fake := &fakeExecutor{handler: func(Cmd) (Result, error) {
		if err := os.MkdirAll(filepath.Dir(cfg.Paths.UsqueConfig), 0o755); err != nil {
			return Result{}, err
		}
		// What a successful registration leaves behind, in the shape the step's Check reads.
		body := []byte(`{"private_key":"a2V5","id":"device-1"}`)
		if err := os.WriteFile(cfg.Paths.UsqueConfig, body, 0o600); err != nil {
			return Result{}, err
		}
		return okResult("registered")
	}}
	env := &stepEnv{
		cfg:  cfg,
		exec: fake,
		log:  slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		now:  func() time.Time { return engineNow },
		arch: "amd64",
		st:   state.New(cfg.Domain, engineNow),
	}
	if err := (&usqueRegisterStep{env}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		cfg.Paths.UsqueBinary() + " register -c " + cfg.Paths.UsqueConfig + " -n termit-uv --accept-tos",
		"chown root:" + service.UsqueUser + " " + cfg.Paths.UsqueConfig,
	}
	if len(fake.calls) != len(want) {
		t.Fatalf("%d commands, want %d: %+v", len(fake.calls), len(want), fake.calls)
	}
	for i, cmd := range fake.calls {
		if got := cmd.Name + " " + strings.Join(cmd.Args, " "); got != want[i] {
			t.Errorf("command %d = %q, want %q", i, got, want[i])
		}
	}
	if chmodIsReal {
		info, err := os.Stat(cfg.Paths.UsqueConfig)
		if err != nil {
			t.Fatalf("stat the device file: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o640 {
			t.Errorf("device file mode = %04o, want 0640 (the unit's user reads it, the world does not)", perm)
		}
	}
}

// TestInitStartsTheSidecar: the units step only writes the unit, so the sidecar stays "loaded;
// disabled; inactive" unless the start step enables it — which is exactly what the first production
// switch left behind, with the core's SOCKS hop pointing at a closed port.
func TestInitStartsTheSidecar(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Warp.Transport = config.WarpTransportUsque
	cfg.Paths.UsqueConfig = filepath.Join(cfg.Paths.ConfigDir, "usque.json")
	sb := newSandbox()

	installer := newTestInstaller(t, cfg, sb)
	report, err := installer.Run(context.Background(), RunOptions{
		Command: "init",
		Skip:    []string{"preflight", "certificate", "warp", "usque-binary", "usque-register", "warp-egress"},
	})
	if err != nil {
		t.Fatalf("Run: %v\n%+v", err, report)
	}
	if !sb.active[service.UsqueUnitName] {
		t.Errorf("init left %s inactive", service.UsqueUnitName)
	}
}
