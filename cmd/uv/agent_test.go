package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// The agent's sync regenerates the core's document. It used to build the WARP half itself, without the
// transport and without the identity, so every sync replaced a wireguard tunnel with the proxy sidecar.
func TestApplierKeepsTheWireGuardTunnel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Paths.StateDir = filepath.Join(dir, "state")
	cfg.Warp.Transport = config.WarpTransportWireGuard
	cfg.Warp.Mode = config.WarpAll
	if err := os.MkdirAll(cfg.Paths.StateDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	identity := &warp.Identity{
		PrivateKey: "a2V5LWZvci10ZXN0cy1vbmx5", PublicKey: "cHViLWZvci10ZXN0cy1vbmx5",
		Addresses: []string{"172.16.0.2/32"}, PeerPublicKey: "cGVlci1rZXk=",
		Endpoint: "162.159.192.1:2408", Reserved: []byte{1, 2, 3}, DeviceID: "device-1",
	}
	if err := warp.SaveIdentity(cfg.Paths.WarpIdentityPath(), identity); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	st := &state.State{Identity: state.Identity{
		SelfUUID: "11111111-2222-4333-8444-555555555555", XHTTPPath: "deadbeef",
	}}

	applier, err := mustApp(t).applier(cfg, st)
	if err != nil {
		t.Fatalf("applier: %v", err)
	}
	if applier.Warp.Transport != xray.WarpTransportWireGuard {
		t.Fatalf("transport = %q, want wireguard: the sync would write the proxy sidecar", applier.Warp.Transport)
	}
	if applier.Warp.WireGuard == nil {
		t.Fatal("the document the sync writes would carry no tunnel")
	}
	if applier.Warp.WireGuard.SecretKey != identity.PrivateKey {
		t.Error("the document carries another key than the registration on disk")
	}
}
