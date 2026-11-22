package server_test

import (
	"context"
	"slices"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
)

// The transport record follows the report, in both directions. A node that turns the transport on must
// appear in the mesh with its second link at its next sync, and a node that turns it off must lose it —
// the second half is the one a "write only when something was announced" rule got wrong: the old row
// stayed for ever, so the mesh kept handing out a link the node no longer served.
func TestSyncRecordsWhatTheNodeServesInBothDirections(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	if response := f.sync(t, f.agentKey, `{"agent_version":"v0.5.1",
		"transports":[{"kind":"vless","version":1},{"kind":"hysteria2","version":1}]}`); response.Code != 200 {
		t.Fatalf("sync with the transport: %d %s", response.Code, response.Body.String())
	}
	assertKinds(t, f, ctx, []string{proto.KindVLESS, proto.KindHysteria2})

	// The node retires it: no `transports` field at all, which is what every build that serves only the
	// TLS profile sends.
	if response := f.sync(t, f.agentKey, `{"agent_version":"v0.5.1"}`); response.Code != 200 {
		t.Fatalf("sync without the transport: %d %s", response.Code, response.Body.String())
	}
	assertKinds(t, f, ctx, []string{proto.KindVLESS})
}

// A kind this server cannot render is dropped, but it must not erase the kinds it can: the node reports
// what it serves, the server keeps what it understands.
func TestSyncKeepsTheKnownKindsOfAReportItHalfUnderstands(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	if response := f.sync(t, f.agentKey, `{"agent_version":"v0.5.1",
		"transports":[{"kind":"vless","version":1},{"kind":"quic-something","version":1}]}`); response.Code != 200 {
		t.Fatalf("sync: %d %s", response.Code, response.Body.String())
	}
	assertKinds(t, f, ctx, []string{proto.KindVLESS})
}

func assertKinds(t *testing.T, f *fixture, ctx context.Context, want []string) {
	t.Helper()
	served, err := f.store.UVTransports(ctx)
	if err != nil {
		t.Fatalf("UVTransports: %v", err)
	}
	got := slices.Clone(served[f.nodeID])
	if len(got) == 0 {
		got = []string{proto.KindVLESS}
	}
	// The store hands the kinds back in its own order; the set is what matters.
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the node serves %v, want %v (all: %v)", got, want, served)
	}
}
