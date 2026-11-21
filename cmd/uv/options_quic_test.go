package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// A node that serves the QUIC transport hands out two links for itself, and both belong under "this
// node": the credentials behind them are the ones the mesh also has, unlike a local device's, which never
// leave the machine. The page used to file the node's own `hy2://` under "Devices", whose note promises
// the opposite — a link captioned "only on this node" for something that is in the mesh.
func TestOptionsFilesTheNodesOwnQUICLinkWithTheNode(t *testing.T) {
	t.Parallel()
	app, cfg, configPath := optionsFixture(t, "")
	// The fixture saves the configuration before the command reads it, so the transport has to be in the
	// file the command loads.
	cfg.Hysteria = &config.Hysteria{}
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}
	// One local device named `phone`, so the page has a Devices section to put the link in wrongly. The
	// fixture already has one of its own, which is why the check below looks for the heading's prefix.
	node, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	node.LocalUsers = append(node.LocalUsers, state.LocalUser{ID: "99999999-8888-4777-8666-555555555555", Label: "phone"})
	if err := node.Save(cfg.Paths.StatePath()); err != nil {
		t.Fatalf("save state: %v", err)
	}

	if code := app.run([]string{"options", "--config", configPath}); code != exitOK {
		t.Fatalf("exit = %d; stderr: %s", code, app.stderr.(*strings.Builder).String())
	}
	reloaded, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, reloaded.Options.Token, "index.html"))
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	html := string(page)

	nodeSection := strings.Index(html, "This node")
	devicesSection := strings.Index(html, "<h2>Devices (")
	if nodeSection < 0 || devicesSection < 0 {
		t.Fatalf("the page has no sections to place a link in: node=%d devices=%d", nodeSection, devicesSection)
	}
	self := strings.Index(html, "node.example.com (QUIC)")
	if self < 0 {
		t.Fatal("the page does not offer the node's own QUIC link")
	}
	if self < nodeSection || self > devicesSection {
		t.Errorf("the node's own QUIC link sits at %d, outside its own section (%d..%d)", self, nodeSection, devicesSection)
	}
	// The local device's QUIC link is a different link and stays where it belongs.
	if device := strings.Index(html, "phone (QUIC)"); device < devicesSection {
		t.Errorf("the local device's QUIC link sits at %d, before the Devices section at %d", device, devicesSection)
	}
}
