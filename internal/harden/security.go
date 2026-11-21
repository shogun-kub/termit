// Package harden is the security block both configurations share: the opt-in host table, the ban
// engine's thresholds and the blocklist. It is configuration plus the small amount of assembly that
// turns a block into an nftables table or a ban ladder; the commands live in hardencmd.
//
// Everything here is off by default, and nothing here is applied by an install, a sync or a timer: the
// table exists only after an operator types `firewall on`, which is the one command allowed to change
// host firewall state (docs/DESCRIPTION.md §11, docs/IMPLEMENTATION_PLAN.md M5).
package harden

import (
	"fmt"
	"net/url"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

const (
	// DefaultScanAttempts is what counts as a port scan: N rejected attempts inside the window.
	DefaultScanAttempts = 5
	// DefaultScanWindow is the rate's time unit. nftables expresses a rate as N per one unit, so it has
	// to be exactly one of second, minute, hour, day or week.
	DefaultScanWindow = time.Minute
	// DefaultScanRemember is how long the kernel remembers a source: enough for one guard pass, and
	// long enough that a burst is not forgotten between two of them.
	DefaultScanRemember = 15 * time.Minute
	// DefaultScanBan is the first ban a scan earns; DefaultScanRepeat the one inside the ladder window.
	DefaultScanBan    = time.Hour
	DefaultScanRepeat = 24 * time.Hour
	// DefaultLadderWindow is how long a repeat still counts as one.
	DefaultLadderWindow = 24 * time.Hour

	// The failed-request ladder: DefaultPath404Attempts 4xx answers inside DefaultPath404Window earn
	// DefaultPath404Ban, each repeat doubles up to DefaultPath404Max.
	DefaultPath404Attempts = 10
	DefaultPath404Window   = 5 * time.Minute
	DefaultPath404Ban      = 5 * time.Minute
	DefaultPath404Max      = time.Hour

	// DefaultBlocklistRefresh fetches the network list once a day: that is how often such lists change.
	DefaultBlocklistRefresh = 24 * time.Hour
)

// Config is the `security:` block.
type Config struct {
	Firewall Firewall `yaml:"firewall"`
	Guard    Guard    `yaml:"guard"`
}

// Firewall is the shape of the host's own table.
type Firewall struct {
	// Enabled records the operator's intent. It is what `firewall status` compares the host with; the
	// table itself is applied by the command.
	Enabled bool `yaml:"enabled"`
	// DefaultDeny drops inbound traffic the rules did not accept. It is a pointer so that an absent
	// key (the default, on) can be told apart from an explicit `default_deny: false`.
	DefaultDeny *bool `yaml:"default_deny"`
	// SSHFrom restricts port 22 to these CIDRs or addresses. Empty means from anywhere, which is the
	// common case: a static operator address is the exception.
	SSHFrom []string `yaml:"ssh_from"`
	// UDPPorts are the inbound UDP ports the component serves besides the TCP ones its table is built
	// for. A QUIC transport needs its port here: an unlisted UDP port falls through to the scan meter.
	UDPPorts []int `yaml:"udp_ports"`
	// BlocklistURL is the network list the host refuses; empty means no list at all. http(s) and file
	// URLs are supported, and nothing else: no git, no rsync.
	BlocklistURL string `yaml:"blocklist_url"`
	// BlocklistRefresh is how often the timer fetches it.
	BlocklistRefresh firewall.Duration `yaml:"blocklist_refresh"`
}

// DenyOthers reports whether the table drops what it did not accept. Unset means yes.
func (f Firewall) DenyOthers() bool { return f.DefaultDeny == nil || *f.DefaultDeny }

// Guard is the ban engine and the kernel-side scan detector it reads.
type Guard struct {
	// Enabled turns the detector on with it: the table's scan meters exist so the guard has
	// something to read, and they are rendered only when the guard will look at them.
	Enabled bool    `yaml:"enabled"`
	Scan    Scan    `yaml:"scan"`
	Path404 Path404 `yaml:"path404"`
	// LadderWindow is how long a repeat still counts as one: past it, a source starts the ladder over.
	LadderWindow firewall.Duration `yaml:"ladder_window"`
}

// Scan is the kernel detector: how many attempts to a port the host does not serve make a scan, and
// how long an offender stays in the kernel's set for the guard to act on.
type Scan struct {
	Attempts int `yaml:"attempts"`
	// Window is the rate's time unit and has to be exactly 1s, 1m, 1h, 1d or 1w: nftables expresses a
	// rate as N per one unit, so a ten-minute window has no spelling.
	Window   firewall.Duration `yaml:"window"`
	Remember firewall.Duration `yaml:"remember"`
	// Ban is the first ban a scan earns; Repeat what a scan inside the ladder window earns.
	Ban    firewall.Duration `yaml:"ban"`
	Repeat firewall.Duration `yaml:"repeat"`
}

// Path404 is the failed-request detector: how many 4xx answers from one source inside a window make a
// ban, and how far the ladder doubles.
type Path404 struct {
	Attempts int               `yaml:"attempts"`
	Window   firewall.Duration `yaml:"window"`
	Ban      firewall.Duration `yaml:"ban"`
	Max      firewall.Duration `yaml:"max"`
}

// ApplyDefaults fills the block. An existing configuration that predates it gets the same values a
// fresh one does; because every part is off by default, nothing changes on the host.
func (c *Config) ApplyDefaults() {
	if c.Firewall.DefaultDeny == nil {
		on := true
		c.Firewall.DefaultDeny = &on
	}
	if c.Guard.Scan.Attempts == 0 {
		c.Guard.Scan.Attempts = DefaultScanAttempts
	}
	if c.Guard.Scan.Window == 0 {
		c.Guard.Scan.Window = firewall.Duration(DefaultScanWindow)
	}
	if c.Guard.Scan.Remember == 0 {
		c.Guard.Scan.Remember = firewall.Duration(DefaultScanRemember)
	}
	if c.Guard.Scan.Ban == 0 {
		c.Guard.Scan.Ban = firewall.Duration(DefaultScanBan)
	}
	if c.Guard.Scan.Repeat == 0 {
		c.Guard.Scan.Repeat = firewall.Duration(DefaultScanRepeat)
	}
	if c.Guard.Path404.Attempts == 0 {
		c.Guard.Path404.Attempts = DefaultPath404Attempts
	}
	if c.Guard.Path404.Window == 0 {
		c.Guard.Path404.Window = firewall.Duration(DefaultPath404Window)
	}
	if c.Guard.Path404.Ban == 0 {
		c.Guard.Path404.Ban = firewall.Duration(DefaultPath404Ban)
	}
	if c.Guard.Path404.Max == 0 {
		c.Guard.Path404.Max = firewall.Duration(DefaultPath404Max)
	}
	if c.Guard.LadderWindow == 0 {
		c.Guard.LadderWindow = firewall.Duration(DefaultLadderWindow)
	}
	if c.Firewall.BlocklistRefresh == 0 {
		c.Firewall.BlocklistRefresh = firewall.Duration(DefaultBlocklistRefresh)
	}
}

// Validate reports every problem in the block.
func (c *Config) Validate() []error {
	var problems []error
	for _, value := range c.Firewall.SSHFrom {
		if _, err := firewall.ParsePrefix(value); err != nil {
			problems = append(problems, fmt.Errorf("security.firewall.ssh_from: %w", err))
		}
	}
	if err := c.Scan().Validate(); err != nil {
		problems = append(problems, fmt.Errorf("security.guard.scan: %w", err))
	}
	if err := c.Ladder().Validate(); err != nil {
		problems = append(problems, fmt.Errorf("security.guard: %w", err))
	}
	if c.Guard.Path404.Attempts < 2 {
		problems = append(problems, fmt.Errorf("security.guard.path404.attempts must be at least 2, got %d",
			c.Guard.Path404.Attempts))
	}
	if c.Guard.Path404.Window.Std() >= c.Guard.LadderWindow.Std() {
		problems = append(problems, fmt.Errorf("security.guard.path404.window (%s) must be shorter than "+
			"security.guard.ladder_window (%s)", c.Guard.Path404.Window, c.Guard.LadderWindow))
	}
	if err := validateBlocklist(c.Firewall); err != nil {
		problems = append(problems, err)
	}
	return problems
}

// validateBlocklist checks the source and the cadence. A refresh shorter than an hour would hammer a
// public list for no gain: these lists change daily, not hourly.
func validateBlocklist(f Firewall) error {
	if f.BlocklistURL != "" {
		parsed, err := url.Parse(f.BlocklistURL)
		if err != nil {
			return fmt.Errorf("security.firewall.blocklist_url: %v", err)
		}
		switch parsed.Scheme {
		case "http", "https":
			if parsed.Host == "" {
				return fmt.Errorf("security.firewall.blocklist_url %q has no host", f.BlocklistURL)
			}
		case "file":
			if parsed.Path == "" {
				return fmt.Errorf("security.firewall.blocklist_url %q has no path", f.BlocklistURL)
			}
		default:
			return fmt.Errorf("security.firewall.blocklist_url %q: only http, https and file URLs are supported",
				f.BlocklistURL)
		}
	}
	if f.BlocklistRefresh.Std() < time.Hour {
		return fmt.Errorf("security.firewall.blocklist_refresh must be at least 1h, got %s: a public list "+
			"changes daily, and asking more often is a way to be blocked by a mirror", f.BlocklistRefresh)
	}
	return nil
}

// Ladder is the ban policy the guard applies, assembled from this block.
func (c *Config) Ladder() firewall.Ladder {
	return firewall.Ladder{
		Window: c.Guard.LadderWindow.Std(),
		Scan: firewall.Escalation{
			First:  c.Guard.Scan.Ban.Std(),
			Repeat: c.Guard.Scan.Repeat.Std(),
		},
		Path404: firewall.Escalation{
			First:  c.Guard.Path404.Ban.Std(),
			Repeat: c.Guard.Path404.Max.Std(),
			Double: true,
		},
	}
}

// Table assembles a table from this block, ready to render. name is the component's own table
// (firewall.NodeTable or firewall.ServerTable) and ports what that component serves.
func (c *Config) Table(name string, ports []int) (firewall.Table, error) {
	prefixes, err := firewall.Prefixes(c.Firewall.SSHFrom)
	if err != nil {
		return firewall.Table{}, err
	}
	return firewall.Table{
		Name:        name,
		Ports:       append([]int(nil), ports...),
		UDPPorts:    append([]int(nil), c.Firewall.UDPPorts...),
		SSHFrom:     prefixes,
		DefaultDeny: c.Firewall.DenyOthers(),
		Scan:        c.Scan(),
	}, nil
}

// Scan is the detector the table carries.
func (c *Config) Scan() firewall.Scan {
	return firewall.Scan{
		Enabled:  c.Guard.Enabled,
		Attempts: c.Guard.Scan.Attempts,
		Window:   c.Guard.Scan.Window.Std(),
		Remember: c.Guard.Scan.Remember.Std(),
	}
}
