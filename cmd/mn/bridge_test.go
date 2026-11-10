package main

import (
	"context"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/probe"
)

// classProber is an enroll.Prober whose verdict the test chooses.
type classProber struct {
	outcome enroll.Outcome
}

func (p classProber) Probe(context.Context, enroll.Target) enroll.Outcome { return p.outcome }

// The scheduler only acts on a definite result, so every unknown that reaches it must stay unknown —
// but the class still has to survive: "the prober is down" and "the node renewed its certificate" are
// different operations problems, and the adapter is where that difference used to get lost.
func TestTheSchedulerAdapterKeepsTheUnknownClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		outcome enroll.Outcome
		want    probe.Class
		unknown bool
	}{
		{"a passed check", enroll.Outcome{}, "", false},
		{"a definite failure", enroll.Outcome{Class: "timeout"}, probe.ClassTimeout, false},
		{"a certificate the server has not pinned", enroll.Outcome{Unknown: true, Class: "cert_changed"}, probe.ClassCertChanged, true},
		{"a prober that could not run", enroll.Outcome{Unknown: true}, probe.ClassWorker, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			results, err := enrollProberAdapter{inner: classProber{outcome: tc.outcome}}.Probe(context.Background(), []probe.Target{
				{ID: "uv_one", ServerName: "one.example.com"},
			})
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d results", len(results))
			}
			result := results[0]
			if result.Class != tc.want {
				t.Errorf("class = %q, want %q", result.Class, tc.want)
			}
			if result.Unknown() != tc.unknown {
				t.Errorf("Unknown() = %v for class %q", result.Unknown(), result.Class)
			}
			if result.OK != (tc.want == "" && !tc.unknown) {
				t.Errorf("OK = %v for class %q", result.OK, tc.outcome.Class)
			}
		})
	}
}
