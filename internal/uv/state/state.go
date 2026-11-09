// Package state keeps what the agent generates once: the node identity, the decoy seed, the pinned
// core's digest, the certificate expiry and the manifest of paths Termit created. It also owns the
// atomic file writer every other package uses for secrets.
//
// The state file is the only place credentials live on the node. It is written 0600 and is never
// copied into logs: callers log Fingerprint values, not the secrets themselves.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/shogun-kub/termit/internal/atomicfile"
)

// SchemaVersion is the state schema this build understands.
const SchemaVersion = 1

// Kind classifies a manifest entry so uninstall can remove it correctly.
type Kind string

const (
	// KindFile is a regular file Termit created.
	KindFile Kind = "file"
	// KindDir is a directory Termit created.
	KindDir Kind = "dir"
	// KindSymlink is a symbolic link Termit created.
	KindSymlink Kind = "symlink"
	// KindUser is a system user Termit created.
	KindUser Kind = "user"
	// KindUnit is a systemd unit file Termit created.
	KindUnit Kind = "unit"
)

// Path is one thing Termit created on the host. Uninstall removes exactly these.
type Path struct {
	Path      string    `json:"path"`
	Kind      Kind      `json:"kind"`
	Step      string    `json:"step,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Identity is the node's own credential material. It is generated once at install and never
// regenerated: clients and the management server already know these values.
type Identity struct {
	// SelfUUID is the VLESS UUID of the owner's device on this node's inbound.
	SelfUUID string `json:"self_uuid,omitempty"`
	// XHTTPPath is the secret path segment nginx forwards and Xray expects.
	XHTTPPath string `json:"xhttp_path,omitempty"`
}

// LocalUser is a device the owner added by hand: a phone, a laptop, a router. It is independent of
// the mesh — no other node ever learns this UUID — and it is not the node's own device.
type LocalUser struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

// AddLocalUser records a new device. The UUID must already be valid; the caller generates it.
func (s *State) AddLocalUser(id, label string, now time.Time) error {
	if id == "" {
		return errors.New("state: a local user needs a UUID")
	}
	for _, existing := range s.LocalUsers {
		if existing.ID == id || (label != "" && existing.Label == label) {
			return fmt.Errorf("state: local user %q already exists", label)
		}
	}
	s.LocalUsers = append(s.LocalUsers, LocalUser{ID: id, Label: label, CreatedAt: now})
	s.Touch()
	return nil
}

// RemoveLocalUser drops a device by UUID or by label.
func (s *State) RemoveLocalUser(ref string) error {
	for i, existing := range s.LocalUsers {
		if existing.ID == ref || existing.Label == ref {
			s.LocalUsers = append(s.LocalUsers[:i], s.LocalUsers[i+1:]...)
			s.Touch()
			return nil
		}
	}
	return fmt.Errorf("state: no local user %q", ref)
}

// Decoy is the masking site's persisted randomness.
type Decoy struct {
	Template string `json:"template,omitempty"`
	Seed     string `json:"seed,omitempty"`
	// Digest covers the generated files, so a re-run can tell whether the site on disk is the site
	// the current seed produces.
	Digest string `json:"digest,omitempty"`
}

// Xray records the installed core, so a re-run can tell whether it must be replaced.
type Xray struct {
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Path    string `json:"path,omitempty"`
}

// Certificate is the last issued certificate's lifetime, as far as the agent knows it.
type Certificate struct {
	IssuedAt time.Time `json:"issued_at"`
	NotAfter time.Time `json:"not_after"`
}

// State is the content of state.json.
type State struct {
	SchemaVersion int          `json:"schema_version"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	Domain        string       `json:"domain"`
	Identity      Identity     `json:"identity"`
	Decoy         Decoy        `json:"decoy"`
	Xray          Xray         `json:"xray"`
	LocalUsers    []LocalUser  `json:"local_users,omitempty"`
	Certificate   *Certificate `json:"certificate,omitempty"`
	Manifest      []Path       `json:"manifest,omitempty"`

	dirty bool
}

// New returns the state of a node that is being installed for the first time.
func New(domain string, now time.Time) *State {
	return &State{
		SchemaVersion: SchemaVersion,
		CreatedAt:     now,
		UpdatedAt:     now,
		Domain:        domain,
		dirty:         true,
	}
}

// Load reads state from filename. A missing file is reported as fs.ErrNotExist so callers can
// distinguish "not installed" from "corrupt".
func Load(filename string) (*State, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- the state path comes from configuration.
	if err != nil {
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", filename, err)
	}
	if st.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("state: %s has schema_version %d, newer than this build supports (%d)",
			filename, st.SchemaVersion, SchemaVersion)
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
		st.dirty = true
	}
	return st, nil
}

// Save writes state atomically with 0600 permissions, stamping the current time.
func (s *State) Save(filename string) error { return s.SaveAt(filename, time.Now()) }

// SaveAt is Save with an explicit timestamp, for tests and for steps that already have a clock.
func (s *State) SaveAt(filename string, now time.Time) error {
	s.SchemaVersion = SchemaVersion
	s.UpdatedAt = now.UTC()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	data = append(data, '\n')
	if err := WriteFileAtomic(filename, data, 0o600); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// Dirty reports whether anything changed since the state was loaded or saved.
func (s *State) Dirty() bool { return s.dirty }

// Touch marks the state as changed.
func (s *State) Touch() { s.dirty = true }

// Installed reports whether an identity exists, which is what makes this node "installed".
func (s *State) Installed() bool {
	return s.Identity.SelfUUID != "" && s.Identity.XHTTPPath != ""
}

// AddPath records a path Termit created. Adding the same path twice keeps one entry, so re-running
// an install step does not grow the manifest.
func (s *State) AddPath(filename string, kind Kind, step string, now time.Time) {
	clean := filepath.Clean(filename)
	for i := range s.Manifest {
		if s.Manifest[i].Path == clean {
			return
		}
	}
	s.Manifest = append(s.Manifest, Path{Path: clean, Kind: kind, Step: step, CreatedAt: now.UTC()})
	s.dirty = true
}

// HasPath reports whether filename is in the manifest.
func (s *State) HasPath(filename string) bool {
	clean := filepath.Clean(filename)
	for _, entry := range s.Manifest {
		if entry.Path == clean {
			return true
		}
	}
	return false
}

// RemovePath drops filename from the manifest.
func (s *State) RemovePath(filename string) {
	clean := filepath.Clean(filename)
	for i := range s.Manifest {
		if s.Manifest[i].Path == clean {
			s.Manifest = append(s.Manifest[:i], s.Manifest[i+1:]...)
			s.dirty = true
			return
		}
	}
}

// Fingerprint is a short, non-reversible identifier for a secret, safe to print in logs and status
// output. It is empty for an empty secret.
func Fingerprint(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:8]
}

// WriteFileAtomic replaces filename with data, flushing the data and the rename to disk before it
// returns. A reader either sees the previous content or the new one, never a partial file.
// WriteFileAtomic replaces filename with data atomically. It is the package's public face for the
// shared helper, because most callers are writing state.
func WriteFileAtomic(filename string, data []byte, perm os.FileMode) error {
	return atomicfile.Write(filename, data, perm)
}

// ErrNotInstalled reports that no state file exists.
var ErrNotInstalled = errors.New("node is not installed")

// LoadInstalled loads state and converts a missing file into ErrNotInstalled.
func LoadInstalled(filename string) (*State, error) {
	st, err := Load(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotInstalled
	}
	return st, err
}
