//go:build lab

package lab_test

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// The replication drills. Two management servers, one journal shared between them, and the failures that
// make replication worth having: the peer is away, the link is cut, the process dies, the journal's oldest
// entry is newer than the peer's cursor. Each drill breaks one thing, asserts the invariant the breakage is
// supposed to protect, and leaves both servers agreeing again.
//
// They need `TERMIT_LAB_MN2` and `TERMIT_LAB_MN2_ADMIN_TOKEN`, plus a mesh built by hand
// (`OPERATIONS.md` §3.4): the same master key on both, each listing the other under `mesh.peers` with the
// peer's leaf pinned. Without that, the drill skips instead of guessing.

// replicationLab is the lab plus the promise that it really is a mesh. A configured second server that is
// not replicating would make every assertion here vacuously true.
func replicationLab(t *testing.T) *lab.Lab {
	t.Helper()
	l := lab.FromEnv(t)
	if l.MN2 == nil {
		t.Skip("no second management server: set " + lab.EnvMN2 + " to run the replication drills")
	}
	if l.MN2AdminToken == "" {
		t.Skip("no second admin token: set " + lab.EnvMN2AdminToken + " to run the replication drills")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	view := meshOnMN(t, ctx, l)
	if len(view.Peers) == 0 {
		t.Fatalf("this lab is not a mesh: %s has no mesh.peers (OPERATIONS.md §3.4)", l.MN.Name)
	}
	return l
}

// meshView is the `mesh:` block of `termit-mn status`, as far as a drill needs it.
type meshView struct {
	MNID  string
	Epoch int
	Peers map[string]peerView
}

// peerView is one `peer` line: how far this server has applied that peer's journal.
type peerView struct {
	Name    string
	Text    string
	Cursor  int64
	LastSeq int64
}

// UpToDate is the one state a drill may call healthy: this server has the peer's journal as of its last pull.
func (p peerView) UpToDate() bool { return strings.HasPrefix(p.Text, "up to date") }

var (
	meshHeader = regexp.MustCompile(`^mesh:\s+(\S+)\s+\(.*epoch (\d+)\)`)
	peerBehind = regexp.MustCompile(`^(\d+) behind \(cursor (\d+) of (\d+)\)`)
	peerAhead  = regexp.MustCompile(`^up to date \((\d+) entr`)
)

// parseMesh reads the block the status command prints. The format is the operator's, so the drill reads it
// the way the operator does instead of asking for a private interface.
func parseMesh(t *testing.T, out string) meshView {
	t.Helper()
	view := meshView{Peers: map[string]peerView{}}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if match := meshHeader.FindStringSubmatch(trimmed); match != nil {
			view.MNID = match[1]
			view.Epoch, _ = strconv.Atoi(match[2])
			continue
		}
		if !strings.HasPrefix(trimmed, "peer ") {
			continue
		}
		name, rest, ok := strings.Cut(strings.TrimPrefix(trimmed, "peer "), " ")
		if !ok {
			continue
		}
		entry := peerView{Name: name}
		if _, text, ok := strings.Cut(rest, "— "); ok {
			entry.Text = strings.TrimSpace(text)
		}
		if match := peerBehind.FindStringSubmatch(entry.Text); match != nil {
			entry.Cursor, _ = strconv.ParseInt(match[2], 10, 64)
			entry.LastSeq, _ = strconv.ParseInt(match[3], 10, 64)
		} else if match := peerAhead.FindStringSubmatch(entry.Text); match != nil {
			entry.LastSeq, _ = strconv.ParseInt(match[1], 10, 64)
			entry.Cursor = entry.LastSeq
		}
		view.Peers[name] = entry
	}
	if view.MNID == "" {
		t.Fatalf("no mesh block in the status output; this server is not in a mesh:\n%s", out)
	}
	return view
}

func meshOnMN(t *testing.T, ctx context.Context, l *lab.Lab) meshView {
	t.Helper()
	return parseMesh(t, l.MustMNAdmin(t, ctx, "status"))
}

