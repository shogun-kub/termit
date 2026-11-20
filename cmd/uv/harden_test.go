package main

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// hardenApp builds a node whose state (and so its WARP identity) lives in a temporary directory.
func hardenApp(t *testing.T, mode string) (*application, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.Domain = "node.example.com"
	cfg.Warp.Mode = mode
	if mode == config.WarpSelected {
		cfg.Warp.Selected = []string{"node.example.com"}
	}
	cfg.Paths.ConfigDir = filepath.Join(dir, "etc")
	cfg.Paths.StateDir = filepath.Join(dir, "state")
	cfg.Paths.CertDir = filepath.Join(dir, "certs")
	cfg.Paths.ACMEWebroot = filepath.Join(dir, "www")
	cfg.Paths.SiteDir = filepath.Join(dir, "site")
	cfg.Paths.BinDir = filepath.Join(dir, "bin")
	cfg.Paths.XrayConfig = filepath.Join(dir, "etc", "xray.json")
	cfg.Paths.NginxSite = filepath.Join(dir, "nginx", "termit")
	cfg.Paths.SitesEnabled = filepath.Join(dir, "nginx", "sites-enabled")
	cfg.Paths.UnitDir = filepath.Join(dir, "units")
	cfg.Paths.ConfigFile = configPath
	cfg.Paths.NginxDropIn = filepath.Join(dir, "nginx", "termit.conf")
	cfg.Paths.DecoySnippet = filepath.Join(dir, "nginx", "decoy.conf")
	cfg.Paths.AccessLog = filepath.Join(dir, "logs", "access.log")
	cfg.Paths.ErrorLog = filepath.Join(dir, "logs", "error.log")
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	app, _, _ := newTestApp(t)
	return app, cfg, configPath
}

// tunnelIdentity is a registered WARP device: the endpoint is the address the node's own tunnel dials.
func tunnelIdentity() *warp.Identity {
	return &warp.Identity{
		PrivateKey:    "cHJpdmF0ZS1rZXktZm9yLXRlc3RzLW9ubHk=",
		PublicKey:     "cHVibGljLWtleS1mb3ItdGVzdHMtb25seQ==",
		Addresses:     []string{"172.16.0.2/32"},
		PeerPublicKey: "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
		Endpoint:      "162.159.192.1:2408",
		Reserved:      []byte{240, 25, 146},
		RegisteredAt:  time.Unix(0, 0).UTC(),
	}
}

// The tunnel endpoint is the node's own upstream: banning it would defend the node into an hour without
// egress. It stays in the never-ban set whatever warp.mode says — a node that turns WARP off keeps the
// identity, and turning it back on must not start by banning the peer.
func TestTunnelEndpointIsNeverBanned(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{config.WarpAll, config.WarpSelected, config.WarpOff} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			app, cfg, configPath := hardenApp(t, mode)
			if err := warp.SaveIdentity(cfg.Paths.WarpIdentityPath(), tunnelIdentity()); err != nil {
				t.Fatalf("SaveIdentity: %v", err)
			}
			component, err := (&uvHarden{app: app}).Open(configPath)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			want := netip.MustParsePrefix("162.159.192.1/32")
			for _, prefix := range component.Never(context.Background()) {
				if prefix == want {
					return
				}
			}
			t.Fatalf("the never-ban set does not contain the tunnel endpoint: %v", component.Never(context.Background()))
		})
	}
}

// A node that never registered WARP has no identity, and that is not an error: the set is what it was.
func TestNoIdentityMeansNoEndpoint(t *testing.T) {
	t.Parallel()
	app, _, configPath := hardenApp(t, config.WarpAll)
	component, err := (&uvHarden{app: app}).Open(configPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, prefix := range component.Never(context.Background()) {
		if prefix.Addr() == netip.MustParseAddr("162.159.192.1") {
			t.Fatalf("a node with no WARP identity has %s in its never-ban set", prefix)
		}
	}
}
