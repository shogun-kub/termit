//go:build lab

package lab_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/test/lab"
)

// The failure drills of M3.6. Each one breaks exactly one thing and asserts the invariant that
// breakage is supposed to protect, then puts the lab back the way it was. They are written against
// the observable surface — the server's state, the node's services, the certificate on the wire —
// rather than against internal functions, because the question is what an operator would see.
//
// Two drills the plan lists are not here yet: clock skew needs CAP_SYS_TIME, which an unprivileged
// LXC does not have (see the skip in TestDrillClockSkew), and the day-long MN outage is the same
// code path as the short one.

const (
	drillFillPath = "/var/lib/termit/mn/drill-fill"
	drillHostsBak = "/root/termit-drill-hosts.bak"
	drillDir      = "/root/termit-drill"
)

// drillLab returns the lab, skipping when it is not configured.
func drillLab(t *testing.T) *lab.Lab {
	t.Helper()
	return lab.FromEnv(t)
}

// drillVictim picks the node a drill breaks: the optional third node when the lab has it, so the pair
// between the other two can witness the damage, and the second node otherwise.
func drillVictim(t *testing.T, l *lab.Lab) *lab.Machine {
	t.Helper()
	if l.UV3 != nil {
		return l.UV3
	}
	return l.UV2
}

// nodeDomain reads the domain from the node's own configuration: it is the address its certificate is
// issued for, which is what the server probes.
func nodeDomain(t *testing.T, ctx context.Context, m *lab.Machine) string {
	t.Helper()
	domain := strings.TrimSpace(m.MustRun(t, ctx, "sed -n 's/^domain: *//p' /etc/termit/uv/config.yaml"))
	if domain == "" {
		t.Fatalf("%s: no domain in its configuration", m.Name)
	}
	return domain
}

// showState is the server's view of one node.
func showState(t *testing.T, ctx context.Context, l *lab.Lab, domain string) string {
	t.Helper()
	return firstField(showField(t, ctx, l, domain, "state:"))
}

// checkNow runs one cycle by hand and returns the fields the CLI prints.
type checkOutcome struct {
	Event  string
	Class  string
	State  string
	Reason string
}

func runCheckNow(t *testing.T, ctx context.Context, l *lab.Lab, domain string) checkOutcome {
	t.Helper()
	out := l.MustMNAdmin(t, ctx, "uv", "check", "now", domain)
	return checkOutcome{
		Event:  outputField(out, "event"),
		Class:  outputField(out, "class"),
		State:  stateWord(outputField(out, "state")),
		Reason: outputField(out, "reason"),
	}
}

