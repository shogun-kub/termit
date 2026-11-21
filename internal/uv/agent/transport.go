package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/uv/hysteria"
)

// Transport keeps the QUIC transport's user set in step with the sync answer.
//
// It has no API and no reload: the users live in its configuration file, so a changed set means rewriting
// one file and restarting one unit. That restart costs the transport's sessions and nothing else — the
// TLS profile and the core are untouched — which is why a peer change can afford it.
type Transport struct {
	// ConfigPath is the file the transport reads; empty means this node does not serve it.
	ConfigPath string
	// Unit is the systemd unit to restart; empty means it is not managed here.
	Unit string
	// Owner is the file owner the transport needs, "root:termit-hy" on a real node. Empty skips the
	// chown, which is what tests want.
	Owner  string
	Runner Runner
}

// Apply writes spec and restarts the transport when the file changed. It reports whether it did.
func (t Transport) Apply(ctx context.Context, spec hysteria.Spec) (bool, error) {
	if t.ConfigPath == "" {
		return false, nil
	}
	want, err := hysteria.Generate(spec)
	if err != nil {
		return false, err
	}
	previous, readErr := os.ReadFile(t.ConfigPath) // #nosec G304 -- a path from our own config.
	if readErr == nil && bytes.Equal(previous, want) {
		return false, nil
	}
	if err := writeTransportFile(t.ConfigPath, want); err != nil {
		return false, err
	}
	if t.Owner != "" {
		if _, err := t.Runner.Run(ctx, "chown", t.Owner, t.ConfigPath); err != nil {
			return false, fmt.Errorf("agent: give %s to %s: %w", t.ConfigPath, t.Owner, err)
		}
	}
	if t.Unit == "" {
		return true, nil
	}
	// A config the binary refuses would take the transport down on this restart; the previous file goes
	// back so the node keeps serving, and the failure is reported rather than hidden.
	_, _ = t.Runner.Run(ctx, "systemctl", "reset-failed", t.Unit)
	if _, err := t.Runner.Run(ctx, "systemctl", "restart", t.Unit); err != nil {
		if readErr == nil {
			if rollbackErr := writeTransportFile(t.ConfigPath, previous); rollbackErr == nil {
				_, _ = t.Runner.Run(ctx, "systemctl", "restart", t.Unit)
				return false, fmt.Errorf("agent: restart %s: %w (the previous config was restored)", t.Unit, err)
			}
		}
		return false, fmt.Errorf("agent: restart %s: %w", t.Unit, err)
	}
	return true, nil
}

// writeTransportFile replaces the file atomically and keeps it readable only by its owner and the
// transport's group: the passwords are in it.
func writeTransportFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hysteria*.yaml")
	if err != nil {
		return fmt.Errorf("agent: write %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agent: write %s: %w", path, err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agent: chmod %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agent: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agent: close %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("agent: install %s: %w", path, err)
	}
	return nil
}
