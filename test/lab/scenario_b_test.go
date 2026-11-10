//go:build lab

package lab_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestScenarioB is the M3.4 acceptance: a node is destroyed and rebuilt from nothing, keeps its agent
// key and therefore its id, pair credentials and place in the mesh, and is admitted again only when
// the owner asks for it with the new endpoint.
func TestScenarioB(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	uv1, uv2 := l.UV1, l.UV2
	uv1Domain := statusDomain(t, ctx, uv1)
	uv2Domain := statusDomain(t, ctx, uv2)
	ids := nodeIDs(t, ctx, l)
	uv1ID, uv2ID := ids[uv1Domain], ids[uv2Domain]
	nodeCount := len(ids)
	if uv1ID == "" || uv2ID == "" {
		t.Fatalf("the server does not list both nodes (domains %s, %s; listed %v)", uv1Domain, uv2Domain, ids)
	}

	// The key is a secret: it goes to a 0600 file and is never logged. The config copy is what lets a
	// rebuilt box reach the same server without a flag that does not exist for mn.url.
	const (
		keyFile    = "/root/termit-rebuild.key"
		configCopy = "/root/termit-rebuild.yaml"
	)

	t.Cleanup(func() { restoreScenarioB(t, l, uv2ID, keyFile, configCopy) })
	t.Cleanup(func() {
		if t.Failed() {
			dump(t, l)
		}
	})

	var oldIdentity, oldPath, newIdentity, newPath string

	t.Run("the node is blocked and its key is saved before the rebuild", func(t *testing.T) {
		if out, err := l.MNAdmin(ctx, "uv", "block", uv2ID); err != nil {
			t.Fatalf("block: %v\n%s", err, out)
		}
		if got := stateOf(t, ctx, l, uv2ID); got != "blocked" {
			t.Fatalf("uv2 is %s, want blocked", got)
		}
		oldIdentity = statusString(t, ctx, uv2, "identity_fingerprint")
		oldPath = identityField(t, ctx, uv2, "xhttp_path")

		key := agentKey(t, ctx, l, uv2ID)
		if err := uv2.WriteFile(ctx, []byte(key+"\n"), keyFile, "0600"); err != nil {
			t.Fatalf("%v", err)
		}
		if out, err := uv2.Run(ctx, "cp /etc/termit/uv/config.yaml "+configCopy); err != nil {
			t.Fatalf("save the config: %v\n%s", err, out)
		}
		t.Logf("saved the agent key to %s and the configuration to %s", keyFile, configCopy)
	})

	t.Run("a fresh install with --relocate keeps the node and mints a new identity", func(t *testing.T) {
		if out, err := uv2.Unit(ctx, "stop", "termit-uv-agent", "termit-xray"); err != nil {
			t.Fatalf("stop: %v\n%s", err, out)
		}
		// Repeated acceptance runs stop and start the units; systemd's start limit counts those, and a
		// tripped limit would fail the install for a reason that has nothing to do with the rebuild.
		if out, err := uv2.Run(ctx, "systemctl reset-failed termit-uv-agent termit-xray"); err != nil {
			t.Logf("reset-failed: %v\n%s", err, out)
		}
		out := uv2.MustRun(t, ctx, "termit-uv uninstall --purge")
		t.Logf("uninstall: %s", oneLine(out))

		uv2.MustRun(t, ctx, "install -d -m 0750 /etc/termit/uv")
		uv2.MustRun(t, ctx, "install -m 0600 "+configCopy+" /etc/termit/uv/config.yaml")
		out = uv2.MustRun(t, ctx, "termit-uv init --config /etc/termit/uv/config.yaml --relocate --agent-key-file "+keyFile)
		t.Logf("init: %s", oneLine(out))
		if !strings.Contains(out, "relocated:") {
			t.Errorf("init did not report the relocated mode:\n%s", out)
		}

		newIdentity = statusString(t, ctx, uv2, "identity_fingerprint")
		if newIdentity == oldIdentity {
			t.Fatal("the rebuild kept the old identity")
		}
		if enrolled, _ := statusJSON(t, ctx, uv2)["enrolled"].(bool); !enrolled {
			t.Error("the rebuilt node is not enrolled: the agent key did not reach the state")
		}
		// The server must not have learned about the new box by itself: only the explicit unblock may
		// move a blocked node, and an init that enrolled again would create a second node.
		if got := stateOf(t, ctx, l, uv2ID); got != "blocked" {
			t.Fatalf("the server is %s after the rebuild; it must not have been touched", got)
		}
		if got := len(nodeIDs(t, ctx, l)); got != nodeCount {
			t.Fatalf("the server lists %d nodes; the rebuild must not enroll a new one", got)
		}
	})

	t.Run("the explicit unblock admits the new endpoint", func(t *testing.T) {
		out := uv2.MustRun(t, ctx, "termit-uv unblock")
		t.Logf("unblock:\n%s", out)
		if got := stateOf(t, ctx, l, uv2ID); got != "active" {
			t.Fatalf("uv2 is %s after the unblock, want active", got)
		}

		newPath = identityField(t, ctx, uv2, "xhttp_path")
		if newPath == oldPath {
			t.Fatal("the rebuild kept the old path")
		}
		for _, m := range []*lab.Machine{uv2, uv1} {
			syncOut := m.MustRun(t, ctx, "termit-uv sync")
			t.Logf("%s sync: %s", m.Name, oneLine(syncOut))
		}
		assertPeerAccepted(t, ctx, uv1, uv2ID, true)
		assertPeerAccepted(t, ctx, uv2, uv1ID, true)

		// The peer's subscription must carry the rebuilt path and no longer the old one. The pair
		// credential is derived from the node ids and must survive the rebuild untouched; the path is
		// the part of the endpoint that changed. The JSON rendering carries the path literally, not
		// percent-encoded like the vless:// list.
		body := subscriptionBody(t, ctx, uv1)
		if !strings.Contains(body, "/"+newPath+"/") {
			t.Error("uv1's subscription does not carry the rebuilt path")
		}
		if strings.Contains(body, "/"+oldPath+"/") {
			t.Error("uv1's subscription still carries the old path")
		}
		if !contains(xrayEmails(t, ctx, uv2), "probe@termit") {
			t.Error("the first sync after the unblock did not restore the server's probe credential")
		}
	})

	t.Run("a live node refuses to be relocated", func(t *testing.T) {
		// The rebuild above left a working node with a state file. Running --relocate on it again must
		// stop before it mints a new identity, or an accidental second run would break the node.
		out, err := uv2.Run(ctx, "termit-uv init --config /etc/termit/uv/config.yaml --relocate --agent-key-file "+keyFile)
		if err == nil {
			t.Fatalf("init --relocate rewrote a live node:\n%s", out)
		}
		if !strings.Contains(out, "already has an identity") {
			t.Errorf("the refusal does not explain itself:\n%s", out)
		}
		if got := statusString(t, ctx, uv2, "identity_fingerprint"); got != newIdentity {
			t.Error("the refused init changed the identity")
		}
	})
}

