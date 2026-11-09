package probe

import (
	"testing"
	"time"
)

func TestBreakerOpensAfterRepeatedFailures(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	b := newBreaker(3, 5*time.Minute)
	b.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if allowed, _ := b.allow(); !allowed {
			t.Fatalf("the breaker was open after %d failure(s)", i+1)
		}
		b.record(false)
	}
	if allowed, _ := b.allow(); !allowed {
		t.Fatal("the breaker opened before the threshold")
	}
	b.record(false)
	allowed, wait := b.allow()
	if allowed {
		t.Fatal("the breaker stayed closed after the threshold")
	}
	if wait != 5*time.Minute {
		t.Errorf("wait = %v, want the cooldown", wait)
	}

	// During the cooldown it stays open, and the remaining time shrinks.
	now = now.Add(time.Minute)
	if allowed, wait := b.allow(); allowed || wait != 4*time.Minute {
		t.Errorf("after a minute: allowed=%v wait=%v", allowed, wait)
	}
	// After the cooldown it closes again by itself: a prober that comes back must be used.
	now = now.Add(4 * time.Minute)
	if allowed, _ := b.allow(); !allowed {
		t.Error("the breaker did not close after the cooldown")
	}
}

func TestBreakerResetsOnSuccess(t *testing.T) {
	t.Parallel()
	b := newBreaker(2, time.Minute)
	b.record(false)
	b.record(true)
	b.record(false)
	if allowed, _ := b.allow(); !allowed {
		t.Error("a success did not reset the failure count")
	}
}

func TestBreakerDefaults(t *testing.T) {
	t.Parallel()
	b := newBreaker(0, 0)
	if b.threshold != 3 || b.cooldown != 5*time.Minute {
		t.Errorf("defaults = %d, %v", b.threshold, b.cooldown)
	}
}

func TestProbeAnswersUnknownWhileTheBreakerIsOpen(t *testing.T) {
	t.Parallel()
	// A worker whose breaker is already open must not start anything: the core is not there to start.
	worker := &Worker{
		cfg:     Config{Binary: "definitely-not-a-binary", WorkDir: t.TempDir(), ProbeURL: "https://example.com/"},
		breaker: newBreaker(1, time.Minute),
	}
	worker.breaker.record(false)

	results, err := worker.Probe(t.Context(), []Target{{ID: "uv_one", Address: "one.example.com", Port: 443, ServerName: "one.example.com", Path: "/p/", Mode: "packet-up", UUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5"}})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if !results[0].Unknown() {
		t.Errorf("class = %q, want worker_unavailable (unknown)", results[0].Class)
	}
	if results[0].OK {
		t.Error("a skipped probe reported success")
	}
}
