// Package firewall renders and applies the nftables table that hardens a Termit node or server.
//
// One inet table per component is the whole of Termit's firewall: it is hooked before the
// distribution's own rules, everything Termit bans lives in it, and removing the table puts the host
// back exactly where it was. The table is generated from this package's typed model, never written by
// hand, and nothing applies it on its own — only the operator's `firewall` command does, because a
// firewall that a timer or a sync can turn on is a firewall that can lock an owner out while they are
// not looking (docs/DESCRIPTION.md §11, docs/IMPLEMENTATION_PLAN.md M5).
//
// The two halves of the design live here because they must agree: the *shape* of the table (this file)
// and the *elements* the guard and the refresh put into it (nft.go).
package firewall

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultBinary is the nft command. Debian ships nftables and the distribution's own ruleset
	// already uses it, so this is not an extra dependency.
	DefaultBinary = "nft"

	// SSHPort is the one port ssh_from restricts. Everything else in the table is either public or
	// the component's own business.
	SSHPort = 22

	// NodeTable and ServerTable name the two tables. One table per component, so `firewall off` on a
	// node can never touch the server's rules.
	NodeTable   = "termit_uv"
	ServerTable = "termit_mn"

	// maxNameLen is well under nftables' limit, and a longer name is a mistake worth refusing.
	maxNameLen = 32
)

// NodePorts is what a node publishes: SSH, and the ports nginx serves the decoy site and the tunnel
// on. The proxy core itself listens on loopback only.
var NodePorts = []int{SSHPort, 80, 443}

// ServerPorts is what the management server publishes: SSH, TLS, and plain HTTP while an ACME order is
// in flight. Port 80 has to stay open for every node's renewal, not only during this server's own order,
// which is why it is not opened conditionally.
var ServerPorts = []int{SSHPort, 80, 443}

// Scan is the kernel-side scan detector: a meter that counts attempts per source and remembers an
// offender in a set with a TTL, which the guard then reads to apply its ban ladder.
type Scan struct {
	Enabled  bool
	Attempts int
	Window   time.Duration
	Remember time.Duration
}

// Validate checks the scan detector on its own, so the configuration can report the problem before a
// table is rendered.
func (s Scan) Validate() error {
	if !s.Enabled {
		return nil
	}
	if s.Attempts < 2 {
		return fmt.Errorf("the scan threshold must be at least 2 attempts, got %d: a single failure must never ban", s.Attempts)
	}
	if _, err := s.unit(); err != nil {
		return err
	}
	if s.Remember <= 0 {
		return fmt.Errorf("the scan memory must be a positive duration, got %s", formatDuration(s.Remember))
	}
	return nil
}

// unit is the nftables rate unit the window has to be exactly one of. nftables can only express a
// rate as N per second/minute/hour/day/week; a window of ten minutes has no spelling, and pretending
// otherwise would silently change the threshold.
func (s Scan) unit() (string, error) {
	switch s.Window {
	case time.Second:
		return "second", nil
	case time.Minute:
		return "minute", nil
	case time.Hour:
		return "hour", nil
	case 24 * time.Hour:
		return "day", nil
	case 7 * 24 * time.Hour:
		return "week", nil
	}
	return "", fmt.Errorf("the scan window must be exactly 1s, 1m, 1h, 1d or 1w, got %s: "+
		"nftables expresses a rate as N per one unit and cannot express %s", formatDuration(s.Window), formatDuration(s.Window))
}

// rate renders the limit expression's rate, e.g. "5/minute".
func (s Scan) rate() (string, error) {
	unit, err := s.unit()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d/%s", s.Attempts, unit), nil
}

// Table is one component's ruleset: the allowed ports, the optional SSH restriction, whether the rest
// of the inbound traffic is dropped, and the scan detector.
type Table struct {
	// Name is the nftables table name, NodeTable or ServerTable.
	Name string
	// Ports are the inbound TCP ports this component publishes. SSHPort must be among them.
	Ports []int
	// UDPPorts are inbound UDP ports it publishes as well. A QUIC transport needs one, and an unlisted
	// UDP port falls through to the scan meter, which bans the participants who use it. The same number
	// in both lists is normal: 443 serves TLS over TCP and QUIC over UDP.
	UDPPorts []int
	// SSHFrom restricts SSHPort to these networks. Empty means "from anywhere".
	SSHFrom []netip.Prefix
	// DefaultDeny drops whatever the rules above did not accept. Off is for an operator who runs
	// something else on the box and knows it.
	DefaultDeny bool
	// Scan adds the kernel's scan detector, which the guard reads.
	Scan Scan
}

