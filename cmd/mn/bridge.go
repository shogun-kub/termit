package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/mn/sched"
)

// errNoProber is what a result carries when the prober could not be built at all: the node is unknown,
// which is the honest answer, and nothing moves.
var errNoProber = errors.New("the prober is not available on this server")

// enrollProberAdapter presents the enrollment prober — one target in, one outcome out — as the
// scheduler's batch prober. The two exist for different callers: enrollment probes the node that is
// registering, the scheduler probes the mesh.
type enrollProberAdapter struct {
	inner enroll.Prober
}

func (a enrollProberAdapter) Probe(ctx context.Context, targets []probe.Target) ([]probe.Result, error) {
	results := make([]probe.Result, 0, len(targets))
	for _, target := range targets {
		outcome := a.inner.Probe(ctx, enroll.Target{
			Domain: target.ServerName,
			Path:   target.Path,
			UUID:   target.UUID,
			Pin:    target.PinnedCertSHA256,
		})
		result := probe.Result{ID: target.ID, OK: outcome.Class == "" && !outcome.Unknown, Err: outcome.Err}
		if outcome.Unknown {
			result.Class = probe.ClassWorker
		} else if outcome.Class != "" {
			result.Class = probe.Class(outcome.Class)
		}
		results = append(results, result)
	}
	return results, nil
}

// schedProber builds the scheduler's prober from the configuration. When no worker can be built the
// returned prober reports every node as unknown.
func schedProber(cfg *config.Config, logger *slog.Logger) sched.Prober {
	inner, err := newProber(cfg, logger)
	if err != nil {
		logger.Warn("the prober is unavailable; every node will be unknown", "err", err)
		return unknownSchedProber{}
	}
	return enrollProberAdapter{inner: inner}
}

// unknownSchedProber is the scheduler's prober when no worker exists: every node is unknown.
type unknownSchedProber struct{}

func (unknownSchedProber) Probe(_ context.Context, targets []probe.Target) ([]probe.Result, error) {
	results := make([]probe.Result, 0, len(targets))
	for _, target := range targets {
		results = append(results, probe.Result{ID: target.ID, Class: probe.ClassWorker, Err: errNoProber})
	}
	return results, nil
}