func meshOnMN2(t *testing.T, ctx context.Context, l *lab.Lab) meshView {
	t.Helper()
	return parseMesh(t, l.MustMN2Admin(t, ctx, "status"))
}

// onlyPeer is the single peer a drill replicates with: two would make "wait until it caught up" ambiguous.
func onlyPeer(t *testing.T, view meshView) string {
	t.Helper()
	if len(view.Peers) != 1 {
		t.Fatalf("the lab mesh has %d peers, want 1", len(view.Peers))
	}
	for name := range view.Peers {
		return name
	}
	return ""
}

// stateOn reads one node's state from a chosen server: how a drill tells "the change reached the replica"
// from "the replica still believes the old value".
func stateOn(t *testing.T, ctx context.Context, l *lab.Lab, onMN2 bool, domain string) string {
	t.Helper()
	var out string
	if onMN2 {
		out = l.MustMN2Admin(t, ctx, "uv", "show", domain)
	} else {
		out = l.MustMNAdmin(t, ctx, "uv", "show", domain)
	}
	return stateWord(outputField(out, "state"))
}

// waitReplicated blocks until the replica reports the node in want.
func waitReplicated(t *testing.T, ctx context.Context, l *lab.Lab, domain, want string) {
	t.Helper()
	err := lab.WaitFor(ctx, 4*time.Minute, 3*time.Second, "the replica to report "+domain+" "+want,
		func(ctx context.Context) (bool, error) {
			return stateOn(t, ctx, l, true, domain) == want, nil
		})
	if err != nil {
		t.Fatalf("%v\nreplica view: %s", err, l.MustMN2Admin(t, ctx, "uv", "show", domain))
	}
}

// stopMN2 and startMN2 manage the second server through systemd, the way an operator would.
func stopMN2(t *testing.T, ctx context.Context, l *lab.Lab) {
	t.Helper()
	l.MN2.MustRun(t, ctx, "systemctl stop termit-mn")
	if err := lab.WaitFor(ctx, 30*time.Second, 300*time.Millisecond, "the second server to stop",
		func(ctx context.Context) (bool, error) {
			out, _ := l.MN2.Run(ctx, "pgrep -x termit-mn")
			return strings.TrimSpace(out) == "", nil
		}); err != nil {
		t.Fatalf("%v", err)
	}
}

func startMN2(t *testing.T, ctx context.Context, l *lab.Lab) {
	t.Helper()
	l.MN2.MustRun(t, ctx, "systemctl start termit-mn")
	if err := lab.WaitFor(ctx, 90*time.Second, time.Second, "the second server to answer",
		func(ctx context.Context) (bool, error) {
			_, err := l.MN2Admin(ctx, "uv", "list")
			return err == nil, nil
		}); err != nil {
		t.Fatalf("%v", err)
	}
}

// pullCycle reads how often the lab replicates, so a drill waits in its units rather than in magic ones.
func pullCycle(t *testing.T, ctx context.Context, l *lab.Lab) time.Duration {
	t.Helper()
	out := l.MN2.MustRun(t, ctx, "sed -n 's/^ *pull_every: *//p' /etc/termit/mn/config.yaml | head -n 1")
	raw := strings.Trim(strings.TrimSpace(out), `"`)
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

// blockOnMN blocks a node and returns the function that unblocks it.
func blockOnMN(t *testing.T, ctx context.Context, l *lab.Lab, domain string) func() {
	t.Helper()
	l.MustMNAdmin(t, ctx, "uv", "block", domain)
	return func() {
		_, _ = l.MNAdmin(context.Background(), "uv", "unblock", domain)
	}
}

// TestDrillReplicationPeerDown stops the replica and asserts the mesh survives it: the home keeps serving
// its nodes, the journal grows while nobody reads it, and when the replica comes back it catches up from the
// journal — no snapshot, no operator, no lost row.
func TestDrillReplicationPeerDown(t *testing.T) {
	l := replicationLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	before := meshOnMN2(t, ctx, l)
	peer := onlyPeer(t, before)
	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)

	stopMN2(t, ctx, l)
	t.Cleanup(func() { startMN2(t, context.Background(), l) })

	unblock := blockOnMN(t, ctx, l, domain)
	t.Cleanup(unblock)
	if got := showState(t, ctx, l, domain); got != "blocked" {
		t.Fatalf("the home reports %s after a block, want blocked", got)
	}

	startMN2(t, ctx, l)
	if err := lab.WaitFor(ctx, 4*time.Minute, 3*time.Second, "the replica to catch up",
		func(ctx context.Context) (bool, error) {
			entry := meshOnMN2(t, ctx, l).Peers[peer]
			return entry.UpToDate() && entry.LastSeq > before.Peers[peer].LastSeq, nil
		}); err != nil {
		t.Fatalf("%v", err)
	}
	waitReplicated(t, ctx, l, domain, "blocked")
	unblock()
	waitReplicated(t, ctx, l, domain, "active")
}