// Validate reports every problem with the table's definition, so one run tells the operator all of
// them.
func (t Table) Validate() error {
	var problems []error
	if err := validateName(t.Name); err != nil {
		problems = append(problems, err)
	}
	if len(t.Ports) == 0 {
		problems = append(problems, errors.New("no ports: the table would publish nothing"))
	}
	seen := make(map[int]bool, len(t.Ports))
	for _, port := range t.Ports {
		if port < 1 || port > 65535 {
			problems = append(problems, fmt.Errorf("port %d is outside 1-65535", port))
		}
		if seen[port] {
			problems = append(problems, fmt.Errorf("port %d is listed twice", port))
		}
		seen[port] = true
	}
	seenUDP := make(map[int]bool, len(t.UDPPorts))
	for _, port := range t.UDPPorts {
		if port < 1 || port > 65535 {
			problems = append(problems, fmt.Errorf("udp port %d is outside 1-65535", port))
		}
		if seenUDP[port] {
			problems = append(problems, fmt.Errorf("udp port %d is listed twice", port))
		}
		seenUDP[port] = true
	}
	if len(t.SSHFrom) > 0 && !seen[SSHPort] {
		problems = append(problems, fmt.Errorf("ssh_from is set but port %d is not published, so SSH would be closed", SSHPort))
	}
	for _, prefix := range t.SSHFrom {
		if !prefix.IsValid() {
			problems = append(problems, fmt.Errorf("ssh_from %q is not a valid network", prefix))
		} else if prefix != prefix.Masked() {
			problems = append(problems, fmt.Errorf("ssh_from %q has host bits set; did you mean %s", prefix, prefix.Masked()))
		}
	}
	if err := t.Scan.Validate(); err != nil {
		problems = append(problems, err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("firewall table %q: %w", t.Name, errors.Join(problems...))
	}
	return nil
}

// validateName keeps the name to what nftables accepts and what a shell cannot be confused by. The
// table is rendered as text, so a name carrying a brace would be an injection into the ruleset.
func validateName(name string) error {
	if name == "" {
		return errors.New("the table needs a name")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("table name %q is longer than %d characters", name, maxNameLen)
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
		if !ok || i == 0 && (r < 'a' || r > 'z') {
			return fmt.Errorf("table name %q must be lower-case letters, digits and underscores, starting with a letter", name)
		}
	}
	return nil
}

// Prefixes parses and normalises a list of CIDRs or single addresses. A single address is a /32 or
// /128 network, which is what an operator writing ssh_from means.
func Prefixes(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := ParsePrefix(value)
		if err != nil {
			return nil, err
		}
		out = append(out, prefix)
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int { return strings.Compare(a.String(), b.String()) })
	return slices.Compact(out), nil
}

// ParsePrefix accepts a CIDR or a bare address.
func ParsePrefix(value string) (netip.Prefix, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return netip.Prefix{}, errors.New("firewall: empty network")
	}
	if prefix, err := netip.ParsePrefix(trimmed); err == nil {
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("firewall: %q is not a CIDR or an address", value)
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// Render produces the ruleset `nft -f -` applies. The first line destroys the table, so applying the
// same ruleset twice replaces it instead of merging, and a half-written table never exists: nft
// commits one batch or nothing.
func (t Table) Render() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Termit firewall: the table `termit-uv firewall on` applied. Do not edit by hand.\n")
	fmt.Fprintf(&b, "# `termit-uv firewall off` removes it and leaves the host as it was.\n")
	fmt.Fprintf(&b, "destroy table inet %s\n\n", t.Name)
	fmt.Fprintf(&b, "table inet %s {\n", t.Name)

	t.writeSet(&b, "blocked4", "ipv4_addr", "flags interval", "auto-merge")
	t.writeSet(&b, "blocked6", "ipv6_addr", "flags interval", "auto-merge")
	t.writeSet(&b, "banned4", "ipv4_addr", "flags timeout")
	t.writeSet(&b, "banned6", "ipv6_addr", "flags timeout")
	if t.Scan.Enabled {
		memory := formatDuration(t.Scan.Remember)
		t.writeSet(&b, "scan4", "ipv4_addr", "flags dynamic, timeout", "timeout "+memory)
		t.writeSet(&b, "scan6", "ipv6_addr", "flags dynamic, timeout", "timeout "+memory)
	}
	if len(t.SSHFrom) > 0 {
		t.writeSSHSets(&b)
	}

	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority -5; policy accept;\n\n")
	b.WriteString("\t\t# Loopback and connections that already carry traffic are never a decision.\n")
	b.WriteString("\t\tiif lo accept\n")
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t\tct state invalid drop\n\n")
	b.WriteString("\t\t# The public list first, then what the guard banned. Both are sets the tools maintain.\n")
	b.WriteString("\t\tip saddr @blocked4 drop\n")
	b.WriteString("\t\tip6 saddr @blocked6 drop\n")
	b.WriteString("\t\tip saddr @banned4 drop\n")
	b.WriteString("\t\tip6 saddr @banned6 drop\n\n")
	b.WriteString("\t\t# ICMPv6 is not optional for IPv6: without NDP and PMTU messages the family dies.\n")
	b.WriteString("\t\ticmpv6 type { nd-router-solicit, nd-router-advert, nd-neighbor-solicit,\n")
	b.WriteString("\t\t              nd-neighbor-advert, packet-too-big, destination-unreachable,\n")
	b.WriteString("\t\t              time-exceeded } accept\n\n")
	b.WriteString("\t\t# The ports this host publishes.\n")
	if len(t.SSHFrom) == 0 {
		fmt.Fprintf(&b, "\t\ttcp dport %s accept\n", portList(t.Ports))
	} else {
		if hasPort(t.Ports, SSHPort) {
			fmt.Fprintf(&b, "\t\ttcp dport %d ip saddr @ssh4 accept\n", SSHPort)
			fmt.Fprintf(&b, "\t\ttcp dport %d ip6 saddr @ssh6 accept\n", SSHPort)
			fmt.Fprintf(&b, "\t\t# Not from the list: the restriction holds whether or not the rest is denied.\n")
			fmt.Fprintf(&b, "\t\ttcp dport %d drop\n", SSHPort)
		}
		if others := withoutPort(t.Ports, SSHPort); len(others) > 0 {
			fmt.Fprintf(&b, "\t\ttcp dport %s accept\n", portList(others))
		}
	}
	if len(t.UDPPorts) > 0 {
		fmt.Fprintf(&b, "\t\tudp dport %s accept\n", portList(t.UDPPorts))
	}
	if t.Scan.Enabled {
		rate, err := t.Scan.rate()
		if err != nil {
			return nil, err
		}
		memory := formatDuration(t.Scan.Remember)
		b.WriteString("\n\t\t# Everything else is counted per source in the kernel. The meter's own set is the rate\n")
		b.WriteString("\t\t# state; the add remembers the offender for the guard, which decides the ban.\n")
		fmt.Fprintf(&b, "\t\tmeter scan4m { ip saddr limit rate over %s } add @scan4 { ip saddr timeout %s }\n",
			rate, memory)
		fmt.Fprintf(&b, "\t\tmeter scan6m { ip6 saddr limit rate over %s } add @scan6 { ip6 saddr timeout %s }\n",
			rate, memory)
	}
	if t.DefaultDeny {
		b.WriteString("\n\t\tcounter drop\n")
	}
	b.WriteString("\t}\n}\n")
	return []byte(b.String()), nil
}

