//go:build lab

package lab_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// TestScenarioA is the M3.3 acceptance: two nodes see each other, one dies, the server walks it
// through unavailable to blocked, the peer converges away, and only an explicit unblock brings it
// back. Every step asserts the observable state (the server's view, the core's live client set and
// the subscription) rather than trusting a command's exit status alone.
func TestScenarioA(t *testing.T) {
	l := lab.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	uv1, uv2 := l.UV1, l.UV2
	uv1Domain := statusDomain(t, ctx, uv1)
	uv2Domain := statusDomain(t, ctx, uv2)
	ids := nodeIDs(t, ctx, l)
	uv1ID, uv2ID := ids[uv1Domain], ids[uv2Domain]
	if uv1ID == "" || uv2ID == "" {
		t.Fatalf("the server does not list both nodes (domains %s, %s; listed %v)", uv1Domain, uv2Domain, ids)
	}
	t.Logf("nodes: %s=%s %s=%s", uv1Domain, uv1ID, uv2Domain, uv2ID)
	t.Logf("peer emails: uv1 expects %s, uv2 expects %s", peerEmail(uv2ID), peerEmail(uv1ID))

	// The lab must be usable after the test whether it passed, failed or panicked.
	t.Cleanup(func() { restore(t, l, uv2ID) })
	// Dump the failure state before restoring it: a failed run must be debuggable from its output.
	t.Cleanup(func() {
		if t.Failed() {
			dump(t, l)
		}
	})

	t.Run("both nodes are active and serve each other", func(t *testing.T) {
		ensureActive(t, ctx, l, uv2ID)
		for _, m := range []*lab.Machine{uv1, uv2} {
			out := m.MustRun(t, ctx, "termit-uv sync")
			t.Logf("%s sync: %s", m.Name, oneLine(out))
		}
		if got := stateOf(t, ctx, l, uv2ID); got != "active" {
			t.Fatalf("uv2 is %s, want active", got)
		}
		assertPeerAccepted(t, ctx, uv1, uv2ID, true)
		assertPeerAccepted(t, ctx, uv2, uv1ID, true)
		assertPeerInSubscription(t, ctx, uv1, uv2Domain, true)
		assertPeerInSubscription(t, ctx, uv2, uv1Domain, true)
	})

	t.Run("a failed cycle makes the node unavailable but keeps it in the peer set", func(t *testing.T) {
		// Killing the core leaves nginx and the certificate intact, so this is the tunnel failing,
		// not the whole host: exactly the case where the peer must stay for a while.
		if out, err := uv2.Unit(ctx, "stop", "termit-uv-agent", "termit-xray"); err != nil {
			t.Fatalf("stop uv2: %v\n%s", err, out)
		}
		event, state := checkNow(t, ctx, l, uv2ID)
		if event != "check_failed" {
			t.Fatalf("the first cycle reported %q, want check_failed (state %s)", event, state)
		}
		if state != "unavailable" {
			t.Fatalf("uv2 is %s after one failed cycle, want unavailable", state)
		}
		// No sync has happened on uv1 yet, so the credential must still be there.
		assertPeerAccepted(t, ctx, uv1, uv2ID, true)
		out := uv1.MustRun(t, ctx, "termit-uv sync")
		t.Logf("uv1 sync: %s", oneLine(out))
		if !strings.Contains(out, "already in sync") {
			t.Errorf("a sync kept nothing to do? %s", oneLine(out))
		}
		assertPeerAccepted(t, ctx, uv1, uv2ID, true)
	})

	t.Run("the second failed cycle blocks it and the peer converges away", func(t *testing.T) {
		event, state := checkNow(t, ctx, l, uv2ID)
		if event != "check_failed" {
			t.Fatalf("the second cycle reported %q, want check_failed (state %s)", event, state)
		}
		if state != "blocked" {
			t.Fatalf("uv2 is %s after two failed cycles, want blocked", state)
		}
		out := uv1.MustRun(t, ctx, "termit-uv sync")
		t.Logf("uv1 sync: %s", oneLine(out))
		assertPeerAccepted(t, ctx, uv1, uv2ID, false)
		assertPeerInSubscription(t, ctx, uv1, uv2Domain, false)
		if peers := statusPeers(t, ctx, uv1); contains(peers, uv2Domain) {
			t.Errorf("uv1 still lists the blocked peer: %v", peers)
		}
	})

	t.Run("a recovered node stays blocked until it asks to be unblocked", func(t *testing.T) {
		for _, unit := range []string{"termit-xray", "termit-uv-agent"} {
			if out, err := uv2.Unit(ctx, "start", unit); err != nil {
				t.Fatalf("start %s: %v\n%s", unit, err, out)
			}
		}
		waitHealthy(t, ctx, uv2)
		// A core that just bound its ports can answer one probe and drop the next while its handlers
		// settle. The assertion is that a healthy node stays blocked, so require the health to hold
		// for two checks in a row instead of declaring a startup race a failure.
		passes := 0
		deadline := time.Now().Add(45 * time.Second)
		for passes < 2 {
			if time.Now().After(deadline) {
				t.Fatalf("the recovered node did not pass two checks in a row (last: %d)", passes)
			}
			event, state := checkNow(t, ctx, l, uv2ID)
			if state != "blocked" {
				t.Fatalf("a healthy node left %s without an unblock request, want blocked", state)
			}
			if event != "check_ok" {
				passes = 0
				time.Sleep(2 * time.Second)
				continue
			}
			passes++
			if passes < 2 {
				time.Sleep(2 * time.Second)
			}
		}

		// Its sync is refused, and a refused sync changes nothing on the node.
		before := fileDigest(t, ctx, uv2, "/etc/termit/uv/xray.json")
		out, err := uv2.Run(ctx, "termit-uv sync")
		if err == nil {
			t.Fatalf("a blocked node's sync succeeded:\n%s", out)
		}
		if !strings.Contains(out, "node_inactive") {
			t.Errorf("the refusal does not name node_inactive:\n%s", out)
		}
		after := fileDigest(t, ctx, uv2, "/etc/termit/uv/xray.json")
		if before != after {
			t.Errorf("the refused sync changed the core config (%s -> %s)", before, after)
		}
	})

	t.Run("an explicit unblock restores the mesh", func(t *testing.T) {
		out := uv2.MustRun(t, ctx, "termit-uv unblock")
		t.Logf("uv2 unblock:\n%s", out)
		if got := stateOf(t, ctx, l, uv2ID); got != "active" {
			t.Fatalf("uv2 is %s after the unblock, want active", got)
		}
		for _, m := range []*lab.Machine{uv2, uv1} {
			syncOut := m.MustRun(t, ctx, "termit-uv sync")
			t.Logf("%s sync: %s", m.Name, oneLine(syncOut))
		}
		assertPeerAccepted(t, ctx, uv1, uv2ID, true)
		assertPeerAccepted(t, ctx, uv2, uv1ID, true)
		assertPeerInSubscription(t, ctx, uv1, uv2Domain, true)
		assertPeerInSubscription(t, ctx, uv2, uv1Domain, true)
	})
}