// TestDrillReplicationPartition cuts the link between the two servers while both keep running. Every node
// keeps working, the replica keeps answering from the last state it has, and a credential rotation is
// refused — a bump on half a mesh is the split the gate exists to prevent.
func TestDrillReplicationPartition(t *testing.T) {
	l := replicationLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	before := meshOnMN(t, ctx, l)
	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)
	waitReplicated(t, ctx, l, domain, "active")

	if _, err := l.MN.Run(ctx, "nft --version"); err != nil {
		t.Skip("no nft on the home: the partition drill needs it")
	}
	cutLink(t, ctx, l)
	t.Cleanup(func() { healLink(context.Background(), l) })

	unblock := blockOnMN(t, ctx, l, domain)
	t.Cleanup(unblock)

	// The replica keeps the last state it had, and it keeps answering while it cannot reach the home.
	window := 3 * pullCycle(t, ctx, l)
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if got := stateOn(t, ctx, l, true, domain); got != "active" {
			t.Fatalf("the change crossed a partition: the replica reports %s after %s", got, window)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	if _, err := l.MN2Admin(ctx, "uv", "show", domain); err != nil {
		t.Fatalf("the replica stopped answering during a partition: %v", err)
	}

	// A rotation needs the whole mesh, and the mesh is apart.
	out, err := l.MNAdmin(ctx, "epoch", "rotate")
	if err == nil {
		t.Fatalf("the rotation went through with the peer unreachable:\n%s", out)
	}
	if !strings.Contains(out, "nothing was changed") {
		t.Errorf("the refusal does not say nothing changed:\n%s", out)
	}
	if after := meshOnMN(t, ctx, l); after.Epoch != before.Epoch {
		t.Errorf("the epoch moved to %d during a partition, want %d", after.Epoch, before.Epoch)
	}
	// What the link carried is the mesh, not the nodes: a node syncing against the home is still fine.
	l.UV1.MustRun(t, ctx, "termit-uv sync")

	healLink(ctx, l)
	waitReplicated(t, ctx, l, domain, "blocked")
	unblock()
	waitReplicated(t, ctx, l, domain, "active")
}

// TestDrillReplicationRestart kills the replica without warning and asserts nothing is lost or doubled: the
// rows are the same, the epoch is the same, and the position moves forward from where it was.
func TestDrillReplicationRestart(t *testing.T) {
	l := replicationLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	before := meshOnMN2(t, ctx, l)
	peer := onlyPeer(t, before)
	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)
	unblock := blockOnMN(t, ctx, l, domain)
	t.Cleanup(unblock)
	waitReplicated(t, ctx, l, domain, "blocked")

	rows := replicaRows(t, ctx, l)
	epoch := meshOnMN2(t, ctx, l).Epoch

	l.MN2.MustRun(t, ctx, "systemctl kill -s KILL termit-mn")
	startMN2(t, ctx, l)

	if got := replicaRows(t, ctx, l); got != rows {
		t.Errorf("the replica came back with %q, want %q", got, rows)
	}
	if got := meshOnMN2(t, ctx, l).Epoch; got != epoch {
		t.Errorf("the epoch is %d after a restart, want %d", got, epoch)
	}
	waitReplicated(t, ctx, l, domain, "blocked")
	entry := meshOnMN2(t, ctx, l).Peers[peer]
	if !entry.UpToDate() || entry.LastSeq < before.Peers[peer].LastSeq {
		t.Errorf("the replica's position after a restart is %q, want up to date and past %d",
			entry.Text, before.Peers[peer].LastSeq)
	}
	unblock()
	waitReplicated(t, ctx, l, domain, "active")
}