// identityField reads one field of the node identity. The value may be a credential; callers compare
// it and never log it.
func identityField(t *testing.T, ctx context.Context, m *lab.Machine, field string) string {
	t.Helper()
	out := m.MustRun(t, ctx, `python3 -c 'import json; print(json.load(open("/var/lib/termit/uv/state.json"))["identity"]["`+field+`"])'`)
	return strings.TrimSpace(out)
}

// agentKey fetches the node's agent key from the server, which is the only place it survives after the
// rebuild. It is written to a 0600 file and never printed.
func agentKey(t *testing.T, ctx context.Context, l *lab.Lab, id string) string {
	t.Helper()
	out := l.MustMNAdmin(t, ctx, "uv", "agentkey", id)
	key := field(out, "agent key:")
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		t.Fatalf("cannot read the agent key for %s", id)
	}
	return key
}

// restoreScenarioB brings the lab back to a converged state and removes the secret it wrote. It also
// finishes a rebuild that died half-way: a node with no state cannot be unblocked, and a machine left
// uninstalled would need a manual repair before the next run.
func restoreScenarioB(t *testing.T, l *lab.Lab, uv2ID, keyFile, configCopy string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	if _, err := l.UV2.Run(ctx, "test -f /var/lib/termit/uv/state.json"); err != nil {
		out, err := l.UV2.Run(ctx, "test -f "+keyFile+" && termit-uv init --config /etc/termit/uv/config.yaml --relocate --agent-key-file "+keyFile)
		if err != nil {
			t.Logf("cleanup: the rebuilt node is still uninstalled: %v\n%s", err, lab.Tail(out, 8))
		}
	}
	_, _ = l.UV2.Run(ctx, "systemctl reset-failed termit-xray termit-uv-agent")
	for _, unit := range []string{"termit-xray", "termit-uv-agent"} {
		if out, err := l.UV2.Unit(ctx, "start", unit); err != nil {
			t.Logf("cleanup: start %s: %v\n%s", unit, err, lab.Tail(out, 5))
		}
	}
	if out, err := l.MNAdmin(ctx, "uv", "unblock", uv2ID); err != nil {
		t.Logf("cleanup: admin unblock: %v\n%s", err, lab.Tail(out, 5))
	}
	for _, m := range []*lab.Machine{l.UV2, l.UV1} {
		if out, err := m.Run(ctx, "termit-uv sync"); err != nil {
			t.Logf("cleanup: %s sync: %v\n%s", m.Name, err, lab.Tail(out, 5))
		}
	}
	if out, err := l.UV2.Run(ctx, "rm -f "+keyFile+" "+configCopy); err != nil {
		t.Logf("cleanup: remove the key file: %v\n%s", err, out)
	}
}