// checkNow runs one server-side cycle and returns the event and the resulting state.
func checkNow(t *testing.T, ctx context.Context, l *lab.Lab, id string) (event, state string) {
	t.Helper()
	out := l.MustMNAdmin(t, ctx, "uv", "check", "now", id)
	event = field(out, "event:")
	state = firstField(field(out, "state:"))
	t.Logf("check %s: event=%s state=%s", id, event, state)
	if event == "" || state == "" {
		t.Fatalf("the check report is missing event/state:\n%s", out)
	}
	return event, state
}

// stateOf is the server's view of one node.
func stateOf(t *testing.T, ctx context.Context, l *lab.Lab, id string) string {
	t.Helper()
	out := l.MustMNAdmin(t, ctx, "uv", "show", id)
	return firstField(field(out, "state:"))
}

func ensureActive(t *testing.T, ctx context.Context, l *lab.Lab, id string) {
	t.Helper()
	if stateOf(t, ctx, l, id) == "active" {
		return
	}
	out, err := l.MNAdmin(ctx, "uv", "unblock", id)
	if err != nil {
		t.Fatalf("bring %s to active: %v\n%s", id, err, out)
	}
	t.Logf("uv unblock (admin): %s", oneLine(out))
}

// assertPeerAccepted checks the live client set of one core, not the file on disk: a peer that the
// running core still accepts has not converged regardless of what was written.
func assertPeerAccepted(t *testing.T, ctx context.Context, m *lab.Machine, peerID string, want bool) {
	t.Helper()
	emails := xrayEmails(t, ctx, m)
	email := peerEmail(peerID)
	if got := contains(emails, email); got != want {
		t.Errorf("%s: peer credential accepted=%v, want %v (emails: %v)", m.Name, got, want, emails)
	}
}

func assertPeerInSubscription(t *testing.T, ctx context.Context, m *lab.Machine, domain string, want bool) {
	t.Helper()
	body := subscriptionBody(t, ctx, m)
	if got := strings.Contains(body, domain); got != want {
		t.Errorf("%s: subscription contains %s=%v, want %v", m.Name, domain, got, want)
	}
}

// subscriptionBody fetches the sing-box rendering, which is plain JSON: the base64 list hides the
// domain inside the encoding, and a test should assert on the content a client receives.
func subscriptionBody(t *testing.T, ctx context.Context, m *lab.Machine) string {
	t.Helper()
	url := subscriptionURL(t, ctx, m)
	if !strings.HasPrefix(url, "http") {
		t.Fatalf("%s: subscription URL %q is not absolute", m.Name, url)
	}
	ca := strings.TrimSpace(m.MustRun(t, ctx,
		`sed -n 's/^  ca_file: *//p' /etc/termit/uv/config.yaml`))
	if ca == "" {
		t.Fatalf("%s: the config has no mn.ca_file", m.Name)
	}
	// The lab images have no curl or wget, but Python is part of the base system on both sides.
	const script = "/tmp/termit-lab-subscription.py"
	if err := m.WriteFile(ctx, []byte(subscriptionFetcher), script, "0644"); err != nil {
		t.Fatalf("%v", err)
	}
	return m.MustRun(t, ctx, "python3 "+script+" "+url+"/json "+ca)
}

