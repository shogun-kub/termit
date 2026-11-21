package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
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

// TestTheTargetFollowsWhatTheNodeServes: the check goes through whatever the node reported. A node that
// serves only QUIC has no VLESS inbound, so the worker binary would be aimed at nothing.
func TestTheTargetFollowsWhatTheNodeServes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "mn.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	uv := store.UV{
		ID: "uv_one", Domain: "one.example.com", ProbeUUID: "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d",
		State: store.StateActive, CreatedAt: time.Unix(0, 0).UTC(),
	}
	if err := db.CreateUV(ctx, uv); err != nil {
		t.Fatalf("create node: %v", err)
	}
	targeter := profileTargeter{store: db}

	if err := db.SetUVTransports(ctx, uv.ID, []string{proto.KindHysteria2}); err != nil {
		t.Fatalf("record transports: %v", err)
	}
	target, ok := targeter.Target(ctx, uv)
	if !ok {
		t.Fatal("no target for a node with a probe credential")
	}
	if target.Transport != proto.KindHysteria2 {
		t.Fatalf("transport = %q, want the QUIC one", target.Transport)
	}

	if err := db.SetUVTransports(ctx, uv.ID, []string{proto.KindVLESS, proto.KindHysteria2}); err != nil {
		t.Fatalf("record transports: %v", err)
	}
	target, _ = targeter.Target(ctx, uv)
	if target.Transport != proto.KindVLESS {
		t.Fatalf("transport = %q, want the TLS profile when the node serves it", target.Transport)
	}
}

// batchInner records the targets it was handed through the batch door and remembers whether the
// single-target door was used instead.
type batchInner struct {
	got     []probe.Target
	through string
}

func (b *batchInner) Probe(context.Context, enroll.Target) enroll.Outcome {
	b.through = "single"
	return enroll.Outcome{}
}

func (b *batchInner) ProbeTargets(_ context.Context, targets []probe.Target) ([]probe.Result, error) {
	b.through = "batch"
	b.got = targets
	results := make([]probe.Result, len(targets))
	for i, target := range targets {
		results[i] = probe.Result{ID: target.ID, OK: true}
	}
	return results, nil
}

// TestTheAdapterKeepsTheTransport: the enrollment interface has no field for the transport, so a prober
// that speaks the scheduler's own targets has to be used through that door. Going through the single
// door would check a QUIC-only node the TLS way — and it would pass, which is the worst outcome of all.
func TestTheAdapterKeepsTheTransport(t *testing.T) {
	t.Parallel()
	inner := &batchInner{}
	results, err := enrollProberAdapter{inner: inner}.Probe(context.Background(), []probe.Target{
		{ID: "uv_one", Transport: proto.KindHysteria2},
	})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if inner.through != "batch" {
		t.Fatalf("the adapter went through the %q door", inner.through)
	}
	if len(inner.got) != 1 || inner.got[0].Transport != proto.KindHysteria2 {
		t.Fatalf("the transport was lost on the way: %+v", inner.got)
	}
	if !results[0].OK {
		t.Error("the batch result did not come back")
	}
}
