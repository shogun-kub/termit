//go:build lab

package lab_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestMeshThreeNodes is the acceptance a two-node lab cannot express. With three nodes every node
// has two pairs, so the tests can tell "the dead peer was removed" from "everything was removed",
// check that the pair credentials are pairwise distinct and symmetric, and see that the pair
// between the two surviving nodes is untouched while the third is blocked.
//
// It needs TERMIT_LAB_UV3 and skips without it: the rest of the suite still runs on two nodes.
func TestMeshThreeNodes(t *testing.T) {
	l := lab.FromEnv(t)
	if l.UV3 == nil {
		t.Skip("TERMIT_LAB_UV3 is not set; the mesh test needs a third node")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	uvs := []*lab.Machine{l.UV1, l.UV2, l.UV3}
	domains := map[string]string{}
	for _, m := range uvs {
		domains[m.Name] = statusDomain(t, ctx, m)
	}
	ids := nodeIDs(t, ctx, l)
	byName := map[string]string{}
	for _, m := range uvs {
		byName[m.Name] = ids[domains[m.Name]]
		if byName[m.Name] == "" {
			t.Fatalf("the server does not list %s (%s); listed %v", m.Name, domains[m.Name], ids)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("the server lists %d nodes, want 3: %v", len(ids), ids)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if out, err := l.MNAdmin(cleanupCtx, "uv", "unblock", byName["uv3"]); err != nil {
			t.Logf("cleanup: unblock uv3: %v\n%s", err, lab.Tail(out, 5))
		}
		for _, m := range uvs {
			if out, err := m.Run(cleanupCtx, "termit-uv sync"); err != nil {
				t.Logf("cleanup: %s sync: %v\n%s", m.Name, err, lab.Tail(out, 5))
			}
		}
	})
	t.Cleanup(func() {
		if t.Failed() {
			dump(t, l)
		}
	})

	// A converged mesh: every node runs and serves the other two.
	for _, m := range uvs {
		m.MustRun(t, ctx, "termit-uv sync")
		if got := stateOf(t, ctx, l, byName[m.Name]); got != "active" {
			t.Fatalf("%s is %s, want active", m.Name, got)
		}
	}

	t.Run("each node serves exactly its two peers", func(t *testing.T) {
		for _, m := range uvs {
			want := []string{"owner", "probe@termit"}
			for _, other := range uvs {
				if other == m {
					continue
				}
				want = append(want, peerEmail(byName[other.Name]))
			}
			sort.Strings(want)
			got := xrayEmails(t, ctx, m)
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s serves %v, want exactly %v", m.Name, got, want)
			}
		}
	})

	t.Run("the subscription has the self credential and two distinct pair credentials", func(t *testing.T) {
		pairs := map[string]string{} // "a|b" -> uuid
		uuids := map[string]string{} // every credential, to prove they differ
		for _, m := range uvs {
			self := identityField(t, ctx, m, "self_uuid")
			if self == "" {
				t.Fatalf("%s: the state has no self_uuid", m.Name)
			}
			uuids[self] = m.Name + " self"
			servers := subscriptionServers(t, ctx, m)
			want := []string{domains[m.Name]}
			for _, other := range uvs {
				if other == m {
					continue
				}
				want = append(want, domains[other.Name])
				uuid := subscriptionPeerUUID(t, ctx, m, domains[other.Name])
				if uuid == "" {
					t.Fatalf("%s: no credential for %s", m.Name, domains[other.Name])
				}
				if uuid == self {
					t.Fatalf("%s: the subscription gave its own credential for %s", m.Name, domains[other.Name])
				}
				key := pairKey(m.Name, other.Name)
				if previous, seen := pairs[key]; seen && previous != uuid {
					t.Errorf("%s and %s disagree about their pair credential", m.Name, other.Name)
				}
				pairs[key] = uuid
				uuids[uuid] = m.Name + " -> " + other.Name
			}
			sort.Strings(want)
			sort.Strings(servers)
			if strings.Join(servers, ",") != strings.Join(want, ",") {
				t.Errorf("%s subscription lists %v, want %v", m.Name, servers, want)
			}
		}
		if len(uuids) != 6 {
			t.Errorf("six credentials (three self and three pairs) must be distinct; got %d distinct values", len(uuids))
		}
	})

	t.Run("blocking one node leaves the other pair working", func(t *testing.T) {
		if out, err := l.MNAdmin(ctx, "uv", "block", byName["uv3"]); err != nil {
			t.Fatalf("block uv3: %v\n%s", err, out)
		}
		for _, m := range []*lab.Machine{l.UV1, l.UV2} {
			m.MustRun(t, ctx, "termit-uv sync")
		}

		// The blocked node is out of both configs; the surviving pair is still there.
		for _, m := range []*lab.Machine{l.UV1, l.UV2} {
			emails := xrayEmails(t, ctx, m)
			if contains(emails, peerEmail(byName["uv3"])) {
				t.Errorf("%s still serves the blocked node's credential", m.Name)
			}
		}
		assertPeerAccepted(t, ctx, l.UV1, byName["uv2"], true)
		assertPeerAccepted(t, ctx, l.UV2, byName["uv1"], true)
		assertPeerAccepted(t, ctx, l.UV1, byName["uv3"], false)

		// A blocked node keeps its own read-only list (QUESTIONS Q6.5): both peers, not itself.
		servers := subscriptionServers(t, ctx, l.UV3)
		if len(servers) != 2 || !contains(servers, domains["uv1"]) || !contains(servers, domains["uv2"]) {
			t.Errorf("uv3's subscription while blocked lists %v, want its two peers", servers)
		}
	})

	t.Run("unblocking it restores both pairs", func(t *testing.T) {
		if out, err := l.MNAdmin(ctx, "uv", "unblock", byName["uv3"]); err != nil {
			t.Fatalf("unblock uv3: %v\n%s", err, out)
		}
		for _, m := range uvs {
			m.MustRun(t, ctx, "termit-uv sync")
		}
		for _, m := range []*lab.Machine{l.UV1, l.UV2} {
			if !contains(xrayEmails(t, ctx, m), peerEmail(byName["uv3"])) {
				t.Errorf("%s did not get the restored node back", m.Name)
			}
		}
		if got := stateOf(t, ctx, l, byName["uv3"]); got != "active" {
			t.Errorf("uv3 is %s, want active", got)
		}
	})
}

// pairKey orders two machine names so one pair is stored once regardless of direction.
func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// subscriptionServers is the address of every outbound in a node's subscription.
func subscriptionServers(t *testing.T, ctx context.Context, m *lab.Machine) []string {
	t.Helper()
	body := subscriptionBody(t, ctx, m)
	var document struct {
		Outbounds []struct {
			Type   string `json:"type"`
			Server string `json:"server"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatalf("%s: subscription is not JSON: %v", m.Name, err)
	}
	var servers []string
	for _, outbound := range document.Outbounds {
		if outbound.Type == "vless" {
			servers = append(servers, outbound.Server)
		}
	}
	return servers
}