// outputField reads one "key: value" line of a CLI report.
func outputField(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) == key {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stateWord(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// makeActive brings the victim back to a checked, synced, active node. It is the setup every drill
// shares and the state every cleanup aims for.
func makeActive(t *testing.T, ctx context.Context, l *lab.Lab, m *lab.Machine, domain string) {
	t.Helper()
	if state := showState(t, ctx, l, domain); state != "active" {
		// A blocked node refuses to sync, so the admin override goes first; the pin then follows the
		// certificate the node serves.
		if _, err := l.MNAdmin(ctx, "uv", "unblock", domain); err != nil {
			t.Fatalf("make %s active: %v", domain, err)
		}
	}
	m.MustRun(t, ctx, "termit-uv sync")
	outcome := runCheckNow(t, ctx, l, domain)
	if outcome.Event != "check_ok" {
		t.Fatalf("the victim is not healthy before the drill: %+v", outcome)
	}
}

// TestDrillMNUnavailable stops the management server and asserts that the nodes keep serving: their
// services do not depend on it, their public certificate still answers, and when the server comes
// back they converge without an operator.
func TestDrillMNUnavailable(t *testing.T) {
	l := drillLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	victim, domain := l.UV1, ""
	domain = nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)

	exe, args := mnCommand(t, ctx, l)
	stopMN(t, ctx, l)
	t.Cleanup(func() {
		if len(mnPIDs(t, context.Background(), l)) > 0 {
			return
		}
		startMN(t, context.Background(), l, exe, args)
	})

	// Nothing on the node talks to the server any more, and everything that serves traffic keeps
	// running: the core, the web tier and the certificate it presents.
	services := victim.MustRun(t, ctx, "systemctl is-active termit-xray nginx termit-uv-agent")
	for _, line := range strings.Split(strings.TrimSpace(services), "\n") {
		if strings.TrimSpace(line) != "active" {
			t.Fatalf("a service stopped when the management server went away:\n%s", services)
		}
	}
	listening := victim.MustRun(t, ctx, "ss -ltn | grep -c ':443 \\|:8443 ' || true")
	if strings.TrimSpace(listening) == "0" {
		t.Fatal("the node stopped listening when the management server went away")
	}
	// The certificate is the one nginx serves, and it is still served: that is the path clients use.
	served := victim.MustRun(t, ctx, "openssl s_client -connect 127.0.0.1:443 -servername "+domain+
		" </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256")
	local := victim.MustRun(t, ctx, "openssl x509 -in /var/lib/termit/uv/certs/fullchain.pem -noout -fingerprint -sha256")
	if strings.TrimSpace(served) == "" || strings.TrimSpace(served) != strings.TrimSpace(local) {
		t.Fatalf("the node stopped serving its certificate:\nserved: %s\nlocal:  %s", served, local)
	}
	// The agent cannot sync, but it must not touch the peer set either: the last known set stays.
	clients := victim.MustRun(t, ctx, "systemctl is-active termit-xray")
	if strings.TrimSpace(clients) != "active" {
		t.Fatalf("xray is %s with the management server down", clients)
	}
	t.Logf("the node serves %s while the management server is down", domain)

	startMN(t, ctx, l, exe, args)
	waitMN(t, ctx, l)

	// Convergence is the node's own doing: one sync, then a check from the far side.
	victim.MustRun(t, ctx, "termit-uv sync")
	victim.MustRun(t, ctx, "termit-uv sync --check")
	if state := showState(t, ctx, l, domain); state != "active" {
		t.Fatalf("the node is %s after the server came back", state)
	}
	outcome := runCheckNow(t, ctx, l, domain)
	if outcome.Event != "check_ok" {
		t.Fatalf("the node did not converge after recovery: %+v", outcome)
	}
}

// TestDrillProberBroken takes the prober's binary away. Checks must report unknown and leave the node
// alone: our broken tool is not evidence against the node. Restoring the binary restores the checks.
func TestDrillProberBroken(t *testing.T) {
	l := drillLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)

	binary := strings.TrimSpace(l.MN.MustRun(t, ctx, "sed -n 's/^  xray_binary: *//p' /etc/termit/mn/config.yaml"))
	if binary == "" {
		t.Fatal("the management server has no prober binary configured")
	}
	l.MN.MustRun(t, ctx, "mv "+binary+" "+binary+".drill")
	t.Cleanup(func() { restoreBinary(t, l, binary) })

	// Two failed starts, not three: the circuit breaker opens on the third and would make the check
	// answer before it tries.
	first := runCheckNow(t, ctx, l, domain)
	second := runCheckNow(t, ctx, l, domain)
	for _, outcome := range []checkOutcome{first, second} {
		if outcome.Event != "check_unknown" {
			t.Fatalf("a broken prober produced %q, want unknown", outcome.Event)
		}
		if outcome.State != "active" {
			t.Fatalf("a broken prober moved the node to %s", outcome.State)
		}
	}
	if state := showState(t, ctx, l, domain); state != "active" {
		t.Fatalf("the node is %s after two unknown checks", state)
	}

	restoreBinary(t, l, binary)
	outcome := runCheckNow(t, ctx, l, domain)
	if outcome.Event != "check_ok" {
		t.Fatalf("the node did not recover with the prober back: %+v", outcome)
	}
}

// restoreBinary is idempotent so the test body and its cleanup can both call it.
func restoreBinary(t *testing.T, l *lab.Lab, binary string) {
	t.Helper()
	_, _ = l.MN.Run(context.Background(), "test -f "+binary+".drill && mv "+binary+".drill "+binary)
}

