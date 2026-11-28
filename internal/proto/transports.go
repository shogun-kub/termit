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

// The renderings of the VLESS profile: how a client speaks it through nginx. Both are the same profile —
// same name, same device, same secret path shape — and differ only in what the core listens for behind
// nginx, which is why a node may serve both and why the choice is an option, not a kind
// (docs/IMPLEMENTATION_PLAN.md §5.2.6).
const (
	// RenderingXHTTP is VLESS over XHTTP, the profile every node serves.
	RenderingXHTTP = "xhttp"
	// RenderingWebSocket is VLESS over WebSocket, for a client that cannot read the XHTTP profile.
	RenderingWebSocket = "websocket"
)

// Renderings are the renderings this build can write a URI for. A node may serve a newer one; a server
// drops what it does not know rather than writing a link that cannot work.
func Renderings() []string { return []string{RenderingXHTTP, RenderingWebSocket} }

// KnownRendering reports whether this build can render a name.
func KnownRendering(name string) bool {
	for _, known := range Renderings() {
		if name == known {
			return true
		}
	}
	return false
}

// Options are the parameters of a kind: what a node serves of it, beyond the kind's name and version. The
// field is additive — a server that does not know it ignores it, and a node that sends none serves what
// the kind always meant — so adding a parameter never needs a TransportVersion bump.
type Options struct {
	// Renderings are the renderings of the profile this node serves. Absent means the kind's default,
	// which is RenderingXHTTP.
	Renderings []string `json:"renderings,omitempty"`
}

// Transport is how a node says what it serves.
type Transport struct {
	Kind    string   `json:"kind"`
	Version int      `json:"version"`
	Options *Options `json:"options,omitempty"`
}

// Serves lists the renderings of a transport, defaulted and in a stable order.
func (t Transport) Serves() []string {
	if t.Options == nil || len(t.Options.Renderings) == 0 {
		return []string{RenderingXHTTP}
	}
	return t.Options.Renderings
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
	if t.Options != nil {
		for _, rendering := range t.Options.Renderings {
			if !KnownRendering(rendering) {
				return fmt.Errorf("proto: transport %q does not render %q", t.Kind, rendering)
			}
		}
	}
	return nil
}

// NodeTransports is what a node with this configuration serves: the TLS profile always, the QUIC
// transport when it is enabled. One place, so the sync request and anything else that has to describe a
// node cannot disagree. The renderings are the profile's own, and a node that serves only the default one
// sends none: an older server then reads exactly what it read before.
func NodeTransports(hysteria bool, renderings ...string) []Transport {
	tls := Transport{Kind: KindVLESS, Version: TransportVersion}
	if len(renderings) > 0 {
		tls.Options = &Options{Renderings: append([]string(nil), renderings...)}
	}
	out := []Transport{tls}
	if hysteria {
		out = append(out, Transport{Kind: KindHysteria2, Version: TransportVersion})
	}
	return out
}

// RenderingServed reports whether what a node reported includes one rendering of a kind. A node that
// announced no options serves the kind's default, which is how a node from before this field behaves.
func RenderingServed(transports []Transport, kind, rendering string) bool {
	for _, transport := range transports {
		if transport.Kind != kind {
			continue
		}
		for _, served := range transport.Serves() {
			if served == rendering {
				return true
			}
		}
	}
	return false
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
