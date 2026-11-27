package firewall

import (
	"net/netip"
	"slices"
)

// Drift names the table fields the configuration would render differently from the record the last `on`
// left behind. The boot unit restores that record and never re-reads the configuration, so a UDP port or
// an SSH source added afterwards waits for the next `on` without anything saying so; this list is what
// makes the wait visible in `status`, in `doctor` and in the boot journal.
func Drift(applied *Applied, want Table) []string {
	if applied == nil {
		return nil
	}
	var out []string
	if !samePorts(applied.Ports, want.Ports) {
		out = append(out, "ports")
	}
	if !samePorts(applied.UDPPorts, want.UDPPorts) {
		out = append(out, "udp_ports")
	}
	if !sameStrings(applied.SSHFrom, prefixStrings(want.SSHFrom)) {
		out = append(out, "ssh_from")
	}
	if applied.DefaultDeny != want.DefaultDeny {
		out = append(out, "default_deny")
	}
	return out
}

// samePorts compares two port lists without caring about order.
func samePorts(a, b []int) bool {
	left, right := slices.Clone(a), slices.Clone(b)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// sameStrings compares two string lists without caring about order.
func sameStrings(a, b []string) bool {
	left, right := slices.Clone(a), slices.Clone(b)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// prefixStrings renders networks the way the applied record stores them.
func prefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, prefix.String())
	}
	return out
}
