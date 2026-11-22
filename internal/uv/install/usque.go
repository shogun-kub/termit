package install

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

const (
	// usqueVersion is the pinned release of the MASQUE sidecar.
	usqueVersion = "4.2.1"
	// usqueReleaseBase is the release directory. The tag has no path component, unlike Hysteria's
	// "app/v2.14.0", so the base stops at "download" and archiveSource appends "/v<version>".
	usqueReleaseBase = "https://github.com/Diniboy1123/usque/releases/download"
)

// usqueAssets pins the release archive and its digest per GOARCH, taken from the release's
// checksums.txt. A mirror cannot substitute a different archive because the digest is checked before
// the binary is extracted, and the binary inside it is what the unit runs.
var usqueAssets = map[string]archivePin{
	"amd64": {Name: "usque_4.2.1_linux_amd64.zip", SHA256: "4117e20695078af9c11edecd1a826c009bbc7ea0b7f64458612b4198910bc313"},
	"arm64": {Name: "usque_4.2.1_linux_arm64.zip", SHA256: "c88b061c2a567f30813d7505637d1fb6fe7ec5898b5b61fd05122409fa5ad925"},
}

// usquePin is the archive and digest this architecture must install.
func usquePin(arch string) (archivePin, error) {
	pin, ok := usqueAssets[arch]
	if !ok {
		return archivePin{}, fmt.Errorf("usque %s has no pinned binary for %s", usqueVersion, arch)
	}
	return pin, nil
}

// usqueBinaryStep installs the sidecar's binary.
type usqueBinaryStep struct{ e *stepEnv }

func (*usqueBinaryStep) Name() string { return "usque-binary" }

func (s *usqueBinaryStep) Check(context.Context) (CheckResult, error) {
	if !s.e.cfg.Warp.UsesUsque() {
		return CheckResult{Satisfied: true, Detail: "the MASQUE sidecar is not the transport"}, nil
	}
	info, err := os.Stat(s.e.cfg.Paths.UsqueBinary())
	if err != nil {
		return CheckResult{Detail: "the sidecar binary is missing"}, nil
	}
	if info.Mode().Perm()&0o111 == 0 {
		return CheckResult{Detail: "the sidecar binary is not executable"}, nil
	}
	return CheckResult{Satisfied: true, Detail: "usque " + usqueVersion}, nil
}

func (s *usqueBinaryStep) Run(ctx context.Context) error {
	if !s.e.cfg.Warp.UsesUsque() {
		return nil
	}
	pin, err := usquePin(s.e.arch)
	if err != nil {
		return err
	}
	cacheDir := filepath.Join(s.e.cfg.Paths.StateDir, "cache")
	archive, err := fetchVerified(ctx, s.e.http, usqueReleaseBase, usqueVersion, pin, cacheDir, "usque")
	if err != nil {
		return err
	}
	s.e.st.AddPath(cacheDir, state.KindDir, "usque-binary", s.e.now())

	binary, err := usqueArchiveEntry(archive, "usque")
	if err != nil {
		return err
	}
	installed := s.e.cfg.Paths.UsqueBinary()
	// Re-extracting is cheap and idempotent; the comparison keeps the mtime of an unchanged binary, so
	// systemd does not see a new file on every `init`.
	if current, err := os.ReadFile(installed); err != nil || !bytes.Equal(current, binary) { // #nosec G304 -- a path this step built.
		if err := os.WriteFile(installed, binary, 0o755); err != nil { // #nosec G306 -- an executable, not a secret.
			return fmt.Errorf("usque: write %s: %w", installed, err)
		}
	}
	if err := os.Chmod(installed, 0o755); err != nil {
		return fmt.Errorf("usque: chmod %s: %w", installed, err)
	}
	s.e.st.AddPath(installed, state.KindFile, "usque-binary", s.e.now())
	s.e.log.Info("installed the MASQUE sidecar", "version", usqueVersion, "path", installed, "sha256", pin.SHA256)
	return nil
}

// usqueArchiveEntry reads one file out of a zip archive held in the cache.
func usqueArchiveEntry(archive, name string) ([]byte, error) {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return nil, fmt.Errorf("usque: open the verified archive: %w", err)
	}
	defer func() { _ = reader.Close() }()
	for _, file := range reader.File {
		if filepath.Base(file.Name) != name {
			continue
		}
		if file.UncompressedSize64 > 64<<20 {
			return nil, fmt.Errorf("usque: %s in the archive is %d bytes, too large", name, file.UncompressedSize64)
		}
		body, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("usque: open %s in the archive: %w", name, err)
		}
		defer func() { _ = body.Close() }()
		return io.ReadAll(io.LimitReader(body, 64<<20))
	}
	return nil, fmt.Errorf("usque: the archive has no %s", name)
}

// usqueRegisterStep registers the device with Cloudflare through the sidecar's own command.
//
// The file it writes holds the device key and the access token, so it is a credential: `0640`
// root:termit-usque from the first moment it exists (the unit's user reads it, the world does not),
// never rewritten once written (a second registration is a second device), and never printed. The
// command's own output is discarded for the same reason — it names the account.
type usqueRegisterStep struct{ e *stepEnv }

func (*usqueRegisterStep) Name() string { return "usque-register" }

