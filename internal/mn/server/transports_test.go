package server_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/version"
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

// The node has to know which build its server runs, and the sync answer is where it learns: a menu offers
// exactly this version as an update target, because the server is what the mesh was proved against.
func TestSyncTellsTheNodeWhichBuildTheServerRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	response := f.sync(t, f.agentKey, `{"agent_version":"v0.1.0"}`)
	if response.Code != 200 {
		t.Fatalf("sync: %d %s", response.Code, response.Body.String())
	}
	var answer proto.SyncResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}
	if answer.ServerVersion != version.Version {
		t.Errorf("server_version = %q, want %q", answer.ServerVersion, version.Version)
	}
}

// The floor is declared to every node so it can report itself below it, and a server that names none says
// nothing at all: the field is omitted from the JSON, not sent empty.
func TestSyncDeclaresTheMinimumAgentVersion(t *testing.T) {
	t.Parallel()
	f := newFixtureWith(t, okProber{}, func(opts *server.Options) { opts.MinAgentVersion = "v0.7.0" })
	response := f.sync(t, f.agentKey, `{"agent_version":"v0.6.0"}`)
	if response.Code != 200 {
		t.Fatalf("sync: %d %s", response.Code, response.Body.String())
	}
	var answer proto.SyncResponse
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}
	if answer.MinAgentVersion != "v0.7.0" {
		t.Errorf("min_agent_version = %q, want v0.7.0", answer.MinAgentVersion)
	}

	plain := newFixture(t)
	body := plain.sync(t, plain.agentKey, `{"agent_version":"v0.6.0"}`).Body.String()
	if strings.Contains(body, "min_agent_version") {
		t.Errorf("a server that names no floor still sent the field: %s", body)
	}
}
