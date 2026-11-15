package config

import (
	"fmt"
	"net/url"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
)

// Security is the hardening the operator can ask for. Everything here is off by default, and nothing
// in this block is applied by `init`, `sync`, the agent or a timer: the table exists only after
// `termit-uv firewall on`, which is the one command that may change host firewall state
// (docs/DESCRIPTION.md §11, docs/IMPLEMENTATION_PLAN.md M5).
type Security struct {
	Firewall Firewall `yaml:"firewall"`
	Guard    Guard    `yaml:"guard"`
}

// Firewall is the shape of the node's own table.
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

// applyDefaults fills the security block. An existing configuration that predates it gets the same
// values a fresh one does; because every part is off by default, nothing changes on the host.
func (s *Security) applyDefaults() {
	if s.Firewall.DefaultDeny == nil {
		on := true
		s.Firewall.DefaultDeny = &on
	}
	if s.Guard.Scan.Attempts == 0 {
		s.Guard.Scan.Attempts = DefaultScanAttempts
	}
	if s.Guard.Scan.Window == 0 {
		s.Guard.Scan.Window = firewall.Duration(DefaultScanWindow)
	}
	if s.Guard.Scan.Remember == 0 {
		s.Guard.Scan.Remember = firewall.Duration(DefaultScanRemember)
	}
	if s.Guard.Scan.Ban == 0 {
		s.Guard.Scan.Ban = firewall.Duration(DefaultScanBan)
	}
	if s.Guard.Scan.Repeat == 0 {
		s.Guard.Scan.Repeat = firewall.Duration(DefaultScanRepeat)
	}
	if s.Guard.Path404.Attempts == 0 {
		s.Guard.Path404.Attempts = DefaultPath404Attempts
	}
	if s.Guard.Path404.Window == 0 {
		s.Guard.Path404.Window = firewall.Duration(DefaultPath404Window)
	}
	if s.Guard.Path404.Ban == 0 {
		s.Guard.Path404.Ban = firewall.Duration(DefaultPath404Ban)
	}
	if s.Guard.Path404.Max == 0 {
		s.Guard.Path404.Max = firewall.Duration(DefaultPath404Max)
	}
	if s.Guard.LadderWindow == 0 {
		s.Guard.LadderWindow = firewall.Duration(DefaultLadderWindow)
	}
	if s.Firewall.BlocklistRefresh == 0 {
		s.Firewall.BlocklistRefresh = firewall.Duration(DefaultBlocklistRefresh)
	}
}

// validate reports every problem in the block.
func (s *Security) validate() []error {
	var problems []error
	for _, value := range s.Firewall.SSHFrom {
		if _, err := firewall.ParsePrefix(value); err != nil {
			problems = append(problems, fmt.Errorf("security.firewall.ssh_from: %w", err))
		}
	}
	if err := s.scan().Validate(); err != nil {
		problems = append(problems, fmt.Errorf("security.guard.scan: %w", err))
	}
	if err := s.ladderDirect().Validate(); err != nil {
		problems = append(problems, fmt.Errorf("security.guard: %w", err))
	}
	if s.Guard.Path404.Attempts < 2 {
		problems = append(problems, fmt.Errorf("security.guard.path404.attempts must be at least 2, got %d",
			s.Guard.Path404.Attempts))
	}
	if s.Guard.Path404.Window.Std() >= s.Guard.LadderWindow.Std() {
		problems = append(problems, fmt.Errorf("security.guard.path404.window (%s) must be shorter than "+
			"security.guard.ladder_window (%s)", s.Guard.Path404.Window, s.Guard.LadderWindow))
	}
	if err := validateBlocklist(s.Firewall); err != nil {
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
func (s *Security) Ladder() firewall.Ladder {
	return firewall.Ladder{
		Window: s.Guard.LadderWindow.Std(),
		Scan: firewall.Escalation{
			First:  s.Guard.Scan.Ban.Std(),
			Repeat: s.Guard.Scan.Repeat.Std(),
		},
		Path404: firewall.Escalation{
			First:  s.Guard.Path404.Ban.Std(),
			Repeat: s.Guard.Path404.Max.Std(),
			Double: true,
		},
	}
}

// ladderDirect is the ladder as written, before defaults have to exist: validation runs after
// applyDefaults, so this is just Ladder under a name that says the call does not render anything.
func (s *Security) ladderDirect() firewall.Ladder { return s.Ladder() }

// scan is the detector the table carries.
func (s *Security) scan() firewall.Scan {
	return firewall.Scan{
		Enabled:  s.Guard.Enabled,
		Attempts: s.Guard.Scan.Attempts,
		Window:   s.Guard.Scan.Window.Std(),
		Remember: s.Guard.Scan.Remember.Std(),
	}
}

// Table assembles the node's table from this block, ready to render.
func (s *Security) Table(ports []int) (firewall.Table, error) {
	prefixes, err := firewall.Prefixes(s.Firewall.SSHFrom)
	if err != nil {
		return firewall.Table{}, err
	}
	return firewall.Table{
		Name:        firewall.NodeTable,
		Ports:       append([]int(nil), ports...),
		SSHFrom:     prefixes,
		DefaultDeny: s.Firewall.DenyOthers(),
		Scan:        s.scan(),
	}, nil
}
