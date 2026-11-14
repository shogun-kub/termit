// Package metrics is the management server's own telemetry: the counters and gauges behind
// `/metrics`, and nothing else.
//
// The registry holds aggregates only. No node id, domain, token or key ever becomes a label: the
// endpoint is scraped by a monitoring agent, and a metric is the wrong place for an inventory. That is
// also why every gauge here is a count or a timestamp — the question "which node" belongs to
// `termit-mn status`, not to Prometheus.
package metrics

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Values of the `result` label.
const (
	// Enrollment outcomes: an admitted node, one parked for approval, one refused (bad invite, taken
	// domain, failed DNS or probe, rate limit).
	ResultOK      = "ok"
	ResultPending = "pending"
	ResultRefused = "refused"
	// Check outcomes: a real verdict either way, and the two ways of not having one. "unknown" means
	// the check produced no evidence, "error" means the cycle could not finish at all.
	ResultFailed  = "failed"
	ResultUnknown = "unknown"
	ResultError   = "error"
)

// checkBuckets are the latency buckets of the probe histogram, in seconds. A check is a certificate
// read, a TLS handshake and one proxied request; anything past ten seconds is a timeout, not a data
// point, so the last bucket is the useful ceiling.
var checkBuckets = [7]float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry accumulates everything the endpoint reports about the server's own work.
type Registry struct {
	started time.Time

	enrollments [3]atomic.Uint64
	syncs       [2]atomic.Uint64
	checks      [4]atomic.Uint64

	mu       sync.Mutex
	buckets  [len(checkBuckets) + 1]uint64
	checkSum float64
	checkN   uint64
}

// New returns an empty registry. started is when the server began serving, and becomes the uptime
// gauge.
func New(started time.Time) *Registry {
	return &Registry{started: started}
}

// ObserveEnrollment records one enrollment attempt. A caller that cannot classify the outcome should
// pass ResultRefused: an attempt that produced no node is a refusal from the mesh's point of view.
func (r *Registry) ObserveEnrollment(result string) {
	switch result {
	case ResultOK:
		r.enrollments[0].Add(1)
	case ResultPending:
		r.enrollments[1].Add(1)
	default:
		r.enrollments[2].Add(1)
	}
}

// ObserveSync records one agent sync: served, or refused.
func (r *Registry) ObserveSync(result string) {
	if result == ResultOK {
		r.syncs[0].Add(1)
		return
	}
	r.syncs[1].Add(1)
}

// ObserveCheck records one scheduler cycle: its verdict and how long it took.
func (r *Registry) ObserveCheck(result string, took time.Duration) {
	switch result {
	case ResultOK:
		r.checks[0].Add(1)
	case ResultFailed:
		r.checks[1].Add(1)
	case ResultUnknown:
		r.checks[2].Add(1)
	default:
		r.checks[3].Add(1)
	}

	seconds := took.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkN++
	r.checkSum += seconds
	// Every bucket whose bound the cycle fits into, because the exposition format is cumulative: a
	// scrape reads `le="5"` as "at most five seconds", not "between 2.5 and 5".
	for i, bound := range checkBuckets {
		if seconds <= bound {
			r.buckets[i]++
		}
	}
	r.buckets[len(checkBuckets)]++
}

// Snapshot is the part of the report the registry cannot know: it comes from the database and from
// the process's own configuration.
type Snapshot struct {
	// Version is the binary's version string, empty when unknown.
	Version string
	// SchemaVersion is the database schema in use.
	SchemaVersion int
	// Now fixes the clock the derived durations are measured against.
	Now time.Time
	// Nodes counts nodes per state. States that are absent are reported as zero, so a dashboard does
	// not have to guess.
	Nodes map[string]int
	// Due is how many nodes the scheduler should have looked at by Now.
	Due int
	// OldestDue is the earliest next_check_at among scheduled nodes, nil when none is scheduled;
	// NextDue is the earliest one still in the future.
	OldestDue *time.Time
	NextDue   *time.Time
	// ProberUp is false when no probe worker could be built: every check will then be unknown.
	ProberUp bool
	// CertificateNotAfter is the server's own certificate expiry, nil when it could not be read.
	CertificateNotAfter *time.Time
}

// states are reported in lifecycle order, including the ones with no nodes.
var states = []string{"active", "unavailable", "blocked", "pending", "removed"}