// subscriptionFetcher is a stdlib HTTPS client that trusts exactly the configured CA.
const subscriptionFetcher = `import ssl, sys, urllib.request

context = ssl.create_default_context(cafile=sys.argv[2])
with urllib.request.urlopen(sys.argv[1], context=context, timeout=15) as response:
    sys.stdout.write(response.read().decode())
`

func subscriptionURL(t *testing.T, ctx context.Context, m *lab.Machine) string {
	t.Helper()
	return strings.TrimSpace(m.MustRun(t, ctx,
		`python3 -c 'import json; print(json.load(open("/var/lib/termit/uv/state.json"))["credentials"].get("subscription_url",""))'`))
}

// xrayEmails is the core's own answer about which users it serves.
func xrayEmails(t *testing.T, ctx context.Context, m *lab.Machine) []string {
	t.Helper()
	binary := statusString(t, ctx, m, "xray_path")
	out := m.MustRun(t, ctx, binary+" api inbounduser --server=127.0.0.1:10085 -tag=proxy")
	var emails []string
	for _, line := range strings.Split(out, "\n") {
		if _, after, ok := strings.Cut(line, `"email": "`); ok {
			emails = append(emails, strings.TrimSuffix(strings.TrimSpace(after), `"`))
		}
	}
	return emails
}

func statusPeers(t *testing.T, ctx context.Context, m *lab.Machine) []string {
	t.Helper()
	status := statusJSON(t, ctx, m)
	raw, _ := status["peers"].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if peer, ok := item.(map[string]any); ok {
			if domain, ok := peer["domain"].(string); ok {
				out = append(out, domain)
			}
		}
	}
	return out
}

func statusDomain(t *testing.T, ctx context.Context, m *lab.Machine) string {
	t.Helper()
	return statusString(t, ctx, m, "domain")
}

func statusString(t *testing.T, ctx context.Context, m *lab.Machine, key string) string {
	t.Helper()
	value, _ := statusJSON(t, ctx, m)[key].(string)
	if value == "" {
		t.Fatalf("%s: status has no %q", m.Name, key)
	}
	return value
}

func statusJSON(t *testing.T, ctx context.Context, m *lab.Machine) map[string]any {
	t.Helper()
	out := m.MustRun(t, ctx, "termit-uv status --json")
	var status map[string]any
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("%s: status --json is not JSON: %v\n%s", m.Name, err, out)
	}
	return status
}

// nodeIDs maps domain to node id from `mn uv list`.
func nodeIDs(t *testing.T, ctx context.Context, l *lab.Lab) map[string]string {
	t.Helper()
	out := l.MustMNAdmin(t, ctx, "uv", "list")
	ids := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "ID" {
			continue
		}
		ids[fields[1]] = fields[0]
	}
	return ids
}

// waitHealthy waits for the node's two public faces: nginx on 443 and the core on 8443.
func waitHealthy(t *testing.T, ctx context.Context, m *lab.Machine) {
	t.Helper()
	err := lab.WaitFor(ctx, 60*time.Second, 2*time.Second, m.Name+" to accept connections", func(ctx context.Context) (bool, error) {
		out, err := m.Run(ctx, "ss -ltn")
		if err != nil {
			return false, err
		}
		return strings.Contains(out, ":443 ") && strings.Contains(out, ":8443 "), nil
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
}

func fileDigest(t *testing.T, ctx context.Context, m *lab.Machine, path string) string {
	t.Helper()
	out := strings.TrimSpace(m.MustRun(t, ctx, "sha256sum "+path))
	return firstField(out)
}

func peerEmail(peerID string) string { return "peer-" + peerID + "@termit" }

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// field returns the value after "key" on its own line (the admin CLI prints "key: value").
func field(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), key))
		}
	}
	return ""
}

func firstField(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// restore puts the lab back into the converged state the next run expects: uv2 up, unblocked and
// synced, then uv1 synced so it carries the peer again.
func restore(t *testing.T, l *lab.Lab, uv2ID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	_, _ = l.UV2.Run(ctx, "systemctl reset-failed termit-xray termit-uv-agent")
	for _, unit := range []string{"termit-xray", "termit-uv-agent"} {
		if out, err := l.UV2.Unit(ctx, "start", unit); err != nil {
			t.Logf("cleanup: start %s: %v\n%s", unit, err, out)
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
}

// dump prints what an operator would look at after a failure.
func dump(t *testing.T, l *lab.Lab) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, m := range []*lab.Machine{l.UV1, l.UV2} {
		out, err := m.Run(ctx, "systemctl is-active termit-xray termit-uv-agent; journalctl -u termit-uv-agent -n 12 --no-pager")
		if err != nil {
			t.Logf("%s: %v", m.Name, err)
		}
		t.Logf("%s:\n%s", m.Name, out)
	}
	if out, err := l.MNAdmin(ctx, "uv", "list"); err == nil {
		t.Logf("nodes:\n%s", out)
	}
}