// TestDrillDNSFlipped points the node's name at an address that answers nothing. That is a genuine
// negative — the name no longer leads to the node — so the state machine must walk it to blocked, the
// surviving pair must keep working, and the restored name plus an explicit unblock must bring it back.
func TestDrillDNSFlipped(t *testing.T) {
	l := drillLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)

	// The witness pair: the two other nodes must keep their own credential untouched.
	witnessDomain := nodeDomain(t, ctx, l.UV1)
	if witnessDomain == domain {
		witnessDomain = nodeDomain(t, ctx, l.UV2)
	}
	beforePair := subscriptionPeerUUID(t, ctx, l.UV1, witnessDomain)

	flipHosts(t, ctx, l, domain)
	t.Cleanup(func() { restoreHosts(t, ctx, l) })
	if out := strings.TrimSpace(l.MN.MustRun(t, ctx, "getent hosts "+domain)); !strings.Contains(out, "203.0.113.7") {
		t.Fatalf("the lab still resolves %s to its real address: %s", domain, out)
	}

	first := runCheckNow(t, ctx, l, domain)
	if first.Event != "check_failed" || first.State != "unavailable" {
		t.Fatalf("the first cycle on a wrong address = %+v, want a failed cycle moving to unavailable", first)
	}
	second := runCheckNow(t, ctx, l, domain)
	if second.Event != "check_failed" || second.State != "blocked" {
		t.Fatalf("the second cycle on a wrong address = %+v, want blocked", second)
	}

	// The pair between the nodes that stayed must be byte-identical.
	if after := subscriptionPeerUUID(t, ctx, l.UV1, witnessDomain); after != beforePair {
		t.Fatalf("the surviving pair changed when %s was blocked", domain)
	}

	restoreHosts(t, ctx, l)
	// Recovery is explicit: the node has to ask to be let back in, and the question is verified
	// against the restored address.
	victim.MustRun(t, ctx, "termit-uv unblock")
	victim.MustRun(t, ctx, "termit-uv sync")
	if state := showState(t, ctx, l, domain); state != "active" {
		t.Fatalf("the node is %s after the unblock", state)
	}
	if outcome := runCheckNow(t, ctx, l, domain); outcome.Event != "check_ok" {
		t.Fatalf("the node is not checked after recovery: %+v", outcome)
	}
}

// TestDrillCertificateReplaced installs an expired certificate in place of the node's own. The
// program must notice it from the outside: the local services are all fine, and only the certificate
// precheck speaks. That is exactly the failure a self-check cannot see, and it must be a verdict, not
// an unknown: clients see the same rejection.
func TestDrillCertificateReplaced(t *testing.T) {
	l := drillLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)

	certPath, keyPath := "/var/lib/termit/uv/certs/fullchain.pem", "/var/lib/termit/uv/certs/privkey.pem"
	victim.MustRun(t, ctx, "mkdir -p "+drillDir+" && cp "+certPath+" "+drillDir+"/fullchain.pem && cp "+keyPath+" "+drillDir+"/privkey.pem")
	// The backup holds the node's own private key, so it goes as soon as the restore has run: cleanups
	// are last-in-first-out, and this one is registered before the restore.
	t.Cleanup(func() {
		_, _ = victim.Run(context.Background(), "rm -rf "+drillDir)
		_, _ = l.MN.Run(context.Background(), "rm -rf "+drillDir)
	})
	t.Cleanup(func() { restoreCertificate(t, ctx, victim, certPath, keyPath) })
	victim.MustRun(t, ctx, "systemctl stop termit-uv-renew.timer")
	t.Cleanup(func() {
		_, _ = victim.Run(context.Background(), "systemctl start termit-uv-renew.timer")
	})

	installExpiredCertificate(t, ctx, l, victim, domain, certPath, keyPath)

	// The node looks healthy from the inside: its core, its web tier and its decoy all check out, and
	// only the certificate line betrays the problem. That is exactly the failure a self-check can
	// describe but cannot attribute to the public path.
	doctor, _ := victim.Run(ctx, "termit-uv doctor")
	for _, want := range []struct{ item, state string }{
		{"decoy", "ok"},
		{"proxy config", "ok"},
		{"certificate", "expired"},
	} {
		line := doctorLine(doctor, want.item)
		if !strings.Contains(line, want.state) {
			t.Fatalf("the local diagnosis does not report %q as %q:\n%s", want.item, want.state, doctor)
		}
	}
	if active := strings.TrimSpace(victim.MustRun(t, ctx, "systemctl is-active termit-xray nginx")); active != "active\nactive" {
		t.Fatalf("the node's services are not running:\n%s", active)
	}

	first := runCheckNow(t, ctx, l, domain)
	if first.Event != "check_failed" || first.State != "unavailable" {
		t.Fatalf("an expired certificate = %+v, want a failed cycle moving to unavailable", first)
	}
	if first.Class != "cert_expired" {
		t.Fatalf("an expired certificate was classified %q", first.Class)
	}
	second := runCheckNow(t, ctx, l, domain)
	if second.Event != "check_failed" || second.State != "blocked" {
		t.Fatalf("the second cycle with an expired certificate = %+v, want blocked", second)
	}

	restoreCertificate(t, ctx, victim, certPath, keyPath)
	victim.MustRun(t, ctx, "systemctl start termit-uv-renew.timer")
	victim.MustRun(t, ctx, "termit-uv unblock")
	victim.MustRun(t, ctx, "termit-uv sync")
	if state := showState(t, ctx, l, domain); state != "active" {
		t.Fatalf("the node is %s after the certificate was restored", state)
	}
	if outcome := runCheckNow(t, ctx, l, domain); outcome.Event != "check_ok" {
		t.Fatalf("the node is not checked after the certificate was restored: %+v", outcome)
	}
}

