// Package netcounters turns the kernel's TCP counters into a verdict about the path to this node.
//
// It is observation only: it reads /proc, opens no sockets and contacts no host. That is deliberate.
// A node must not phone a third party to find out whether its own connections work, and the counters
// it already keeps answer the same question: a listener retransmits a SYN-ACK only for a SYN it
// received, so a growing retransmit count against a flat drop count means the answers are being lost
// on the way back.
package netcounters

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Snapshot is one reading of the counters this package uses. All values are cumulative since boot.
type Snapshot struct {
	// ListenDrops counts incoming SYNs the node's own kernel refused: a full accept queue or a
	// local firewall. This is the one counter that blames the node.
	ListenDrops int64
	// SynRetrans counts packets carrying a SYN that had to be sent again. On a listener that is an
	// unanswered SYN-ACK, which means the peer never got the answer.
	SynRetrans int64
	// Timeouts counts retransmission timeouts. It moves with SynRetrans and adds nothing the
	// verdict uses, but it is what an operator recognises from `nstat`.
	Timeouts int64
	// PassiveOpens counts inbound connections that completed the handshake.
	PassiveOpens int64
	// ActiveOpens counts connections this node started.
	ActiveOpens int64
	// AttemptFails counts outbound connection attempts that gave up.
	AttemptFails int64
	// SynRecv counts sockets sitting in SYN_RECV right now: the handshake is under way and the
	// final ACK has not arrived.
	SynRecv int64
}

// Delta is what changed between two snapshots over a known interval.
type Delta struct {
	Before  Snapshot
	After   Snapshot
	Elapsed time.Duration

	ListenDrops  int64
	SynRetrans   int64
	Timeouts     int64
	PassiveOpens int64
	ActiveOpens  int64
	AttemptFails int64
	// SynRecvPeak is the larger of the two SYN_RECV readings: a socket can appear and disappear
	// between the samples, so the peak is more informative than either end.
	SynRecvPeak int64
}

// Difference subtracts two snapshots. Counters reset when the interface table is rebuilt, so a
// negative difference is clamped to zero rather than reported as a bogus rate.
func Difference(before, after Snapshot, elapsed time.Duration) Delta {
	clamp := func(value int64) int64 {
		if value < 0 {
			return 0
		}
		return value
	}
	peak := before.SynRecv
	if after.SynRecv > peak {
		peak = after.SynRecv
	}
	return Delta{
		Before:       before,
		After:        after,
		Elapsed:      elapsed,
		ListenDrops:  clamp(after.ListenDrops - before.ListenDrops),
		SynRetrans:   clamp(after.SynRetrans - before.SynRetrans),
		Timeouts:     clamp(after.Timeouts - before.Timeouts),
		PassiveOpens: clamp(after.PassiveOpens - before.PassiveOpens),
		ActiveOpens:  clamp(after.ActiveOpens - before.ActiveOpens),
		AttemptFails: clamp(after.AttemptFails - before.AttemptFails),
		SynRecvPeak:  peak,
	}
}

// InboundTraffic is how many inbound handshakes the sample saw at all: completed ones plus the
// answers that had to be repeated.
func (d Delta) InboundTraffic() int64 { return d.PassiveOpens + d.SynRetrans }

// AnswerLoss is the share of inbound handshakes whose answer had to be retransmitted, in [0, 1].
// SynRetrans also counts outbound SYNs, so it is an upper bound; when the node starts almost no
// connections itself the bound is tight.
func (d Delta) AnswerLoss() float64 {
	total := d.InboundTraffic()
	if total == 0 {
		return 0
	}
	return float64(d.SynRetrans) / float64(total)
}

// PerSecond converts a counter delta to a rate.
func (d Delta) PerSecond(value int64) float64 {
	if d.Elapsed <= 0 {
		return 0
	}
	return float64(value) / d.Elapsed.Seconds()
}

// Verdict is what the sample supports.
type Verdict string

const (
	VerdictOK      Verdict = "ok"
	VerdictWarn    Verdict = "warn"
	VerdictFail    Verdict = "fail"
	VerdictUnknown Verdict = "unknown"
)

// The thresholds. A healthy node completes its handshakes: the retransmit share is a fraction of a
// percent. A node losing one in five inbound handshakes is not usable, which is where fail starts.
const (
	LossWarn = 0.02
	LossFail = 0.20
)