// Write renders the Prometheus text exposition. It never returns an error from the writer: a scrape
// that cannot be flushed is the transport's problem, and there is no partial state to roll back.
func (r *Registry) Write(w io.Writer, snap Snapshot) {
	now := snap.Now
	if now.IsZero() {
		now = time.Now()
	}

	fmt.Fprint(w, "# HELP termit_mn_up Whether the server answers.\n# TYPE termit_mn_up gauge\ntermit_mn_up 1\n")
	fmt.Fprint(w, "# HELP termit_mn_build_info Build and schema identity.\n# TYPE termit_mn_build_info gauge\n")
	fmt.Fprintf(w, "termit_mn_build_info{version=\"%s\",schema_version=\"%d\"} 1\n",
		label(snap.Version), snap.SchemaVersion)
	fmt.Fprint(w, "# HELP termit_mn_start_time_seconds When this process started.\n# TYPE termit_mn_start_time_seconds gauge\n")
	fmt.Fprintf(w, "termit_mn_start_time_seconds %d\n", r.started.Unix())

	fmt.Fprint(w, "# HELP termit_mn_nodes Nodes by state.\n# TYPE termit_mn_nodes gauge\n")
	for _, state := range states {
		fmt.Fprintf(w, "termit_mn_nodes{state=\"%s\"} %d\n", state, snap.Nodes[state])
	}

	fmt.Fprint(w, "# HELP termit_mn_prober_up Whether a probe worker could be built; 0 means every check is unknown.\n# TYPE termit_mn_prober_up gauge\n")
	fmt.Fprintf(w, "termit_mn_prober_up %d\n", boolGauge(snap.ProberUp))

	fmt.Fprint(w, "# HELP termit_mn_check_queue_due Nodes the scheduler should have checked by now.\n# TYPE termit_mn_check_queue_due gauge\n")
	fmt.Fprintf(w, "termit_mn_check_queue_due %d\n", snap.Due)
	fmt.Fprint(w, "# HELP termit_mn_check_queue_oldest_due_seconds How late the oldest due check is, in seconds.\n# TYPE termit_mn_check_queue_oldest_due_seconds gauge\n")
	fmt.Fprintf(w, "termit_mn_check_queue_oldest_due_seconds %s\n", secondsSince(snap.OldestDue, now))
	fmt.Fprint(w, "# HELP termit_mn_check_next_due_timestamp_seconds When the next scheduled check is due.\n# TYPE termit_mn_check_next_due_timestamp_seconds gauge\n")
	fmt.Fprintf(w, "termit_mn_check_next_due_timestamp_seconds %s\n", unixOrZero(snap.NextDue))

	fmt.Fprint(w, "# HELP termit_mn_certificate_expiry_timestamp_seconds When the server's own certificate expires.\n# TYPE termit_mn_certificate_expiry_timestamp_seconds gauge\n")
	fmt.Fprintf(w, "termit_mn_certificate_expiry_timestamp_seconds %s\n", unixOrZero(snap.CertificateNotAfter))

	enrollments := [3]*atomic.Uint64{&r.enrollments[0], &r.enrollments[1], &r.enrollments[2]}
	fmt.Fprint(w, "# HELP termit_mn_enrollments_total Enrollment attempts by outcome.\n# TYPE termit_mn_enrollments_total counter\n")
	for i, result := range []string{ResultOK, ResultPending, ResultRefused} {
		fmt.Fprintf(w, "termit_mn_enrollments_total{result=\"%s\"} %d\n", result, enrollments[i].Load())
	}

	fmt.Fprint(w, "# HELP termit_mn_syncs_total Agent syncs by outcome.\n# TYPE termit_mn_syncs_total counter\n")
	fmt.Fprintf(w, "termit_mn_syncs_total{result=\"%s\"} %d\n", ResultOK, r.syncs[0].Load())
	fmt.Fprintf(w, "termit_mn_syncs_total{result=\"%s\"} %d\n", ResultRefused, r.syncs[1].Load())

	fmt.Fprint(w, "# HELP termit_mn_checks_total Scheduler cycles by outcome.\n# TYPE termit_mn_checks_total counter\n")
	checks := [4]*atomic.Uint64{&r.checks[0], &r.checks[1], &r.checks[2], &r.checks[3]}
	for i, result := range []string{ResultOK, ResultFailed, ResultUnknown, ResultError} {
		fmt.Fprintf(w, "termit_mn_checks_total{result=\"%s\"} %d\n", result, checks[i].Load())
	}

	r.writeHistogram(w)
}

// writeHistogram renders the cycle duration histogram under its own lock, so a concurrent observation
// cannot produce a bucket count and a total that disagree.
func (r *Registry) writeHistogram(w io.Writer) {
	r.mu.Lock()
	buckets := r.buckets
	count, sum := r.checkN, r.checkSum
	r.mu.Unlock()

	fmt.Fprint(w, "# HELP termit_mn_check_duration_seconds How long one scheduler cycle took.\n# TYPE termit_mn_check_duration_seconds histogram\n")
	for i, bound := range checkBuckets {
		fmt.Fprintf(w, "termit_mn_check_duration_seconds_bucket{le=\"%g\"} %d\n", bound, buckets[i])
	}
	fmt.Fprintf(w, "termit_mn_check_duration_seconds_bucket{le=\"+Inf\"} %d\n", buckets[len(checkBuckets)])
	fmt.Fprintf(w, "termit_mn_check_duration_seconds_sum %g\n", sum)
	fmt.Fprintf(w, "termit_mn_check_duration_seconds_count %d\n", count)
}

// labelEscaper escapes a label value the way the exposition format requires.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func label(value string) string { return labelEscaper.Replace(value) }

func boolGauge(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unixOrZero(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "0"
	}
	return fmt.Sprintf("%d", at.Unix())
}

// secondsSince reports how long ago at was, clamped at zero: a timestamp in the future is not "late",
// and a negative gauge would look like a clock problem to an alert.
func secondsSince(at *time.Time, now time.Time) string {
	if at == nil || at.IsZero() {
		return "0"
	}
	seconds := now.Sub(*at).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%g", seconds)
}