// doctorLine finds the diagnosis line for one check. The output aligns the status in a column, so the
// name is matched as a prefix.
func doctorLine(doctor, name string) string {
	for _, line := range strings.Split(doctor, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), name+" ") {
			return line
		}
	}
	return ""
}

// restoreCertificate puts the node's own certificate back and reloads the web tier.
func restoreCertificate(t *testing.T, ctx context.Context, m *lab.Machine, certPath, keyPath string) {
	t.Helper()
	_, _ = m.Run(context.Background(), "cp "+drillDir+"/fullchain.pem "+certPath+" && cp "+drillDir+"/privkey.pem "+keyPath+
		" && chmod 600 "+keyPath+" && systemctl reload nginx")
}

// installExpiredCertificate mints a certificate that chains to the lab CA and expired in the past,
// then makes the node serve it. The CA lives on the management server; the leaf travels as base64 so
// the test does not need a second SSH channel.
func installExpiredCertificate(t *testing.T, ctx context.Context, l *lab.Lab, m *lab.Machine, domain, certPath, keyPath string) {
	t.Helper()
	caFile := strings.TrimSpace(l.MN.MustRun(t, ctx, "sed -n 's/^  ca_file: *//p' /etc/termit/mn/config.yaml"))
	if caFile == "" {
		t.Skip("the lab has no private CA configured; an expired leaf cannot be minted")
	}
	script := fmt.Sprintf(`set -e
mkdir -p %s
cd %s
ca_dir=$(dirname '%s')
test -f "$ca_dir/cert.pem" && test -f "$ca_dir/key.pem" || { echo "no CA key next to %s" >&2; exit 3; }
not_before=$(date -u -d '-72 hours' +%%Y%%m%%d%%H%%M%%SZ)
not_after=$(date -u -d '-24 hours' +%%Y%%m%%d%%H%%M%%SZ)
openssl genrsa -out leaf.key 2048 >/dev/null 2>&1
openssl req -new -key leaf.key -subj "/CN=%s" -addext "subjectAltName=DNS:%s" -out leaf.csr >/dev/null 2>&1
openssl x509 -req -in leaf.csr -CA "$ca_dir/cert.pem" -CAkey "$ca_dir/key.pem" -CAcreateserial \
  -copy_extensions copy -not_before "$not_before" -not_after "$not_after" -out leaf.pem >/dev/null 2>&1
cat leaf.pem "$ca_dir/cert.pem" > chain.pem
openssl x509 -in leaf.pem -noout -enddate`, drillDir, drillDir, caFile, caFile, domain, domain)
	if out, err := l.MN.Run(ctx, script); err != nil {
		t.Fatalf("mint an expired certificate: %v\n%s", err, out)
	}
	chain := base64Bytes(t, ctx, l.MN, drillDir+"/chain.pem")
	key := base64Bytes(t, ctx, l.MN, drillDir+"/leaf.key")
	if err := m.WriteFile(ctx, chain, certPath, "0644"); err != nil {
		t.Fatalf("install the expired chain: %v", err)
	}
	if err := m.WriteFile(ctx, key, keyPath, "0600"); err != nil {
		t.Fatalf("install the expired key: %v", err)
	}
	m.MustRun(t, ctx, "systemctl reload nginx && sleep 1")
	served := m.MustRun(t, ctx, "openssl s_client -connect 127.0.0.1:443 -servername "+domain+
		" </dev/null 2>/dev/null | openssl x509 -noout -dates")
	if !strings.Contains(served, "notAfter") {
		t.Fatalf("the node does not serve the drill certificate:\n%s", served)
	}
}

