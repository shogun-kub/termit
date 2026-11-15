package config

import (
	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/harden"
)

// Security is the shared `security:` block plus the one option that is about the core rather than the
// host. The defaults, the ladder and the table assembly live in internal/harden so the server's
// configuration is the same code with a different table name.
type Security struct {
	harden.Config `yaml:",inline"`
	// BitTorrentBlock makes the core sniff every connection and blackhole the torrent handshakes. It
	// is not part of the firewall, and it costs a little CPU per connection, which is why it is off
	// unless asked for.
	BitTorrentBlock bool `yaml:"bittorrent_block"`
}

// applyDefaults fills the security block.
func (s *Security) applyDefaults() { s.ApplyDefaults() }

// validate reports every problem in the block.
func (s *Security) validate() []error { return s.Validate() }

// Table assembles this node's table, ready to render.
func (s *Security) Table(ports []int) (firewall.Table, error) {
	return s.Config.Table(firewall.NodeTable, ports)
}
