package netcounters_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/netcounters"
)

// fixture writes the four files Read parses under a temporary proc root.
func fixture(t *testing.T, netstat, snmp, tcp, tcp6 string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"netstat": netstat,
		"snmp":    snmp,
		"tcp":     tcp,
	}
	if tcp6 != "" {
		files["tcp6"] = tcp6
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

const (
	netstatHeader = "TcpExt: SyncookiesSent SyncookiesRecv ListenDrops TCPSynRetrans TCPTimeouts\n"
	snmpHeader    = "Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets\n"
	tcpHeader     = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
)

func TestReadParsesBothTables(t *testing.T) {
	t.Parallel()
	root := fixture(t,
		netstatHeader+"TcpExt: 0 0 56 904594 1084033\n",
		snmpHeader+"Tcp: 1 200 120000 -1 114993 130950 412 900\n",
		tcpHeader+
			"   0: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1\n"+
			"   1: 0A000001:01BB 0A000002:9C40 03 00000000:00000000 00:00000000 00000000     0        0 2\n",
		"",
	)
	snapshot, err := netcounters.Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snapshot.ListenDrops != 56 || snapshot.SynRetrans != 904594 || snapshot.Timeouts != 1084033 {
		t.Errorf("netstat fields are wrong: %+v", snapshot)
	}
	if snapshot.ActiveOpens != 114993 || snapshot.PassiveOpens != 130950 || snapshot.AttemptFails != 412 {
		t.Errorf("snmp fields are wrong: %+v", snapshot)
	}
	if snapshot.SynRecv != 1 {
		t.Errorf("SynRecv = %d, want the one socket in state 03", snapshot.SynRecv)
	}
}

func TestReadCountsSynRecvAcrossFamilies(t *testing.T) {
	t.Parallel()
	synRecv := "   1: 0A000001:01BB 0A000002:9C40 03 00000000:00000000 00:00000000 00000000     0        0 2\n"
	root := fixture(t,
		netstatHeader+"TcpExt: 0 0 0 0 0\n",
		snmpHeader+"Tcp: 1 200 120000 -1 0 0 0 0\n",
		tcpHeader+synRecv,
		tcpHeader+synRecv+synRecv,
	)
	snapshot, err := netcounters.Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snapshot.SynRecv != 3 {
		t.Errorf("SynRecv = %d, want 3", snapshot.SynRecv)
	}
}

func TestReadFailsWithoutTheTables(t *testing.T) {
	t.Parallel()
	if _, err := netcounters.Read(t.TempDir()); err == nil {
		t.Fatal("an empty proc root was accepted")
	}
}

func TestDifferenceAndJudge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		before  netcounters.Snapshot
		after   netcounters.Snapshot
		verdict netcounters.Verdict
	}{
		{
			name:   "a healthy sample",
			before: netcounters.Snapshot{PassiveOpens: 1000},
			after:  netcounters.Snapshot{PassiveOpens: 2000, SynRetrans: 2},
			// 1000 completed, 2 retransmitted: 0.2%, below the warn floor.
			verdict: netcounters.VerdictOK,
		},
		{
			name:    "answers being lost",
			before:  netcounters.Snapshot{PassiveOpens: 1000},
			after:   netcounters.Snapshot{PassiveOpens: 1035, SynRetrans: 61},
			verdict: netcounters.VerdictFail, // 61/96 = 64%
		},
		{
			name:    "a moderate loss",
			before:  netcounters.Snapshot{PassiveOpens: 1000},
			after:   netcounters.Snapshot{PassiveOpens: 1035, SynRetrans: 5},
			verdict: netcounters.VerdictWarn, // 5/40 = 12.5%
		},
		{
			name:    "the node dropped them itself",
			before:  netcounters.Snapshot{PassiveOpens: 10},
			after:   netcounters.Snapshot{PassiveOpens: 20, ListenDrops: 3},
			verdict: netcounters.VerdictFail,
		},
		{
			name:    "no traffic at all",
			before:  netcounters.Snapshot{PassiveOpens: 100},
			after:   netcounters.Snapshot{PassiveOpens: 100},
			verdict: netcounters.VerdictUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			delta := netcounters.Difference(tc.before, tc.after, 60*time.Second)
			verdict, detail, _ := netcounters.Judge(delta)
			if verdict != tc.verdict {
				t.Errorf("verdict = %q, want %q (%s)", verdict, tc.verdict, detail)
			}
			if detail == "" {
				t.Error("the verdict carries no detail")
			}
		})
	}
}

func TestDifferenceClampsAReset(t *testing.T) {
	t.Parallel()
	delta := netcounters.Difference(
		netcounters.Snapshot{PassiveOpens: 1000, SynRetrans: 500},
		netcounters.Snapshot{PassiveOpens: 10, SynRetrans: 5},
		time.Minute,
	)
	if delta.PassiveOpens != 0 || delta.SynRetrans != 0 {
		t.Errorf("a counter reset produced a negative delta: %+v", delta)
	}
}

func TestJudgeSaysUnknownRatherThanOKFromSilence(t *testing.T) {
	t.Parallel()
	verdict, detail, hint := netcounters.Judge(netcounters.Difference(netcounters.Snapshot{}, netcounters.Snapshot{}, time.Minute))
	if verdict != netcounters.VerdictUnknown {
		t.Fatalf("verdict = %q, want unknown", verdict)
	}
	if !strings.Contains(hint, "client") {
		t.Errorf("the hint does not tell the operator how to get a sample: %q", hint)
	}
	if !strings.Contains(detail, "no inbound") {
		t.Errorf("detail = %q", detail)
	}
}
