package proto

import (
	"errors"
	"fmt"
	"strings"
)

// The kinds a node can serve. A kind names a client-facing transport, not a protocol version: the
// parameters that make it work live in the node's configuration, and Version below says which shape of
// them a node speaks.
const (
	// KindVLESS is the TLS profile: VLESS over XHTTP inside the TLS nginx terminates.
	KindVLESS = "vless"
	// KindHysteria2 is the QUIC transport on UDP/443.
	KindHysteria2 = "hysteria2"
)

// TransportVersion is the parameter shape this build speaks for every kind it serves. A future change to
// what a kind needs (a new required parameter, a different path shape) bumps it, and a server that does
// not know the newer number refuses the kind instead of rendering a link a client cannot use.
const TransportVersion = 1

// Transport is how a node says what it serves.
type Transport struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
}

// Validate reports whether the transport is one this build can name at all.
func (t Transport) Validate() error {
	switch t.Kind {
	case KindVLESS, KindHysteria2:
	default:
		return fmt.Errorf("proto: unknown transport kind %q", t.Kind)
	}
	if t.Version < 1 {
		return fmt.Errorf("proto: transport %q has version %d", t.Kind, t.Version)
	}
	return nil
}

// NodeTransports is what a node with this configuration serves: the TLS profile always, the QUIC
// transport when it is enabled. One place, so the sync request and anything else that has to describe a
// node cannot disagree.
func NodeTransports(hysteria bool) []Transport {
	out := []Transport{{Kind: KindVLESS, Version: TransportVersion}}
	if hysteria {
		out = append(out, Transport{Kind: KindHysteria2, Version: TransportVersion})
	}
	return out
}

// ServedKinds reduces what a node reported to the kinds a server of this version can render. An unknown
// kind or a newer version is dropped rather than refused: a node running a newer build must still be
// usable, and a link this server cannot write correctly is worse than no link.
func ServedKinds(transports []Transport) []string {
	kinds := make([]string, 0, len(transports))
	for _, transport := range transports {
		if transport.Version > TransportVersion {
			continue
		}
		switch transport.Kind {
		case KindVLESS, KindHysteria2:
			kinds = append(kinds, transport.Kind)
		}
	}
	return kinds
}

// ParseKinds is ServedKinds over a stored list of names, which is what the server keeps: the kinds a node
// reported, validated again because a database row can be edited by hand.
func ParseKinds(names []string) ([]string, error) {
	kinds := make([]string, 0, len(names))
	for _, name := range names {
		kind := strings.TrimSpace(name)
		if kind == "" {
			continue
		}
		transport := Transport{Kind: kind, Version: TransportVersion}
		if err := transport.Validate(); err != nil {
			return nil, err
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

// Serves reports whether a list of kinds includes one.
func Serves(kinds []string, kind string) bool {
	for _, name := range kinds {
		if name == kind {
			return true
		}
	}
	return false
}

// ErrNoTransport says a node reported nothing this server can serve, which would leave its participants
// with an empty subscription.
var ErrNoTransport = errors.New("proto: the node reported no transport this server can serve")