// TestDrillReplicationSnapshotOnGap takes a journal entry away while the replica is down, so the next entry
// it is offered is not the one after its cursor. A stale cursor cannot be replayed, and a snapshot is the
// only way across the hole — after which both sides hold the same rows.
//
// This drill rewrites the home's journal with sqlite3: it skips when that is not installed, and it does not
// pretend the missing entry can be put back.
func TestDrillReplicationSnapshotOnGap(t *testing.T) {
	l := replicationLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	if _, err := l.MN.Run(ctx, "command -v sqlite3"); err != nil {
		t.Skip("no sqlite3 on the home: the gap drill rewrites its journal")
	}
	if l.UV3 == nil {
		t.Skip("the gap drill needs a third node: set " + lab.EnvUV3)
	}
	home := meshOnMN(t, ctx, l)
	// The peer name is local to the server that prints it: MN2 calls the home "mn", and MN calls MN2 "mn2".
	peer := onlyPeer(t, meshOnMN2(t, ctx, l))
	first := nodeDomain(t, ctx, l.UV2)
	second := nodeDomain(t, ctx, l.UV3)
	makeActive(t, ctx, l, l.UV2, first)
	makeActive(t, ctx, l, l.UV3, second)

	if err := lab.WaitFor(ctx, 4*time.Minute, 3*time.Second, "the replica to be up to date",
		func(ctx context.Context) (bool, error) {
			return meshOnMN2(t, ctx, l).Peers[peer].UpToDate(), nil
		}); err != nil {
		t.Fatalf("%v\nthe replica says: %q", err, meshOnMN2(t, ctx, l).Peers[peer].Text)
	}
	// The replica's cursor, as the home's status line reports it: everything up to here is applied there.
	cursor := meshOnMN2(t, ctx, l).Peers[peer].LastSeq

	stopMN2(t, ctx, l)
	t.Cleanup(func() { startMN2(t, context.Background(), l) })

	unblock := blockOnMN(t, ctx, l, first)
	t.Cleanup(unblock)
	l.MustMNAdmin(t, ctx, "uv", "block", second)
	t.Cleanup(func() { _, _ = l.MNAdmin(context.Background(), "uv", "unblock", second) })

	// The entry the replica is about to be offered is gone: seq cursor+1 held the first block, so the next
	// entry it sees is cursor+2 and ApplyEvents refuses the hole.
	dropJournal(t, ctx, l, home.MNID, cursor+1)

	startMN2(t, ctx, l)
	// A snapshot replaces the replica's copy of the home's rows, so both blocks must be there — including the
	// one whose journal entry no longer exists.
	waitReplicated(t, ctx, l, first, "blocked")
	waitReplicated(t, ctx, l, second, "blocked")
	if got := meshOnMN2(t, ctx, l).Peers[peer].LastSeq; got <= cursor {
		t.Errorf("the replica's position is %d after a snapshot, want past %d", got, cursor)
	}
	unblock()
	_, _ = l.MNAdmin(ctx, "uv", "unblock", second)
	waitReplicated(t, ctx, l, first, "active")
	waitReplicated(t, ctx, l, second, "active")
}

