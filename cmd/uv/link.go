package main

import (
	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// withFragmentation adds every form of client-side fragmentation this node was asked for. Happ reads
// `fragment=…` from the link; podkop-engine and the patched sing-box builds read `sb.tls.fragment=1`
// and `sb.tls.record_fragment=1`. The link is copied by whoever holds it, and the node cannot know
// which client they run, so it carries both dialects — a client ignores a parameter it does not know.
//
// One knob turns them all on: a node whose configuration carries a Happ fragment is a node whose
// handshakes are meant to be split.
func withFragmentation(uri string, cfg *config.Config) string {
	if cfg.HappFragment == "" {
		return uri
	}
	return proto.AddSingBoxFragment(proto.AddHappFragment(uri, cfg.HappFragment), true, true)
}
