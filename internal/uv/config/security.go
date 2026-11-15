package config

import (
	"fmt"

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
}

// DenyOthers reports whether the table drops what it did not accept. Unset means yes.
func (f Firewall) DenyOthers() bool { return f.DefaultDeny == nil || *f.DefaultDeny }

// Guard is the ban engine and the kernel-side scan detector it reads.
type Guard struct {
	// Enabled turns the detector on with it: the table's scan meters exist so the guard has
	// something to read, and they are rendered only when the guard will look at them.
	Enabled bool `yaml:"enabled"`
	Scan    Scan `yaml:"scan"`
}

// Scan is the kernel detector: how many attempts to a port the host does not serve make a scan, and
// how long an offender stays in the kernel's set for the guard to act on.
type Scan struct {
	Attempts int `yaml:"attempts"`
	// Window is the rate's time unit and has to be exactly 1s, 1m, 1h, 1d or 1w: nftables expresses a
	// rate as N per one unit, so a ten-minute window has no spelling.
	Window   firewall.Duration `yaml:"window"`
	Remember firewall.Duration `yaml:"remember"`
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
	return problems
}

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
