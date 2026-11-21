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
