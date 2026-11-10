//go:build lab

package lab_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestScenarioC is the M3.5 acceptance: the box moves to a new domain and keeps its agent key, so it
// keeps its id and every pair credential. The move is the event that admits it again, and the peers
// only see a new endpoint in their subscription.
//
// The new name is the same name with a "b-" added or removed, so repeated runs move the node between
// two names instead of growing one; the lab keeps whatever name the last run left it on.
func TestScenarioC(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	uv1, uv2 := l.UV1, l.UV2
	uv1Domain := statusDomain(t, ctx, uv1)
	oldDomain := statusDomain(t, ctx, uv2)
	newDomain := alternateName(oldDomain)

	ids := nodeIDs(t, ctx, l)
	uv1ID, uv2ID := ids[uv1Domain], ids[oldDomain]
	if uv1ID == "" || uv2ID == "" {
		t.Fatalf("the server does not list both nodes (domains %s, %s; listed %v)", uv1Domain, oldDomain, ids)
	}
	// The number of nodes the mesh started with: the move must not add or drop one, whatever the
	// lab has (two UVs, or three when the optional third node is configured).
	nodeCount := len(ids)
	var uv3ID, uv3Domain string
	if l.UV3 != nil {
		uv3Domain = statusDomain(t, ctx, l.UV3)
		uv3ID = ids[uv3Domain]
		if uv3ID == "" {
			t.Fatalf("the server does not list uv3 (%s); listed %v", uv3Domain, ids)
		}
	}

	// The key is a secret: it goes to a 0600 file and is never logged. The config copy lets a rebuilt
	// box reach the same server without passing credentials on a command line.
	const (
		keyFile    = "/root/termit-move.key"
		configCopy = "/root/termit-move.yaml"
	)

	var addedMN, addedUV2 bool
	t.Cleanup(func() {
		restoreScenarioC(t, l, uv2ID, keyFile, configCopy, oldDomain, newDomain, addedMN, addedUV2)
	})
	t.Cleanup(func() {
		if t.Failed() {
			dump(t, l)
		}
	})

	var oldIdentity, oldPath, newIdentity, newPath, oldPeerUUID string
	var oldPeerUV3, oldPeerUV2UV3 string

	t.Run("the pair credential and the old endpoint are recorded", func(t *testing.T) {
		sync := []*lab.Machine{uv1, uv2}
		if l.UV3 != nil {
			sync = append(sync, l.UV3)
		}
		for _, m := range sync {
			m.MustRun(t, ctx, "termit-uv sync")
		}
		oldPeerUUID = subscriptionPeerUUID(t, ctx, uv1, oldDomain)
		if oldPeerUUID == "" {
			t.Fatal("uv1's subscription does not carry uv2's pair credential to compare against")
		}
		if l.UV3 != nil {
			// The pair uv2 shares with the third node must survive uv2's rebuild, and uv1's pair with
			// that node must not be disturbed by uv2's move.
			oldPeerUV3 = subscriptionPeerUUID(t, ctx, uv1, uv3Domain)
			oldPeerUV2UV3 = subscriptionPeerUUID(t, ctx, l.UV3, oldDomain)
			if oldPeerUV3 == "" || oldPeerUV2UV3 == "" {
				t.Fatal("the third node's pair credentials are not in the subscriptions to compare against")
			}
		}
		oldIdentity = statusString(t, ctx, uv2, "identity_fingerprint")
		oldPath = identityField(t, ctx, uv2, "xhttp_path")
	})

	t.Run("the node is blocked and its key is saved before the move", func(t *testing.T) {
		if out, err := l.MNAdmin(ctx, "uv", "block", uv2ID); err != nil {
			t.Fatalf("block: %v\n%s", err, out)
		}
		if got := stateOf(t, ctx, l, uv2ID); got != "blocked" {
			t.Fatalf("uv2 is %s, want blocked", got)
		}
		key := agentKey(t, ctx, l, uv2ID)
		if err := uv2.WriteFile(ctx, []byte(key+"\n"), keyFile, "0600"); err != nil {
			t.Fatalf("%v", err)
		}
		if out, err := uv2.Run(ctx, "cp /etc/termit/uv/config.yaml "+configCopy); err != nil {
			t.Fatalf("save the config: %v\n%s", err, out)
		}
		t.Logf("saved the agent key to %s and the configuration to %s", keyFile, configCopy)
	})

	t.Run("the new name resolves", func(t *testing.T) {
		// The verification believes DNS and then proves the box, so the name has to point at the node
		// before the move means anything. The server needs it for the DNS check, the certificate
		// check, the prober and the CA; the node needs it because its core resolves the HTTP Host a
		// client sends, and the canary sends the node's own domain.
		ip := showField(t, ctx, l, uv2ID, "ip:")
		if ip == "" {
			t.Fatalf("the server does not know uv2's address")
		}
		addedMN = ensureHost(t, ctx, l.MN, ip+" "+newDomain, newDomain)
		addedUV2 = ensureHost(t, ctx, uv2, "127.0.1.1 "+newDomain, newDomain)
		resolved := l.MN.MustRun(t, ctx, "getent hosts "+newDomain)
		if !strings.Contains(resolved, ip) {
			t.Fatalf("%s does not resolve to %s on the server: %s", newDomain, ip, resolved)
		}
	})

	t.Run("the box is rebuilt on the new domain with the same agent key", func(t *testing.T) {
		if out, err := uv2.Unit(ctx, "stop", "termit-uv-agent", "termit-xray"); err != nil {
			t.Fatalf("stop: %v\n%s", err, out)
		}
		if out, err := uv2.Run(ctx, "systemctl reset-failed termit-uv-agent termit-xray"); err != nil {
			t.Logf("reset-failed: %v\n%s", err, out)
		}
		out := uv2.MustRun(t, ctx, "termit-uv uninstall --purge")
		t.Logf("uninstall: %s", oneLine(out))

		uv2.MustRun(t, ctx, "install -d -m 0750 /etc/termit/uv")
		uv2.MustRun(t, ctx, "install -m 0600 "+configCopy+" /etc/termit/uv/config.yaml")
		// The new box answers on the new name; everything else stays what it was.
		uv2.MustRun(t, ctx, `sed -i 's/^domain:.*/domain: `+newDomain+`/' /etc/termit/uv/config.yaml`)
		out = uv2.MustRun(t, ctx, "termit-uv init --config /etc/termit/uv/config.yaml --relocate --agent-key-file "+keyFile)
		t.Logf("init: %s", oneLine(out))
		if !strings.Contains(out, "relocated:") {
			t.Errorf("init did not report the relocated mode:\n%s", out)
		}

		newIdentity = statusString(t, ctx, uv2, "identity_fingerprint")
		if newIdentity == oldIdentity {
			t.Fatal("the rebuild kept the old identity")
		}
		newPath = identityField(t, ctx, uv2, "xhttp_path")
		if newPath == oldPath {
			t.Fatal("the rebuild kept the old path")
		}
		if enrolled, _ := statusJSON(t, ctx, uv2)["enrolled"].(bool); !enrolled {
			t.Error("the rebuilt node is not enrolled: the agent key did not reach the state")
		}
		// The move is the next command, not the install: the server must still hold the old endpoint
		// and the node must still be out of the mesh.
		if got := showField(t, ctx, l, uv2ID, "domain:"); got != oldDomain {
			t.Fatalf("the server is already on %q before the move", got)
		}
		if got := stateOf(t, ctx, l, uv2ID); got != "blocked" {
			t.Fatalf("the server is %s after the rebuild; it must not have been touched", got)
		}
		if got := len(nodeIDs(t, ctx, l)); got != nodeCount {
			t.Fatalf("the server lists %d nodes; the move must not create a new one", got)
		}
	})

	t.Run("migrate --relocate moves the record and keeps the pair credentials", func(t *testing.T) {
		out := uv2.MustRun(t, ctx, "termit-uv migrate --relocate")
		t.Logf("migrate:\n%s", out)
		if got := stateOf(t, ctx, l, uv2ID); got != "active" {
			t.Fatalf("uv2 is %s after the move, want active", got)
		}
		if got := showField(t, ctx, l, uv2ID, "domain:"); got != newDomain {
			t.Fatalf("the server still answers on %q", got)
		}

		for _, m := range []*lab.Machine{uv2, uv1} {
			syncOut := m.MustRun(t, ctx, "termit-uv sync")
			t.Logf("%s sync: %s", m.Name, oneLine(syncOut))
		}
		assertPeerAccepted(t, ctx, uv1, uv2ID, true)
		assertPeerAccepted(t, ctx, uv2, uv1ID, true)

		// The endpoint moved; the credential did not. That is the whole point of the move: nobody on
		// the mesh has to be reconfigured, they only learn a new host and path. The server serves the
		// pair credential for a peer, so the value must be the same one uv1 had before the move.
		if got := subscriptionPeerUUID(t, ctx, uv1, oldDomain); got != "" {
			t.Error("uv1's subscription still carries the old domain")
		}
		if got := subscriptionPeerUUID(t, ctx, uv1, newDomain); got == "" {
			t.Fatal("uv1's subscription does not carry the new domain")
		} else if got != oldPeerUUID {
			t.Error("the pair credential changed while the node moved")
		}
		body := subscriptionBody(t, ctx, uv1)
		if !strings.Contains(body, "/"+newPath+"/") {
			t.Error("uv1's subscription does not carry the new path")
		}
		if strings.Contains(body, "/"+oldPath+"/") {
			t.Error("uv1's subscription still carries the old path")
		}
		if !contains(xrayEmails(t, ctx, uv2), "probe@termit") {
			t.Error("the first sync after the move did not restore the server's probe credential")
		}
		if l.UV3 == nil {
			return
		}
		// With a third node the promise is visible: the node that moved changed its endpoint, the
		// pairs involving the node that stayed did not, and both directions agree on the credential.
		l.UV3.MustRun(t, ctx, "termit-uv sync")
		if got := subscriptionPeerUUID(t, ctx, uv1, uv3Domain); got != oldPeerUV3 {
			t.Error("uv1's pair with the third node changed while uv2 moved")
		}
		if got := subscriptionPeerUUID(t, ctx, l.UV3, uv1Domain); got != oldPeerUV3 {
			t.Error("the third node's pair with uv1 changed while uv2 moved")
		}
		if got := subscriptionPeerUUID(t, ctx, l.UV3, newDomain); got != oldPeerUV2UV3 {
			t.Error("the third node's pair with uv2 changed while uv2 moved")
		}
		if got := subscriptionPeerUUID(t, ctx, l.UV3, oldDomain); got != "" {
			t.Error("the third node's subscription still carries the old endpoint")
		}
		assertPeerAccepted(t, ctx, uv1, uv3ID, true)
		assertPeerAccepted(t, ctx, l.UV3, uv1ID, true)
		assertPeerAccepted(t, ctx, l.UV3, uv2ID, true)
	})

	t.Run("the old endpoint is gone from the mesh", func(t *testing.T) {
		if _, ok := nodeIDs(t, ctx, l)[oldDomain]; ok {
			t.Error("the server still lists the old domain")
		}
		if got := len(nodeIDs(t, ctx, l)); got != nodeCount {
			t.Errorf("the server lists %d nodes, want the same %d", got, nodeCount)
		}
	})
}