// Judge turns a sample into a verdict and the two strings a check needs: what was seen, and what to
// do about it. It never reports ok from silence: a sample with no inbound traffic is unknown, because
// a quiet minute and a working node look identical in the counters.
func Judge(d Delta) (Verdict, string, string) {
	if d.Elapsed <= 0 {
		return VerdictUnknown, "the sample has no length", "run it again with --seconds"
	}
	if d.ListenDrops > 0 {
		return VerdictFail,
			fmt.Sprintf("%d incoming SYNs were dropped by this node's own kernel", d.ListenDrops),
			"raise net.core.somaxconn and net.ipv4.tcp_max_syn_backlog, and check for a local firewall"
	}
	if d.InboundTraffic() == 0 {
		return VerdictUnknown,
			"no inbound connections were seen during the sample",
			"run it again while a client or the prober connects: a quiet node and a healthy node look the same"
	}

	loss := d.AnswerLoss()
	detail := fmt.Sprintf("%d inbound handshakes: %d completed, %d answers retransmitted (%.0f%%); "+
		"%d started outbound, %d gave up; %d in SYN_RECV at peak",
		d.InboundTraffic(), d.PassiveOpens, d.SynRetrans, loss*100,
		d.ActiveOpens, d.AttemptFails, d.SynRecvPeak)
	hint := "an answer that had to be repeated means the peer never received it: this is the path between " +
		"this node and its clients, not the node itself. `docs/OPERATIONS.md` section 16.1 has the recipe"

	switch {
	case loss >= LossFail:
		return VerdictFail, detail, hint
	case loss >= LossWarn:
		return VerdictWarn, detail, hint
	}
	return VerdictOK, detail, ""
}

// Read parses the counters under procRoot. procRoot is a parameter so tests can point at fixtures.
func Read(procRoot string) (Snapshot, error) {
	var snapshot Snapshot
	var problems []error

	netstat, err := readPair(filepath.Join(procRoot, "net", "netstat"))
	if err != nil {
		problems = append(problems, err)
	} else {
		for _, field := range []struct {
			name   string
			target *int64
		}{
			{"ListenDrops", &snapshot.ListenDrops},
			{"TCPSynRetrans", &snapshot.SynRetrans},
			{"TCPTimeouts", &snapshot.Timeouts},
		} {
			if value, ok := netstat["TcpExt"][field.name]; ok {
				*field.target = value
			}
		}
	}

	snmp, err := readPair(filepath.Join(procRoot, "net", "snmp"))
	if err != nil {
		problems = append(problems, err)
	} else {
		if value, ok := snmp["Tcp"]["ActiveOpens"]; ok {
			snapshot.ActiveOpens = value
		}
		if value, ok := snmp["Tcp"]["PassiveOpens"]; ok {
			snapshot.PassiveOpens = value
		}
		if value, ok := snmp["Tcp"]["AttemptFails"]; ok {
			snapshot.AttemptFails = value
		}
	}

	synRecv, err := countSynRecv(procRoot)
	if err != nil {
		problems = append(problems, err)
	} else {
		snapshot.SynRecv = synRecv
	}

	if err := errors.Join(problems...); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// readPair parses the header/value pairs of /proc/net/netstat and /proc/net/snmp: a "<Prefix>:"
// header line, then a "<Prefix>:" value line of the same length.
func readPair(path string) (map[string]map[string]int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	out := make(map[string]map[string]int64)
	var pending []string
	var pendingPrefix string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		prefix, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) > 0 && len(pending) == 0 {
			// A header line: names, no numbers. Recognise it by the first field not being numeric.
			if _, err := strconv.ParseInt(fields[0], 10, 64); err != nil {
				pending = fields
				pendingPrefix = prefix
				continue
			}
		}
		if pendingPrefix != prefix || len(fields) != len(pending) {
			pending = nil
			continue
		}
		values := make(map[string]int64, len(fields))
		for i, name := range pending {
			value, err := strconv.ParseInt(fields[i], 10, 64)
			if err != nil {
				values = nil
				break
			}
			values[name] = value
		}
		if values != nil {
			out[prefix] = values
		}
		pending = nil
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no counters in %s", path)
	}
	return out, nil
}

// countSynRecv counts sockets in the SYN_RECV state, both address families.
func countSynRecv(procRoot string) (int64, error) {
	var total int64
	var problems []error
	for _, name := range []string{"tcp", "tcp6"} {
		raw, err := os.ReadFile(filepath.Join(procRoot, "net", name))
		if err != nil {
			if os.IsNotExist(err) && name == "tcp6" {
				continue
			}
			problems = append(problems, fmt.Errorf("read %s: %w", name, err))
			continue
		}
		lines := strings.Split(string(raw), "\n")
		for index, line := range lines {
			if index == 0 || strings.TrimSpace(line) == "" {
				continue // the header
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			if fields[3] == "03" { // SYN_RECV
				total++
			}
		}
	}
	if err := errors.Join(problems...); err != nil {
		return total, err
	}
	return total, nil
}
