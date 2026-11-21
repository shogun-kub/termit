package firewall

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/shogun-kub/termit/internal/atomicfile"
)

// ConfirmWindow is how long an applied table waits for `firewall confirm` before the rollback timer removes
// it. It is long enough to open a second session and short enough that a lockout is measured in minutes.
const ConfirmWindow = 5 * time.Minute

// Applied is what the last `firewall on` put in place. It lives next to the component's other state
// because three things need to agree on it: `status` (is the table the configuration's?), the rollback
// timer (has it been confirmed?) and `off` (what to describe afterwards).
//
// It holds no secret: ports, networks and timestamps only.
type Applied struct {
	Table       string    `json:"table"`
	Ports       []int     `json:"ports"`
	UDPPorts    []int     `json:"udp_ports,omitempty"`
	SSHFrom     []string  `json:"ssh_from,omitempty"`
	DefaultDeny bool      `json:"default_deny"`
	AppliedAt   time.Time `json:"applied_at"`
	// Deadline is when the rollback timer removes the table unless `confirm` ran first. It is stored
	// rather than derived so `status` can say how long is left without asking systemd.
	Deadline time.Time `json:"deadline,omitzero"`
	// ConfirmedAt is when the operator confirmed. After it, the timer does nothing.
	ConfirmedAt time.Time `json:"confirmed_at,omitzero"`
	// RemovedAt is when the table was taken down. A record with it set describes a firewall that is
	// off, and it outlives the table so `status` can say when that happened.
	RemovedAt time.Time `json:"removed_at,omitzero"`
}

// Pending reports whether the rules are applied and not yet confirmed.
func (a *Applied) Pending(now time.Time) bool {
	if a == nil || a.AppliedAt.IsZero() || !a.RemovedAt.IsZero() {
		return false
	}
	return a.ConfirmedAt.IsZero() || a.ConfirmedAt.Before(a.AppliedAt)
}

// Confirmed reports whether the operator has confirmed this apply.
func (a *Applied) Confirmed() bool {
	return a != nil && !a.AppliedAt.IsZero() && !a.ConfirmedAt.IsZero() && !a.ConfirmedAt.Before(a.AppliedAt)
}

// LoadApplied reads the record. An absent file is not an error: it means nothing was ever applied
// here, and the caller gets a nil record.
func LoadApplied(path string) (*Applied, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the path comes from the component's configuration.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("firewall: read %s: %w", path, err)
	}
	var applied Applied
	if err := json.Unmarshal(data, &applied); err != nil {
		return nil, fmt.Errorf("firewall: %s: %w", path, err)
	}
	return &applied, nil
}

// SaveApplied writes the record atomically. It is not a secret, but it is read by the rollback unit,
// so a partial file would be worse than none.
func SaveApplied(path string, applied *Applied) error {
	if applied == nil {
		return errors.New("firewall: nothing to save")
	}
	data, err := json.MarshalIndent(applied, "", "  ")
	if err != nil {
		return fmt.Errorf("firewall: encode the applied record: %w", err)
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("firewall: write %s: %w", path, err)
	}
	return nil
}

// Restore answers what a boot unit should do with this record: put the table back, and whether the
// rollback timer has to guard it.
//
// A confirmed apply comes back on its own — the operator already watched this exact table work, and a
// reboot is not a reason to forget that. An apply that was still waiting comes back only while its window
// is open; past the deadline the operator never vouched for it, so it stays gone. A record that says the
// table was taken down is not a table to restore.
func (a *Applied) Restore(now time.Time) (apply bool, armTimer bool) {
	if a == nil || a.AppliedAt.IsZero() || !a.RemovedAt.IsZero() {
		return false, false
	}
	if a.Confirmed() {
		return true, false
	}
	if a.Deadline.IsZero() || !now.Before(a.Deadline) {
		return false, false
	}
	return true, true
}