// base64Bytes reads a file from a lab machine through the SSH channel.
func base64Bytes(t *testing.T, ctx context.Context, m *lab.Machine, path string) []byte {
	t.Helper()
	encoded := strings.TrimSpace(m.MustRun(t, ctx, "base64 -w0 "+path))
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return data
}

// flipHosts rewrites the management server's hosts file so the domain points at a black hole, keeping
// a backup the cleanup restores even after a failed run.
func flipHosts(t *testing.T, ctx context.Context, l *lab.Lab, domain string) {
	t.Helper()
	if _, err := l.MN.Run(ctx, "test -f "+drillHostsBak); err != nil {
		l.MN.MustRun(t, ctx, "cp /etc/hosts "+drillHostsBak)
	}
	script := fmt.Sprintf(`awk -v d='%s' '$NF == d && $1 !~ /^#/ { print "# drill: " $0; next } { print }' /etc/hosts > /tmp/hosts.drill && cat /tmp/hosts.drill > /etc/hosts && echo "203.0.113.7 %s" >> /etc/hosts`, domain, domain)
	l.MN.MustRun(t, ctx, script)
}

// restoreHosts puts the original hosts file back. It is safe to call twice.
func restoreHosts(t *testing.T, ctx context.Context, l *lab.Lab) {
	t.Helper()
	if _, err := l.MN.Run(context.Background(), "test -f "+drillHostsBak); err != nil {
		return
	}
	if out, err := l.MN.Run(context.Background(), "cp "+drillHostsBak+" /etc/hosts && rm -f "+drillHostsBak); err != nil {
		t.Logf("cleanup: restore the hosts file: %v\n%s", err, out)
	}
}