// writeSet renders one set declaration, one option per line.
func (t Table) writeSet(b *strings.Builder, name, kind string, options ...string) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n", name, kind)
	for _, option := range options {
		fmt.Fprintf(b, "\t\t%s\n", option)
	}
	b.WriteString("\t}\n\n")
}

// writeSSHSets renders the two sets ssh_from is compiled into, with its networks as elements: they
// belong to the table's definition, so the restriction survives a rebuild without the guard running.
func (t Table) writeSSHSets(b *strings.Builder) {
	for _, family := range []struct {
		set   string
		kind  string
		match func(netip.Prefix) bool
	}{
		{"ssh4", "ipv4_addr", func(p netip.Prefix) bool { return p.Addr().Is4() }},
		{"ssh6", "ipv6_addr", func(p netip.Prefix) bool { return p.Addr().Is6() && !p.Addr().Is4In6() }},
	} {
		elements := make([]string, 0, len(t.SSHFrom))
		for _, prefix := range t.SSHFrom {
			if family.match(prefix) {
				elements = append(elements, prefix.String())
			}
		}
		options := []string{"flags interval"}
		if len(elements) > 0 {
			options = append(options, "elements = { "+strings.Join(elements, ", ")+" }")
		}
		t.writeSet(b, family.set, family.kind, options...)
	}
}

// portList renders one port or a set of them, sorted and free of duplicates.
func portList(ports []int) string {
	sorted := append([]int(nil), ports...)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	if len(sorted) == 1 {
		return fmt.Sprintf("%d", sorted[0])
	}
	parts := make([]string, len(sorted))
	for i, port := range sorted {
		parts[i] = fmt.Sprintf("%d", port)
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func hasPort(ports []int, port int) bool {
	return slices.Contains(ports, port)
}

func withoutPort(ports []int, port int) []int {
	out := make([]int, 0, len(ports))
	for _, candidate := range ports {
		if candidate != port {
			out = append(out, candidate)
		}
	}
	return out
}

// formatDuration renders a duration the way nftables' time parser wants it: one unit, no compound
// forms, so "90m" is never written as "1h30m".
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	if d < 0 {
		return d.String()
	}
	for _, unit := range []struct {
		size time.Duration
		name string
	}{
		{7 * 24 * time.Hour, "w"},
		{24 * time.Hour, "d"},
		{time.Hour, "h"},
		{time.Minute, "m"},
		{time.Second, "s"},
	} {
		if d%unit.size == 0 {
			return fmt.Sprintf("%d%s", d/unit.size, unit.name)
		}
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
