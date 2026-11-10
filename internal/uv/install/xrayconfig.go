package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// ownerEmail is the address the node's own device is known by. The API and the logs use it, and the
// owner sees it in `uv status`, so it is a fixed word rather than a random string.
const ownerEmail = "owner"

// xrayConfigStep writes the proxy core's configuration.
//
// It is written atomically and checked with `xray -test` before it replaces the live file: a config
// the core refuses would otherwise take the node down at the next restart, possibly hours later.
type xrayConfigStep struct{ e *stepEnv }

func (*xrayConfigStep) Name() string { return "xray-config" }

// spec builds the generator input from the persisted identity. The secret path and the node's own
// UUID come from the state file, never from the command line, so a re-run cannot mint new ones.
func (s *xrayConfigStep) spec() (xray.Spec, error) {
	spec := xray.DefaultSpec()
	if s.e.st.Identity.SelfUUID == "" || s.e.st.Identity.XHTTPPath == "" {
		return spec, errors.New("the node identity has not been generated")
	}
	spec.Path = "/" + s.e.st.Identity.XHTTPPath + "/"
	spec.Warp = xray.WarpSpec{
		Mode:     s.e.cfg.Warp.Mode,
		Selected: s.e.cfg.Warp.Selected,
		FailOpen: s.e.cfg.Warp.FailOpenEnabled(),
	}
	spec.Clients = []xray.Client{{ID: s.e.st.Identity.SelfUUID, Email: ownerEmail}}
	for _, user := range s.e.st.LocalUsers {
		spec.Clients = append(spec.Clients, xray.Client{ID: user.ID, Email: localEmail(user)})
	}
	// An enrolled node has two more kinds of client that must survive any local regeneration: the
	// server's probe credential, without which the next remote check fails, and every peer's pair
	// credential, without which the mesh loses this node. `uv device add` used to write a config that
	// dropped both, and the node looked broken minutes later.
	if s.e.st.Credentials != nil && s.e.st.Credentials.ProbeUUID != "" {
		spec.Clients = append(spec.Clients, xray.Client{ID: s.e.st.Credentials.ProbeUUID, Email: "probe@termit"})
	}
	for _, peer := range s.e.st.Peers {
		if peer.UUID == "" {
			continue
		}
		spec.Clients = append(spec.Clients, xray.Client{ID: peer.UUID, Email: "peer-" + peer.ID + "@termit"})
	}
	return spec, nil
}

// localEmail turns a device into the address the core logs. The label is free text, so it cannot be
// the address itself; the UUID prefix keeps it stable across renames.
func localEmail(user state.LocalUser) string {
	id := strings.ReplaceAll(user.ID, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return "local-" + id + "@termit"
}

func (s *xrayConfigStep) Check(context.Context) (CheckResult, error) {
	spec, err := s.spec()
	if err != nil {
		return CheckResult{Detail: err.Error()}, nil
	}
	want, err := xray.Generate(spec)
	if err != nil {
		return CheckResult{}, err
	}
	got, err := os.ReadFile(s.e.cfg.Paths.XrayConfig) // #nosec G304 -- a path from our own config.
	switch {
	case errors.Is(err, os.ErrNotExist):
		return CheckResult{Detail: s.e.cfg.Paths.XrayConfig + " does not exist"}, nil
	case err != nil:
		return CheckResult{}, fmt.Errorf("read %s: %w", s.e.cfg.Paths.XrayConfig, err)
	}
	if !bytes.Equal(got, want) {
		return CheckResult{Detail: "the config on disk no longer matches the node's identity or clients"}, nil
	}
	info, err := os.Stat(s.e.cfg.Paths.XrayConfig)
	if err != nil {
		return CheckResult{}, fmt.Errorf("stat %s: %w", s.e.cfg.Paths.XrayConfig, err)
	}
	if chmodIsReal && info.Mode().Perm() != 0o640 {
		return CheckResult{Detail: fmt.Sprintf("%s has mode %04o, want 0640", s.e.cfg.Paths.XrayConfig, info.Mode().Perm())}, nil
	}
	return CheckResult{Satisfied: true, Detail: fmt.Sprintf("%s, %d client(s)", spec.Path, len(spec.Clients))}, nil
}

func (s *xrayConfigStep) Run(ctx context.Context) error {
	spec, err := s.spec()
	if err != nil {
		return err
	}
	config, err := xray.Generate(spec)
	if err != nil {
		return err
	}

	target := s.e.cfg.Paths.XrayConfig
	previous, previousErr := os.ReadFile(target) // #nosec G304 -- a path from our own config.
	// The core picks its parser by extension, so the staged file has to end in .json too.
	staged := target + ".new.json"
	if err := os.WriteFile(staged, config, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", staged, err)
	}
	defer func() { _ = os.Remove(staged) }()

	// The core runs as `termit`, so the file has to be readable by that group and by nobody else.
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "chown", Args: []string{"root:" + serviceUser, staged}}); err != nil {
		return fmt.Errorf("give %s to root:%s: %w", staged, serviceUser, err)
	}
	if _, err := s.e.exec.Run(ctx, Cmd{
		Name: s.e.cfg.Paths.XrayBinary(),
		Args: []string{"run", "-test", "-config", staged},
	}); err != nil {
		return fmt.Errorf("the generated config was rejected by the core: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("install %s: %w", target, err)
	}

	now := s.e.now()
	s.e.st.AddPath(filepath.Dir(target), state.KindDir, "xray-config", now)
	s.e.st.AddPath(target, state.KindFile, "xray-config", now)
	s.e.st.Touch()

	if err := s.restartIfRunning(ctx, previous, previousErr == nil); err != nil {
		return err
	}
	s.e.log.Info("wrote the proxy config",
		"path", target, "clients", len(spec.Clients), "xray_path", "'"+spec.Path+"'")
	return nil
}

// restartIfRunning reloads the core only when it is already installed as a service. Before the units
// step it is not running, and a restart would only produce a confusing error.
func (s *xrayConfigStep) restartIfRunning(ctx context.Context, previous []byte, hadPrevious bool) error {
	if !s.e.serviceActive(ctx, service.XrayUnitName) {
		return nil
	}
	// A deliberate restart can follow a burst of them — an upgrade, a mode switch, a sync — and
	// systemd's start limiter would then refuse to start the core at all: the node would be down
	// because it was restarted too often, not because anything is wrong with it. Clearing the
	// counter first is systemd's own remedy, and a restart that genuinely cannot start the core
	// still reports the failure.
	_, _ = s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"reset-failed", service.XrayUnitName}})
	_, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"restart", service.XrayUnitName}})
	if err == nil {
		return nil
	}
	// The core refused the new config at runtime. Put the old one back so the node keeps serving,
	// then report both failures: the operator needs to know the rollback happened.
	if hadPrevious {
		if rollbackErr := os.WriteFile(s.e.cfg.Paths.XrayConfig, previous, 0o640); rollbackErr == nil {
			_, _ = s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"restart", service.XrayUnitName}})
			return fmt.Errorf("restart %s: %w (the previous config was restored)", service.XrayUnitName, err)
		}
	}
	return fmt.Errorf("restart %s: %w (the previous config could not be restored)", service.XrayUnitName, err)
}

// serviceActive reports whether a systemd unit is currently running.
func (s *stepEnv) serviceActive(ctx context.Context, unit string) bool {
	_, err := s.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", unit}})
	return err == nil
}
