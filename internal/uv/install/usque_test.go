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

// TestUsqueRegisterAcceptsTheTerms pins the flag the sidecar needs. `usque register` prompts for
// Cloudflare's terms on stdin unless --accept-tos is passed and an install has no stdin, so the first
// production switch stopped at "failed to read user input: EOF".
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
	if len(fake.calls) != 1 {
		t.Fatalf("%d commands, want one: %+v", len(fake.calls), fake.calls)
	}
	got := strings.Join(fake.calls[0].Args, " ")
	want := "register -c " + cfg.Paths.UsqueConfig + " -n termit-uv --accept-tos"
	if got != want {
		t.Errorf("register args = %q, want %q", got, want)
	}
}