// alternateName is the name the move goes to. Adding and removing the prefix keeps the lab on two
// names instead of growing one with every acceptance run.
func alternateName(domain string) string {
	if strings.HasPrefix(domain, "b-") {
		return strings.TrimPrefix(domain, "b-")
	}
	return "b-" + domain
}

// ensureHost appends line to the machine's hosts file unless the name is already there, and reports
// whether it wrote anything (the caller removes only what it added).
func ensureHost(t *testing.T, ctx context.Context, m *lab.Machine, line, domain string) bool {
	t.Helper()
	if _, err := m.Run(ctx, "grep -q '"+domain+"' /etc/hosts"); err == nil {
		return false
	}
	m.MustRun(t, ctx, "printf '%s\\n' '"+line+"' >> /etc/hosts")
	return true
}

func removeHost(t *testing.T, ctx context.Context, m *lab.Machine, domain string) {
	t.Helper()
	if _, err := m.Run(ctx, "sed -i '/ "+domain+"$/d' /etc/hosts"); err != nil {
		t.Logf("cleanup: remove %s from %s's hosts file: %v", domain, m.Name, err)
	}
}

// showField reads one field of `mn uv show`: domain, state, ip.
func showField(t *testing.T, ctx context.Context, l *lab.Lab, id, key string) string {
	t.Helper()
	return field(l.MustMNAdmin(t, ctx, "uv", "show", id), key)
}