// replicaRows is the set of ids the replica holds, which is how a drill spots a lost or doubled row.
func replicaRows(t *testing.T, ctx context.Context, l *lab.Lab) string {
	t.Helper()
	out := l.MustMN2Admin(t, ctx, "uv", "list")
	ids := make([]string, 0, 8)
	for _, line := range strings.Split(out, "\n") {
		if id, _, ok := strings.Cut(strings.TrimSpace(line), " "); ok && strings.HasPrefix(id, "uv_") {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, ",")
}

// dropJournal deletes one entry from the home's journal. It is the only drill that edits a database by hand,
// and it does so to build the one state a peer cannot replay.
func dropJournal(t *testing.T, ctx context.Context, l *lab.Lab, mnID string, seq int64) {
	t.Helper()
	query := "DELETE FROM replication_event WHERE mn_id = '" + mnID + "' AND seq = " + strconv.FormatInt(seq, 10)
	l.MN.MustRun(t, ctx, "sqlite3 /var/lib/termit/mn/termit.db \""+query+"\"")
}

// cutLink drops the mesh's HTTPS between the two servers, in both directions, and leaves SSH alone.
func cutLink(t *testing.T, ctx context.Context, l *lab.Lab) {
	t.Helper()
	for _, machine := range []*lab.Machine{l.MN, l.MN2} {
		machine.MustRun(t, ctx, "nft add table inet labpart")
		machine.MustRun(t, ctx, "nft 'add chain inet labpart out { type filter hook output priority 0; policy accept; }'")
		machine.MustRun(t, ctx, "nft add rule inet labpart out ip daddr "+peerHost(l, machine)+" tcp dport 443 drop")
	}
}

// healLink removes what cutLink installed. It is called at the end of the drill and again from its cleanup,
// so it must be harmless when the tables are already gone.
func healLink(ctx context.Context, l *lab.Lab) {
	for _, machine := range []*lab.Machine{l.MN, l.MN2} {
		_, _ = machine.Run(ctx, "nft delete table inet labpart")
	}
}

// peerHost is the other server's address, which is the one the puller dials.
func peerHost(l *lab.Lab, machine *lab.Machine) string {
	if machine == l.MN {
		return l.MN2.Host
	}
	return l.MN.Host
}

// TestDrillReplicationReadOnlySync points a node at the replica while its home is alive. The replica has to
// answer from the last state the home published, say so, change nothing of its own, and hand the node's
// report to the home — which applies it as if the node had synced directly.
func TestDrillReplicationReadOnlySync(t *testing.T) {
	l := replicationLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	home := meshOnMN(t, ctx, l)
	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)
	waitReplicated(t, ctx, l, domain, "active")

	homeURL := serverURL(t, ctx, l.MN)
	replicaURL := serverURL(t, ctx, l.MN2)
	// The sighting timestamp is compared as stored: `uv list` rounds to the minute, and a drill that compares
	// minutes cannot tell a delivered report from a stale one.
	for _, machine := range []*lab.Machine{l.MN, l.MN2} {
		if _, err := machine.Run(ctx, "command -v sqlite3"); err != nil {
			t.Skip("no sqlite3 on both servers: this drill reads the sighting timestamp")
		}
	}
	if homeURL == "" || replicaURL == "" {
		t.Fatalf("cannot read the management URLs: node %q, replica %q", homeURL, replicaURL)
	}
	seenBefore := lastSeenSQL(t, ctx, l, false, domain)
	copiedBefore := lastSeenSQL(t, ctx, l, true, domain)
	stateBefore := stateOn(t, ctx, l, true, domain)

	restore := repointNode(t, ctx, victim, homeURL, replicaURL)
	t.Cleanup(restore)

	out := victim.MustRun(t, ctx, "termit-uv sync")
	if !strings.Contains(out, "read-only") {
		t.Fatalf("the replica did not answer read-only:\n%s", out)
	}
	if !strings.Contains(out, home.MNID) {
		t.Errorf("the read-only answer does not name the home %s:\n%s", home.MNID, out)
	}
	// A replica copies and never writes: its own view of the node must be exactly what it was.
	if got := lastSeenSQL(t, ctx, l, true, domain); got != copiedBefore {
		t.Errorf("the replica recorded a sighting of a node it does not home: %q, want %q", got, copiedBefore)
	}
	if got := stateOn(t, ctx, l, true, domain); got != stateBefore {
		t.Errorf("the replica changed the node's state to %q, want %q", got, stateBefore)
	}
	// The report is queued there and delivered to the home, which is what makes the read-only answer safe.
	if err := lab.WaitFor(ctx, 4*time.Minute, 3*time.Second, "the home to apply the queued report",
		func(ctx context.Context) (bool, error) {
			return lastSeenSQL(t, ctx, l, false, domain) != seenBefore, nil
		}); err != nil {
		t.Fatalf("%v\nthe home still says %q", err, lastSeenSQL(t, ctx, l, false, domain))
	}

	restore()
	if out := victim.MustRun(t, ctx, "termit-uv sync"); strings.Contains(out, "read-only") {
		t.Errorf("the node still syncs read-only after being pointed back at its home:\n%s", out)
	}
}

