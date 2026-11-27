package firewall

import (
	"net/netip"
	"slices"
	"testing"
)

// TestDriftNamesWhatChanged: the record is what the boot unit restores, so a configuration that moved
// after the last `on` has no other way of becoming visible. The order inside a list must not count.
func TestDriftNamesWhatChanged(t *testing.T) {
	t.Parallel()
	applied := &Applied{
		Ports:       []int{22, 80, 443},
		UDPPorts:    []int{443},
		SSHFrom:     []string{"192.0.2.10/32"},
		DefaultDeny: true,
	}
	ssh := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}
	cases := []struct {
		name  string
		want  Table
		drift []string
	}{
		{"the same table, in another order", Table{
			Ports: []int{443, 80, 22}, UDPPorts: []int{443}, SSHFrom: ssh, DefaultDeny: true,
		}, nil},
		{"a UDP port the record lacks", Table{
			Ports: []int{22, 80, 443}, SSHFrom: ssh, DefaultDeny: true,
		}, []string{"udp_ports"}},
		{"a TCP port the record lacks", Table{
			Ports: []int{22, 80, 443, 8443}, UDPPorts: []int{443}, SSHFrom: ssh, DefaultDeny: true,
		}, []string{"ports"}},
		{"another SSH source", Table{
			Ports: []int{22, 80, 443}, UDPPorts: []int{443},
			SSHFrom: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}, DefaultDeny: true,
		}, []string{"ssh_from"}},
		{"default deny flipped", Table{
			Ports: []int{22, 80, 443}, UDPPorts: []int{443}, SSHFrom: ssh, DefaultDeny: false,
		}, []string{"default_deny"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Drift(applied, tc.want)
			if !slices.Equal(got, tc.drift) {
				t.Errorf("Drift = %v, want %v", got, tc.drift)
			}
		})
	}
	if got := Drift(nil, Table{}); got != nil {
		t.Errorf("Drift(nil) = %v, want nil", got)
	}
}
