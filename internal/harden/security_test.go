package harden_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/harden"
)

// TestUDPPortsReachTheTable: the yaml key is the part a type cannot check. A QUIC port that never reaches
// the table is a port the scan meter counts, and the guard bans for what the meter counted.
func TestUDPPortsReachTheTable(t *testing.T) {
	t.Parallel()
	var cfg harden.Config
	if err := yaml.Unmarshal([]byte("firewall:\n  enabled: true\n  udp_ports: [443]\n"), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	cfg.ApplyDefaults()
	table, err := cfg.Table(firewall.NodeTable, firewall.NodePorts)
	if err != nil {
		t.Fatalf("Table: %v", err)
	}
	data, err := table.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(data), "udp dport 443 accept") {
		t.Errorf("the rendered table has no UDP accept line:\n%s", data)
	}
}
