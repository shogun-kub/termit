// Package sub renders a node's peer list as a subscription.
//
// Two formats, one peer set: a base64 blob of `vless://` URIs, which is what most clients paste, and a
// sing-box profile, which carries the same peers plus the routing a client needs to keep direct
// traffic direct. Both are ordered the same way and both are byte-stable for the same input, because
// the response is cached by ETag.
package sub

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shogun-kub/termit/internal/proto"
)

// Peer is one node as it appears in a subscription.
type Peer struct {
	// ID orders peers that share a label, and keeps the output stable.
	ID     string
	Label  string
	Domain string
	UUID   string
	Path   string
	// Degraded marks a node that is still in the list but currently failing its checks.
	Degraded bool
}

// Options configure the web links.
type Options struct {
	// SubscriptionURL is the client-facing URL of the plain list; the JSON format lives beside it.
	SubscriptionURL string
}

// Ordered returns the peers in subscription order: label, then id. The order is part of the contract:
// the same peer set must produce the same bytes.
func Ordered(peers []Peer) []Peer {
	out := append([]Peer(nil), peers...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RenderList returns the base64 body of the plain subscription: one `vless://` URI per line.
func RenderList(peers []Peer) (string, error) {
	ordered := Ordered(peers)
	lines := make([]string, 0, len(ordered))
	for _, peer := range ordered {
		label := peer.Label
		if label == "" {
			label = peer.Domain
		}
		if peer.Degraded {
			label += " (degraded)"
		}
		raw, err := proto.BuildVLESSUrl(proto.Profile{
			Domain: peer.Domain,
			UUID:   peer.UUID,
			Path:   peer.Path,
			Label:  label,
		})
		if err != nil {
			return "", fmt.Errorf("sub: build the URI for %s: %w", peer.Domain, err)
		}
		lines = append(lines, raw)
	}
	// A trailing newline is what every client tolerates, and it keeps a one-peer list from looking
	// like a truncated file.
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	return base64.StdEncoding.EncodeToString([]byte(body)), nil
}

// singBoxProfile is the subset of the sing-box format a client needs: one outbound per peer, a
// selector over them, and a direct outbound so that traffic the rules do not match stays direct.
type singBoxProfile struct {
	Log       map[string]any `json:"log"`
	DNS       map[string]any `json:"dns"`
	Inbounds  []any          `json:"inbounds"`
	Outbounds []any          `json:"outbounds"`
	Route     map[string]any `json:"route"`
	// SubscriptionURL is ours, not sing-box's: it lets a client find the plain list again.
	SubscriptionURL string `json:"termit_subscription_url,omitempty"`
}

// RenderJSON returns the sing-box profile for the same peers.
func RenderJSON(peers []Peer, opts Options) ([]byte, error) {
	ordered := Ordered(peers)
	outbounds := make([]any, 0, len(ordered)+2)
	tags := make([]string, 0, len(ordered))
	for _, peer := range ordered {
		tag := peer.Label
		if tag == "" {
			tag = peer.Domain
		}
		if peer.Degraded {
			tag += " (degraded)"
		}
		tags = append(tags, tag)
		outbounds = append(outbounds, map[string]any{
			"type":        "vless",
			"tag":         tag,
			"server":      peer.Domain,
			"server_port": proto.Port,
			"uuid":        peer.UUID,
			"flow":        "",
			"tls": map[string]any{
				"enabled":     true,
				"server_name": peer.Domain,
				"alpn":        []string{"h2"},
			},
			"transport": map[string]any{
				"type": "xhttp",
				"path": peer.Path,
				"mode": proto.Mode,
			},
		})
	}
	outbounds = append(outbounds, map[string]any{
		"type":      "selector",
		"tag":       "proxy",
		"outbounds": append(append([]string(nil), tags...), "direct"),
	})
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "direct"})

	profile := singBoxProfile{
		Log: map[string]any{"level": "warn"},
		DNS: map[string]any{
			"servers": []any{
				map[string]any{"tag": "remote", "detour": "proxy"},
				map[string]any{"tag": "local"},
			},
		},
		Inbounds: []any{
			map[string]any{"type": "tun", "tag": "tun-in", "stack": "system", "auto_route": true},
		},
		Outbounds: outbounds,
		Route: map[string]any{
			"rules": []any{
				// The mesh is for the open internet; private addresses stay on the local network.
				map[string]any{"ip_cidr": []string{"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12"}, "outbound": "direct"},
				map[string]any{"domain_suffix": []string{".local", ".lan"}, "outbound": "direct"},
			},
			"final": "proxy",
		},
		SubscriptionURL: opts.SubscriptionURL,
	}
	body, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sub: encode the sing-box profile: %w", err)
	}
	return body, nil
}

// ETag is a strong validator for a rendered body: the same peers and the same format always produce
// the same tag, so a client that keeps it saves the transfer.
func ETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}
