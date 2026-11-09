// Package dns is the resolver the enrollment and prober checks use.
//
// Two resolvers, because one resolver's answer is one resolver's mistake: the system resolver is what
// the machine itself believes, and a second one is asked independently. A resolver that cannot answer
// is not evidence against a node — the caller counts answers, not errors.
package dns

import (
	"context"
	"fmt"
	"net"
	"time"
)

// System uses the host's configured resolver.
type System struct{}

// Lookup resolves domain.
func (System) Lookup(ctx context.Context, domain string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", domain)
}

// Server asks one DNS server directly, bypassing the host configuration.
type Server struct {
	// Addr is host:port of the resolver, for example 1.1.1.1:53.
	Addr string
}

// Lookup resolves domain.
func (s Server) Lookup(ctx context.Context, domain string) ([]net.IP, error) {
	if s.Addr == "" {
		return nil, fmt.Errorf("dns: no server address")
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, "udp", s.Addr)
		},
	}
	return resolver.LookupIP(ctx, "ip", domain)
}
