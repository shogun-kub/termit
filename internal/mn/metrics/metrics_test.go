package metrics

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func render(t *testing.T, registry *Registry, snap Snapshot) string {
	t.Helper()
	var buf bytes.Buffer
	if snap.Now.IsZero() {
		snap.Now = testNow
	}
	registry.Write(&buf, snap)
	return buf.String()
}

// line returns the first line that starts with prefix, so a test can assert on one sample without
// depending on the order of the whole document.
func line(t *testing.T, body, prefix string) string {
	t.Helper()
	for _, candidate := range strings.Split(body, "\n") {
		if strings.HasPrefix(candidate, prefix) {
			return candidate
		}
	}
	t.Fatalf("no line starts with %q in:\n%s", prefix, body)
	return ""
}

func TestCountersCarryTheirResultLabel(t *testing.T) {
	t.Parallel()
	registry := New(testNow)

	registry.ObserveEnrollment(ResultOK)
	registry.ObserveEnrollment(ResultPending)
	registry.ObserveEnrollment(ResultRefused)
	registry.ObserveEnrollment("something else") // an unknown outcome is a refusal, never a success
	registry.ObserveSync(ResultOK)
	registry.ObserveSync(ResultRefused)
	registry.ObserveCheck(ResultOK, 120*time.Millisecond)
	registry.ObserveCheck(ResultFailed, time.Second)
	registry.ObserveCheck(ResultUnknown, 2*time.Second)
	registry.ObserveCheck(ResultError, 30*time.Second)

	body := render(t, registry, Snapshot{})

	for _, want := range []string{
		`termit_mn_enrollments_total{result="ok"} 1`,
		`termit_mn_enrollments_total{result="pending"} 1`,
		`termit_mn_enrollments_total{result="refused"} 2`,
		`termit_mn_syncs_total{result="ok"} 1`,
		`termit_mn_syncs_total{result="refused"} 1`,
		`termit_mn_checks_total{result="ok"} 1`,
		`termit_mn_checks_total{result="failed"} 1`,
		`termit_mn_checks_total{result="unknown"} 1`,
		`termit_mn_checks_total{result="error"} 1`,
	} {
		if got := strings.Count(body, want); got != 1 {
			t.Errorf("%q appears %d times, want 1:\n%s", want, got, body)
		}
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	t.Parallel()
	registry := New(testNow)
	registry.ObserveCheck(ResultOK, 50*time.Millisecond)  // ≤ 0.1
	registry.ObserveCheck(ResultOK, 300*time.Millisecond) // ≤ 0.5
	registry.ObserveCheck(ResultOK, 3*time.Second)        // ≤ 5

	body := render(t, registry, Snapshot{})

	for _, want := range []string{
		`termit_mn_check_duration_seconds_bucket{le="0.1"} 1`,
		`termit_mn_check_duration_seconds_bucket{le="0.25"} 1`,
		`termit_mn_check_duration_seconds_bucket{le="0.5"} 2`,
		`termit_mn_check_duration_seconds_bucket{le="1"} 2`,
		`termit_mn_check_duration_seconds_bucket{le="2.5"} 2`,
		`termit_mn_check_duration_seconds_bucket{le="5"} 3`,
		`termit_mn_check_duration_seconds_bucket{le="10"} 3`,
		`termit_mn_check_duration_seconds_bucket{le="+Inf"} 3`,
		`termit_mn_check_duration_seconds_count 3`,
	} {
		if got := line(t, body, strings.Split(want, " ")[0]); got != want {
			t.Errorf("sample = %q, want %q", got, want)
		}
	}
	if got := line(t, body, "termit_mn_check_duration_seconds_sum"); got != "termit_mn_check_duration_seconds_sum 3.35" {
		t.Errorf("sum = %q, want 3.35", got)
	}
}

func TestStatesAreAlwaysReported(t *testing.T) {
	t.Parallel()
	registry := New(testNow)

	body := render(t, registry, Snapshot{Nodes: map[string]int{"active": 3, "pending": 1}})

	for _, want := range []string{
		`termit_mn_nodes{state="active"} 3`,
		`termit_mn_nodes{state="pending"} 1`,
		`termit_mn_nodes{state="unavailable"} 0`,
		`termit_mn_nodes{state="blocked"} 0`,
		`termit_mn_nodes{state="removed"} 0`,
	} {
		if got := line(t, body, strings.Split(want, " ")[0]); got != want {
			t.Errorf("sample = %q, want %q", got, want)
		}
	}
}

func TestQueueAgeIsClampedAtZero(t *testing.T) {
	t.Parallel()
	registry := New(testNow)

	overdue := testNow.Add(-90 * time.Second)
	body := render(t, registry, Snapshot{Due: 4, OldestDue: &overdue})
	if got := line(t, body, "termit_mn_check_queue_due"); got != "termit_mn_check_queue_due 4" {
		t.Errorf("due = %q", got)
	}
	if got := line(t, body, "termit_mn_check_queue_oldest_due_seconds"); got != "termit_mn_check_queue_oldest_due_seconds 90" {
		t.Errorf("age = %q, want 90", got)
	}

	// A schedule that is entirely in the future is not a backlog, and a negative gauge would read as
	// a clock problem.
	future := testNow.Add(time.Hour)
	body = render(t, registry, Snapshot{OldestDue: &future})
	if got := line(t, body, "termit_mn_check_queue_oldest_due_seconds"); got != "termit_mn_check_queue_oldest_due_seconds 0" {
		t.Errorf("age = %q, want 0", got)
	}
}

func TestTimestampsAndFlags(t *testing.T) {
	t.Parallel()
	registry := New(testNow)
	next := testNow.Add(24 * time.Hour)
	notAfter := testNow.Add(60 * 24 * time.Hour)

	body := render(t, registry, Snapshot{
		Version:             "0.1.0",
		SchemaVersion:       5,
		NextDue:             &next,
		CertificateNotAfter: &notAfter,
		ProberUp:            true,
	})

	if got := line(t, body, "termit_mn_build_info"); got != `termit_mn_build_info{version="0.1.0",schema_version="5"} 1` {
		t.Errorf("build info = %q", got)
	}
	if got := line(t, body, "termit_mn_prober_up"); got != "termit_mn_prober_up 1" {
		t.Errorf("prober = %q", got)
	}
	if got := line(t, body, "termit_mn_check_next_due_timestamp_seconds"); got != "termit_mn_check_next_due_timestamp_seconds "+strconv.FormatInt(next.Unix(), 10) {
		t.Errorf("next due = %q", got)
	}
	if got := line(t, body, "termit_mn_certificate_expiry_timestamp_seconds"); got != "termit_mn_certificate_expiry_timestamp_seconds "+strconv.FormatInt(notAfter.Unix(), 10) {
		t.Errorf("certificate = %q", got)
	}

	// No prober and no certificate are honest zeroes, not omissions: a missing metric would leave an
	// alert rule silently dead.
	body = render(t, registry, Snapshot{})
	if got := line(t, body, "termit_mn_prober_up"); got != "termit_mn_prober_up 0" {
		t.Errorf("prober = %q, want 0", got)
	}
	if got := line(t, body, "termit_mn_certificate_expiry_timestamp_seconds"); got != "termit_mn_certificate_expiry_timestamp_seconds 0" {
		t.Errorf("certificate = %q, want 0", got)
	}
}

func TestLabelsAreEscaped(t *testing.T) {
	t.Parallel()
	registry := New(testNow)
	body := render(t, registry, Snapshot{Version: `1"2\3`})
	if got := line(t, body, "termit_mn_build_info"); got != `termit_mn_build_info{version="1\"2\\3",schema_version="0"} 1` {
		t.Errorf("escaping = %q", got)
	}
}