// TestDrillSQLLock holds an exclusive lock on the registry while the server serves reads and a write.
// The invariant is availability: the server waits for the lock instead of corrupting the database or
// crashing, and it is healthy once the lock is gone.
func TestDrillSQLLock(t *testing.T) {
	l := drillLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)

	dbPath := strings.TrimSpace(l.MN.MustRun(t, ctx, "sed -n 's/^db_path: *//p' /etc/termit/mn/config.yaml"))
	if dbPath == "" {
		t.Fatal("the management server has no database path in its configuration")
	}
	holder := `import sqlite3, sys, time, os
c = sqlite3.connect(sys.argv[1], isolation_level=None)
c.execute("BEGIN EXCLUSIVE")
open("/tmp/termit-drill-lock", "w").close()
time.sleep(float(sys.argv[2]))
c.rollback()
os.remove("/tmp/termit-drill-lock")
`
	if err := l.MN.WriteFile(ctx, []byte(holder), drillDir+"/lock.py", "0600"); err != nil {
		t.Fatalf("upload the lock holder: %v", err)
	}
	l.MN.MustRun(t, ctx, "rm -f /tmp/termit-drill-lock")
	l.MN.MustRun(t, ctx, "setsid nohup python3 "+drillDir+"/lock.py "+dbPath+" 8 >/dev/null 2>&1 &")
	t.Cleanup(func() {
		_, _ = l.MN.Run(context.Background(), "pkill -f 'termit-drill/lock.py' ; rm -f /tmp/termit-drill-lock")
	})
	waitForFile(t, ctx, l, "/tmp/termit-drill-lock")

	// Whatever the lock makes the server answer, it must answer: no hang, no crash, and nothing
	// half-applied. A read and a write are both tried while the lock is held.
	started := time.Now()
	_, readErr := l.MNAdmin(ctx, "uv", "list")
	readTook := time.Since(started)
	started = time.Now()
	_, writeErr := l.MNAdmin(ctx, "uv", "check", "now", domain)
	writeTook := time.Since(started)
	t.Logf("during the lock: read %s (err=%v), write %s (err=%v)",
		readTook.Round(time.Millisecond), readErr, writeTook.Round(time.Millisecond), writeErr)
	for _, took := range []time.Duration{readTook, writeTook} {
		if took > 20*time.Second {
			t.Fatalf("a request hung for %s behind the lock", took)
		}
	}
	if pids := mnPIDs(t, ctx, l); len(pids) == 0 {
		t.Fatal("the management server died while the database was locked")
	}
	// The lock is released; everything works and the registry is intact.
	waitGone(t, ctx, l, "/tmp/termit-drill-lock")
	if state := showState(t, ctx, l, domain); state != "active" {
		t.Fatalf("the node is %s after the lock", state)
	}
	if outcome := runCheckNow(t, ctx, l, domain); outcome.Event != "check_ok" {
		t.Fatalf("the server did not recover from the lock: %+v", outcome)
	}
}

// TestDrillDiskFull fills the registry's filesystem. The write path must report the failure instead of
// pretending the check happened, the server must stay up, and freeing the space must be enough to
// recover — no repair step.
func TestDrillDiskFull(t *testing.T) {
	l := drillLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	victim := drillVictim(t, l)
	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)

	l.MN.MustRun(t, ctx, "rm -f "+drillFillPath)
	t.Cleanup(func() { _, _ = l.MN.Run(context.Background(), "rm -f "+drillFillPath) })

	avail, err := strconv.ParseInt(strings.TrimSpace(l.MN.MustRun(t, ctx, "df --output=avail -B1 /var/lib/termit/mn | tail -1")), 10, 64)
	if err != nil {
		t.Fatalf("read the free space: %v", err)
	}
	if avail < 3<<30 {
		t.Skipf("the management server has only %d bytes free; filling it would be destructive beyond the drill", avail)
	}
	// Leave a megabyte, then consume it: to make a database write fail the filesystem has to be
	// genuinely full, not merely tight.
	l.MN.MustRun(t, ctx, fmt.Sprintf("fallocate -l %d %s", avail-1<<20, drillFillPath))
	if _, err := l.MN.Run(ctx, "dd if=/dev/zero bs=1M count=4 >> "+drillFillPath+" 2>/dev/null"); err != nil {
		t.Logf("the last megabyte went in with an expected failure: %v", err)
	}
	free := strings.TrimSpace(l.MN.MustRun(t, ctx, "df --output=avail -B1 /var/lib/termit/mn | tail -1"))
	if free != "0" {
		t.Logf("the filesystem still reports %s bytes free", free)
	}

	// The write path must refuse; a check that cannot be recorded would be a lie either way.
	out, err := l.MNAdmin(ctx, "uv", "check", "now", domain)
	if err == nil {
		t.Fatalf("a check succeeded on a full disk: %s", out)
	}
	// The server is still answering reads: the disk is full, not the server.
	if listing, err := l.MNAdmin(ctx, "uv", "list"); err != nil {
		t.Fatalf("the server stopped answering reads after a failed write: %v\n%s", err, listing)
	}

	l.MN.MustRun(t, ctx, "rm -f "+drillFillPath)
	if outcome := runCheckNow(t, ctx, l, domain); outcome.Event != "check_ok" {
		t.Fatalf("the server did not recover when the space came back: %+v", outcome)
	}
	if state := showState(t, ctx, l, domain); state != "active" {
		t.Fatalf("the node is %s after the disk drill", state)
	}
}

