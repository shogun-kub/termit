package proto_test

import (
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
)

// TestServedKindsKeepsOnlyWhatThisServerCanRender: a newer node may report a kind or a version this server
// does not know, and the answer is to drop it - rendering a link that cannot work is worse than none.
func TestServedKindsKeepsOnlyWhatThisServerCanRender(t *testing.T) {
	t.Parallel()
	got := proto.ServedKinds([]proto.Transport{
		{Kind: proto.KindVLESS, Version: proto.TransportVersion},
		{Kind: proto.KindHysteria2, Version: proto.TransportVersion},
		{Kind: "tuic", Version: proto.TransportVersion},
		{Kind: proto.KindHysteria2, Version: proto.TransportVersion + 1},
	})
	if len(got) != 2 || got[0] != proto.KindVLESS || got[1] != proto.KindHysteria2 {
		t.Fatalf("ServedKinds = %v", got)
	}
	if kinds := proto.NodeTransports(false); len(kinds) != 1 || kinds[0].Kind != proto.KindVLESS {
		t.Errorf("a node without the transport reports %v", kinds)
	}
	if kinds := proto.NodeTransports(true); len(kinds) != 2 {
		t.Errorf("a node with the transport reports %v", kinds)
	}
	if _, err := proto.ParseKinds([]string{"nonsense"}); err == nil {
		t.Error("ParseKinds accepted a kind this build does not know")
	}
}

// A node from before the field sends no options, and that must read as the kind's own default rather than
// as a node that serves nothing.
func TestATransportWithoutOptionsServesTheDefaultRendering(t *testing.T) {
	t.Parallel()
	plain := proto.Transport{Kind: proto.KindVLESS, Version: proto.TransportVersion}
	if got := plain.Serves(); len(got) != 1 || got[0] != proto.RenderingXHTTP {
		t.Errorf("Serves = %v, want the xhttp default", got)
	}
	if !proto.RenderingServed([]proto.Transport{plain}, proto.KindVLESS, proto.RenderingXHTTP) {
		t.Error("the default rendering is not reported as served")
	}
	if proto.RenderingServed([]proto.Transport{plain}, proto.KindVLESS, proto.RenderingWebSocket) {
		t.Error("a node that said nothing is reported as serving WebSocket")
	}
}

// A node that serves both says so, and the answer names what it can be asked for.
func TestNodeTransportsCarriesTheRenderings(t *testing.T) {
	t.Parallel()
	if got := proto.NodeTransports(false); got[0].Options != nil {
		t.Errorf("a node with the default rendering sends options: %+v", got[0].Options)
	}
	both := proto.NodeTransports(true, proto.RenderingXHTTP, proto.RenderingWebSocket)
	if len(both) != 2 || both[0].Options == nil {
		t.Fatalf("NodeTransports = %+v", both)
	}
	if !proto.RenderingServed(both, proto.KindVLESS, proto.RenderingWebSocket) {
		t.Error("the reported transports do not say the node serves WebSocket")
	}
	if proto.RenderingServed(both, proto.KindHysteria2, proto.RenderingWebSocket) {
		t.Error("the QUIC transport is reported as serving a VLESS rendering")
	}
}

func TestAnUnknownTransportRenderingIsRefused(t *testing.T) {
	t.Parallel()
	transport := proto.Transport{Kind: proto.KindVLESS, Version: proto.TransportVersion,
		Options: &proto.Options{Renderings: []string{"grpc"}}}
	if err := transport.Validate(); err == nil {
		t.Error("Validate accepted a rendering this build cannot write")
	}
	if proto.KnownRendering("grpc") {
		t.Error("KnownRendering accepted grpc")
	}
	for _, rendering := range proto.Renderings() {
		if !proto.KnownRendering(rendering) {
			t.Errorf("Renderings lists %q but KnownRendering refuses it", rendering)
		}
	}
}
