package warp

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shogun-kub/termit/internal/uv/state"
)

// Identity is this node's WARP registration: the key pair the tunnel authenticates with, the address
// Cloudflare assigned inside the tunnel, and the peer to reach. It is a credential, not a preference —
// the file is 0600, it never reaches a log line or a status output, and it is written once and then
// kept. Registering again creates a new device on Cloudflare's side, so nothing regenerates it on a
// re-run of init.
type Identity struct {
	// PrivateKey is the node's Curve25519 key, base64, as the core's WireGuard outbound wants it.
	PrivateKey string `yaml:"private_key"`
	// PublicKey is the other half, kept so the identity can be checked without deriving it again.
	PublicKey string `yaml:"public_key"`
	// Addresses are the tunnel addresses Cloudflare assigned, with their prefix lengths.
	Addresses []string `yaml:"addresses"`
	// PeerPublicKey is Cloudflare's side of the tunnel.
	PeerPublicKey string `yaml:"peer_public_key"`
	// Endpoint is the peer's host:port the core dials.
	Endpoint string `yaml:"endpoint"`
	// Reserved is the three-byte client identifier Cloudflare puts in the WireGuard header; a
	// handshake with the wrong bytes is dropped without an error.
	Reserved []byte `yaml:"reserved,omitempty"`
	// DeviceID is the registration's identifier. It is public and is what `warp status` may print.
	DeviceID string `yaml:"device_id,omitempty"`
	// RegisteredAt records when the identity was created.
	RegisteredAt time.Time `yaml:"registered_at,omitempty"`
}

// LoadIdentity reads an identity file, refusing one that is readable by anyone but its owner: a
// credential this file holds is a device on Cloudflare's side, and a group-readable copy is a leak.
func LoadIdentity(filename string) (*Identity, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return nil, fmt.Errorf("warp: read identity %s: %w", filename, err)
	}
	// Windows does not carry Unix permission bits: every file reads as 0666 there, and the node only
	// ever runs on Linux, so the check is meaningless rather than strict.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("warp: identity %s is readable by group or others (%04o)", filename, info.Mode().Perm())
	}
	data, err := os.ReadFile(filename) // #nosec G304 -- the operator chooses the state path.
	if err != nil {
		return nil, fmt.Errorf("warp: read identity %s: %w", filename, err)
	}
	var identity Identity
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&identity); err != nil {
		return nil, fmt.Errorf("warp: identity %s: %w", filename, err)
	}
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("warp: identity %s: %w", filename, err)
	}
	return &identity, nil
}

// SaveIdentity writes the identity atomically with 0600 permissions. The file is the node's WARP
// device: it is replaced only by a deliberate re-registration, never by a routine install step.
func SaveIdentity(filename string, identity *Identity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(identity)
	if err != nil {
		return fmt.Errorf("warp: encode identity: %w", err)
	}
	if err := state.WriteFileAtomic(filename, data, 0o600); err != nil {
		return fmt.Errorf("warp: write identity %s: %w", filename, err)
	}
	return nil
}

// Validate reports what a working tunnel cannot do without.
func (i *Identity) Validate() error {
	var problems []error
	switch {
	case i.PrivateKey == "":
		problems = append(problems, errors.New("the identity has no private key"))
	case i.PublicKey == "":
		problems = append(problems, errors.New("the identity has no public key"))
	case i.PeerPublicKey == "":
		problems = append(problems, errors.New("the identity has no peer public key"))
	case i.Endpoint == "":
		problems = append(problems, errors.New("the identity has no endpoint"))
	case len(i.Addresses) == 0:
		problems = append(problems, errors.New("the identity has no tunnel address"))
	}
	// Cloudflare's client id is three bytes; anything else is a registration this code cannot use.
	if len(i.Reserved) != 0 && len(i.Reserved) != 3 {
		problems = append(problems, fmt.Errorf("the identity's reserved bytes are %d long, want 3", len(i.Reserved)))
	}
	return errors.Join(problems...)
}