// unusable says why the device file cannot serve the sidecar, or "" when it can.
func (s *usqueRegisterStep) unusable() string {
	path := s.e.cfg.Paths.UsqueConfig
	info, err := os.Stat(path)
	if err != nil {
		return "the sidecar has no registered device"
	}
	if info.Mode().Perm()&0o007 != 0 {
		return "the sidecar's device file is readable by others"
	}
	if !parsesDeviceFile(path) {
		return "the sidecar's device file is not a registration"
	}
	// Group-read is what lets the unit's own user open it; a file written before that rule has none.
	if info.Mode().Perm()&0o040 == 0 {
		return "the sidecar's user cannot read the device file"
	}
	return ""
}

func (s *usqueRegisterStep) Check(context.Context) (CheckResult, error) {
	if !s.e.cfg.Warp.UsesUsque() {
		return CheckResult{Satisfied: true, Detail: "the MASQUE sidecar is not the transport"}, nil
	}
	if why := s.unusable(); why != "" {
		return CheckResult{Detail: why}, nil
	}
	return CheckResult{Satisfied: true, Detail: "a device is registered"}, nil
}

func (s *usqueRegisterStep) Run(ctx context.Context) error {
	if !s.e.cfg.Warp.UsesUsque() {
		return nil
	}
	path := s.e.cfg.Paths.UsqueConfig
	if !parsesDeviceFile(path) {
		// A file that exists but does not parse is a half-written or corrupted credential: it is moved
		// aside rather than reused, because registering on top of it would keep a key the service cannot
		// use. The backup is deleted when the new registration succeeds.
		backup := path + ".broken"
		if _, err := os.Stat(path); err == nil {
			if err := os.Rename(path, backup); err != nil {
				return fmt.Errorf("usque: move the unusable device file aside: %w", err)
			}
			_ = os.Chmod(backup, 0o600)
		}
		binary := s.e.cfg.Paths.UsqueBinary()
		// --accept-tos: without it the sidecar asks for Cloudflare's terms on stdin, and an install has
		// none ("failed to read user input: EOF"). Registering already means accepting them, as
		// `warp-cli` does.
		args := []string{"register", "-c", path, "-n", "termit-uv", "--accept-tos"}
		if _, err := s.e.exec.Run(ctx, Cmd{Name: binary, Args: args}); err != nil {
			return fmt.Errorf("usque: register the device: %w", err)
		}
		s.e.log.Info("registered a WARP device for the MASQUE sidecar")
		_ = os.Remove(backup)
	}
	// A registration that predates the rule above is made readable too: registering a second device to
	// fix a mode would be the wrong repair, and a chown of a correct file is idempotent.
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "chown", Args: []string{"root:" + service.UsqueUser, path}}); err != nil {
		return fmt.Errorf("usque: give %s to root:%s: %w", path, service.UsqueUser, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		return fmt.Errorf("usque: chmod %s: %w", path, err)
	}
	s.e.st.AddPath(path, state.KindFile, "usque-register", s.e.now())
	return nil
}

// parsesDeviceFile reports whether the file at path is a registration the sidecar can use.
func parsesDeviceFile(path string) bool {
	body, err := os.ReadFile(path) // #nosec G304 -- a path from the configuration.
	if err != nil {
		return false
	}
	var registered struct {
		PrivateKey string `json:"private_key"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(body, &registered); err != nil {
		return false
	}
	return registered.PrivateKey != "" && registered.ID != ""
}

// usqueOffStep removes the sidecar when the configuration no longer asks for it: a unit for a binary
// nobody runs is a failed service on every boot, and a device file nobody reads is a credential lying
// around.
type usqueOffStep struct{ e *stepEnv }

func (*usqueOffStep) Name() string { return "usque-off" }

func (s *usqueOffStep) Check(context.Context) (CheckResult, error) {
	if s.e.cfg.Warp.UsesUsque() {
		return CheckResult{Satisfied: true, Detail: "the MASQUE sidecar is the transport"}, nil
	}
	for _, path := range s.paths() {
		if _, err := os.Stat(path); err == nil {
			return CheckResult{Detail: "the MASQUE sidecar is installed but no longer the transport"}, nil
		}
	}
	return CheckResult{Satisfied: true, Detail: "the MASQUE sidecar is not installed"}, nil
}

func (s *usqueOffStep) paths() []string {
	return []string{
		filepath.Join(s.e.cfg.Paths.UnitDir, service.UsqueUnitName),
		filepath.Join(s.e.cfg.Paths.UnitDir, service.UsqueRelayUnitName),
		s.e.cfg.Paths.UsqueBinary(),
		s.e.cfg.Paths.UsqueConfig,
	}
}

func (s *usqueOffStep) Run(ctx context.Context) error {
	if s.e.cfg.Warp.UsesUsque() {
		return nil
	}
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"disable", "--now", service.UsqueUnitName, service.UsqueRelayUnitName}}); err != nil {
		// A unit that is not loaded is the common case here; the file removal below is what matters.
		s.e.log.Debug("usque: systemctl disable", "err", err)
	}
	for _, path := range s.paths() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("usque: remove %s: %w", path, err)
		}
		s.e.st.RemovePath(path)
	}
	return nil
}

// UsqueSocks is where the sidecar listens when the configuration does not say otherwise.
func UsqueSocks(cfg *config.Config) string {
	return xray.DefaultUsqueSocks
}