// nodeMNURL is the management server a node is configured to sync with.
func nodeMNURL(t *testing.T, ctx context.Context, m *lab.Machine) string {
	t.Helper()
	out := m.MustRun(t, ctx, "sed -n 's|^  url: *||p' /etc/termit/uv/config.yaml | head -n 1")
	return strings.TrimSpace(out)
}

// serverURL is a management server's own API address, the shape a node's configuration holds.
func serverURL(t *testing.T, ctx context.Context, m *lab.Machine) string {
	t.Helper()
	domain := strings.TrimSpace(m.MustRun(t, ctx, "sed -n 's|^domain: *||p' /etc/termit/mn/config.yaml | head -n 1"))
	prefix := strings.TrimSpace(m.MustRun(t, ctx, "sed -n 's|^api_prefix: *||p' /etc/termit/mn/config.yaml | head -n 1"))
	if domain == "" || prefix == "" {
		return ""
	}
	return "https://" + strings.Trim(domain, `"`) + prefix
}

// repointNode points a node at another management server and restarts its agent. The returned function puts
// the configuration back and restarts the agent again, so a failed drill still leaves the node at home.
func repointNode(t *testing.T, ctx context.Context, m *lab.Machine, from, to string) func() {
	t.Helper()
	// Starting anywhere else would turn the drill into a no-op that still reports success.
	if got := nodeMNURL(t, ctx, m); got != from {
		t.Fatalf("the node points at %q, not at %q; point it back at its home first", got, from)
	}
	m.MustRun(t, ctx, "cp /etc/termit/uv/config.yaml /root/uv-config.repoint")
	m.MustRun(t, ctx, "sed -i "+lab.Quote("s|"+from+"|"+to+"|")+" /etc/termit/uv/config.yaml")
	m.MustRun(t, ctx, "systemctl restart termit-uv-agent")
	if got := nodeMNURL(t, ctx, m); got != to {
		t.Fatalf("the node reads %q as its management server, want %q", got, to)
	}
	restore := func() {
		_, _ = m.Run(context.Background(), "cp /root/uv-config.repoint /etc/termit/uv/config.yaml")
		_, _ = m.Run(context.Background(), "systemctl restart termit-uv-agent")
	}
	return restore
}

// lastSeenSQL is the node's sighting timestamp as the registry stores it, on either server.
func lastSeenSQL(t *testing.T, ctx context.Context, l *lab.Lab, onMN2 bool, domain string) string {
	t.Helper()
	query := "SELECT COALESCE(last_seen_at, '') FROM uv WHERE domain = '" + domain + "'"
	machine := l.MN
	if onMN2 {
		machine = l.MN2
	}
	out := machine.MustRun(t, ctx, "sqlite3 /var/lib/termit/mn/termit.db \""+query+"\"")
	return strings.TrimSpace(out)
}