// subscriptionPeerUUID finds the outbound a peer uses for domain and returns its credential. The
// domain is the server address in the sing-box rendering; the tag may be a label, so the address is
// the reliable selector.
func subscriptionPeerUUID(t *testing.T, ctx context.Context, m *lab.Machine, domain string) string {
	t.Helper()
	body := subscriptionBody(t, ctx, m)
	var doc struct {
		Outbounds []struct {
			Type   string `json:"type"`
			Tag    string `json:"tag"`
			Server string `json:"server"`
			UUID   string `json:"uuid"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("%s: subscription is not JSON: %v", m.Name, err)
	}
	for _, outbound := range doc.Outbounds {
		if outbound.Type == "vless" && outbound.Server == domain {
			return outbound.UUID
		}
	}
	return ""
}

// restoreScenarioC brings the lab back to a converged state and removes the secret it wrote. A move
// that died half-way is finished here: the box either still serves the old name or already serves the
// new one, and the record is made to agree with whatever the box actually serves.
func restoreScenarioC(t *testing.T, l *lab.Lab, uv2ID, keyFile, configCopy, oldDomain, newDomain string, addedMN, addedUV2 bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	uv2 := l.UV2

	_, _ = uv2.Run(ctx, "systemctl reset-failed termit-xray termit-uv-agent")
	for _, unit := range []string{"termit-xray", "termit-uv-agent"} {
		if out, err := uv2.Unit(ctx, "start", unit); err != nil {
			t.Logf("cleanup: start %s: %v\n%s", unit, err, lab.Tail(out, 5))
		}
	}
	stateInstalled := func() bool {
		_, err := uv2.Run(ctx, "test -f /var/lib/termit/uv/state.json")
		return err == nil
	}
	if !stateInstalled() {
		// The rebuild stopped before it produced a state: install it again from the saved key and
		// configuration, which still names the old domain (the copy was taken before the switch).
		if err := installFromSaved(ctx, uv2, keyFile, configCopy); err != nil {
			t.Logf("cleanup: the rebuilt node is still uninstalled and the saved key did not finish it: %v", err)
		}
	}
	if stateInstalled() && configDomain(ctx, uv2) != oldDomain {
		// The test moved the box. Put it back on the name it started with, so the lab is where it was
		// and the record can be brought back too.
		if err := reinstallFromSaved(ctx, uv2, keyFile, configCopy); err != nil {
			t.Logf("cleanup: could not put the node back on %s: %v", oldDomain, err)
		} else if out, err := uv2.Run(ctx, "termit-uv migrate --relocate"); err != nil {
			t.Logf("cleanup: the node serves %s but the record did not follow: %v\n%s", oldDomain, err, lab.Tail(out, 5))
		}
	}
	if final := configDomain(ctx, uv2); final != "" {
		// Whatever name the node ends on has to resolve on both sides, or its own canary and the
		// server's probe cannot reach it. Only the extra name this run introduced is removed later.
		if out, err := l.MNAdmin(ctx, "uv", "show", uv2ID); err == nil {
			if ip := field(out, "ip:"); ip != "" {
				ensureHost(t, ctx, l.MN, ip+" "+final, final)
			}
		}
		ensureHost(t, ctx, uv2, "127.0.1.1 "+final, final)
	}
	if out, err := l.MNAdmin(ctx, "uv", "unblock", uv2ID); err != nil {
		t.Logf("cleanup: admin unblock: %v\n%s", err, lab.Tail(out, 5))
	}
	for _, m := range append([]*lab.Machine{uv2, l.UV1}, optionalUV3(l)...) {
		if out, err := m.Run(ctx, "termit-uv sync"); err != nil {
			t.Logf("%s: sync %v\n%s", m.Name, err, lab.Tail(out, 5))
		}
	}
	if final := configDomain(ctx, uv2); final != newDomain {
		if addedUV2 {
			removeHost(t, ctx, uv2, newDomain)
		}
		if addedMN {
			removeHost(t, ctx, l.MN, newDomain)
		}
	}
	if stateInstalled() {
		if _, err := uv2.Run(ctx, "rm -f "+keyFile+" "+configCopy); err != nil {
			t.Logf("cleanup: remove the saved credentials: %v", err)
		}
	}
}

func configDomain(ctx context.Context, m *lab.Machine) string {
	out, _ := m.Run(ctx, "sed -n 's/^domain: *//p' /etc/termit/uv/config.yaml")
	return strings.TrimSpace(out)
}

// optionalUV3 is the third machine when the lab has one, so callers can append it unconditionally.
func optionalUV3(l *lab.Lab) []*lab.Machine {
	if l.UV3 == nil {
		return nil
	}
	return []*lab.Machine{l.UV3}
}

// installFromSaved installs from the key and configuration the test saved before it touched anything.
func installFromSaved(ctx context.Context, m *lab.Machine, keyFile, configCopy string) error {
	out, err := m.Run(ctx, "test -f "+keyFile+" && test -f "+configCopy+" && install -d -m 0750 /etc/termit/uv && "+
		"install -m 0600 "+configCopy+" /etc/termit/uv/config.yaml && "+
		"termit-uv init --config /etc/termit/uv/config.yaml --relocate --agent-key-file "+keyFile)
	if err != nil {
		return fmt.Errorf("%w: %s", err, lab.Tail(out, 8))
	}
	return nil
}

func reinstallFromSaved(ctx context.Context, m *lab.Machine, keyFile, configCopy string) error {
	_, _ = m.Unit(ctx, "stop", "termit-uv-agent", "termit-xray")
	_, _ = m.Run(ctx, "systemctl reset-failed termit-uv-agent termit-xray")
	_, _ = m.Run(ctx, "termit-uv uninstall --purge")
	return installFromSaved(ctx, m, keyFile, configCopy)
}