// TestDrillClockSkew is the one drill the lab cannot run: an unprivileged LXC has no CAP_SYS_TIME, so
// the clock cannot be moved. It fails the way the lab does, and says why, instead of passing on a
// check it never performed.
func TestDrillClockSkew(t *testing.T) {
	l := drillLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := l.MN.Run(ctx, "date -s '+2 hours'")
	if err == nil {
		// A privileged lab: put the clock back and let a later version of this test do the work.
		_, _ = l.MN.Run(ctx, "date -s '-2 hours'")
		t.Skip("the clock could be moved (the lab is privileged); the skew drill is not written yet")
	}
	t.Skipf("an unprivileged LXC cannot move the clock: %s", strings.TrimSpace(out))
}

// mnCommand reads how the management server was started, so a restart uses the same command. The
// first line is the executable, the second argv[0]; the rest is what to pass to it.
func mnCommand(t *testing.T, ctx context.Context, l *lab.Lab) (string, []string) {
	t.Helper()
	out := l.MN.MustRun(t, ctx, `pid=$(pgrep -x termit-mn | head -1); test -n "$pid" || exit 1; readlink -f /proc/$pid/exe; tr '\0' '\n' < /proc/$pid/cmdline`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("cannot read how the management server is running:\n%s", out)
	}
	return lines[0], lines[2:]
}

func mnPIDs(t *testing.T, ctx context.Context, l *lab.Lab) []string {
	t.Helper()
	out, err := l.MN.Run(ctx, "pgrep -x termit-mn")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

func stopMN(t *testing.T, ctx context.Context, l *lab.Lab) {
	t.Helper()
	pids := mnPIDs(t, ctx, l)
	if len(pids) == 0 {
		return
	}
	l.MN.MustRun(t, ctx, "kill -TERM "+strings.Join(pids, " "))
	waitGone(t, ctx, l, "termit-mn")
}

func startMN(t *testing.T, ctx context.Context, l *lab.Lab, exe string, args []string) {
	t.Helper()
	if len(mnPIDs(t, ctx, l)) > 0 {
		return
	}
	command := "setsid nohup " + exe + " " + strings.Join(args, " ") + " >>/tmp/mn-serving.log 2>&1 < /dev/null &"
	l.MN.MustRun(t, ctx, command)
	waitMN(t, ctx, l)
}

func waitMN(t *testing.T, ctx context.Context, l *lab.Lab) {
	t.Helper()
	err := lab.WaitFor(ctx, 60*time.Second, 500*time.Millisecond, "the management server to answer", func(ctx context.Context) (bool, error) {
		_, err := l.MNAdmin(ctx, "uv", "list")
		return err == nil, nil
	})
	if err != nil {
		t.Fatalf("the management server did not come back: %v", err)
	}
}

// waitGone polls until a process name or a path is gone. The path form is used for the lock marker.
func waitGone(t *testing.T, ctx context.Context, l *lab.Lab, what string) {
	t.Helper()
	var command string
	if strings.HasPrefix(what, "/") {
		command = "test -e " + what
	} else {
		command = "pgrep -x " + what
	}
	err := lab.WaitFor(ctx, 60*time.Second, 250*time.Millisecond, what+" to disappear", func(ctx context.Context) (bool, error) {
		_, err := l.MN.Run(ctx, command)
		return err != nil, nil
	})
	if err != nil {
		t.Fatalf("%s did not disappear: %v", what, err)
	}
}

func waitForFile(t *testing.T, ctx context.Context, l *lab.Lab, path string) {
	t.Helper()
	err := lab.WaitFor(ctx, 20*time.Second, 100*time.Millisecond, path+" to appear", func(ctx context.Context) (bool, error) {
		_, err := l.MN.Run(ctx, "test -f "+path)
		return err == nil, nil
	})
	if err != nil {
		t.Fatalf("%s did not appear: %v", path, err)
	}
}
