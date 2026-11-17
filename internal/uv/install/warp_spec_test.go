package install

import (
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// WarpSpecFor is the single place the WARP half of the core document is built. When the agent's sync
// built its own, every sync rewrote a wireguard node as the proxy sidecar and the node exited directly.
func TestWarpSpecFor(t *testing.T) {
	cases := []struct {
		name          string
		transport     string
		mode          string
		registered    bool
		wantTransport string
		wantMode      string
		wantTunnel    bool
	}{
		{"a registered wireguard node carries its identity", config.WarpTransportWireGuard, config.WarpAll, true,
			xray.WarpTransportWireGuard, config.WarpAll, true},
		{"an unregistered wireguard node exits directly", config.WarpTransportWireGuard, config.WarpAll, false,
			xray.WarpTransportWireGuard, "", false},
		{"mode off keeps the registration out of the document", config.WarpTransportWireGuard, config.WarpOff, true,
			xray.WarpTransportWireGuard, config.WarpOff, false},
		{"the proxy transport never reads the identity", config.WarpTransportProxy, config.WarpAll, false,
			xray.WarpTransportProxy, config.WarpAll, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Warp.Transport = tc.transport
			cfg.Warp.Mode = tc.mode
			if tc.registered {
				probeTestIdentity(t, cfg)
			}
			spec, err := WarpSpecFor(cfg)
			if err != nil {
				t.Fatalf("WarpSpecFor: %v", err)
			}
			if spec.Transport != tc.wantTransport {
				t.Errorf("transport = %q, want %q", spec.Transport, tc.wantTransport)
			}
			if spec.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", spec.Mode, tc.wantMode)
			}
			if got := spec.WireGuard != nil; got != tc.wantTunnel {
				t.Errorf("tunnel in the document = %v, want %v", got, tc.wantTunnel)
			}
			if tc.wantTunnel && spec.WireGuard.SecretKey == "" {
				t.Error("the document carries a tunnel without a key")
			}
		})
	}
}
