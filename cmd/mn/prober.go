package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/probe"
)

// newProber returns the prober the enrollment service uses.
//
// With no xray binary configured — a fresh install, or a machine that is not meant to probe — every
// check reports "unknown". That is the safe default: an unknown check never blocks a node, and it is
// visible in the log instead of silently looking like a healthy node.
func newProber(cfg *config.Config, logger *slog.Logger) (enroll.Prober, error) {
	if cfg.Prober.XrayBinary == "" {
		logger.Warn("no prober configured: enrollment checks will report unknown and nodes stay pending")
		return unknownProber{}, nil
	}
	worker, err := probe.NewWorker(probe.Config{
		Binary:   cfg.Prober.XrayBinary,
		WorkDir:  filepath.Join(cfg.DataDir, "probe"),
		ProbeURL: cfg.Prober.URL,
		CAFile:   cfg.Prober.CAFile,
	})
	if err != nil {
		// A broken worker must not take the server down: agents keep enrolling, every check reports
		// unknown, and the operator sees exactly why in the log.
		logger.Error("prober unavailable; checks will report unknown", "err", err)
		return unknownProber{}, nil
	}
	return workerProber{worker: worker}, nil
}

// workerProber adapts the xray-core worker to the narrow interface enrollment needs.
type workerProber struct {
	worker *probe.Worker
}

func (p workerProber) Probe(ctx context.Context, target enroll.Target) enroll.Outcome {
	results, err := p.worker.Probe(ctx, []probe.Target{{
		ID:         target.Domain,
		Address:    target.Domain,
		Port:       443,
		ServerName: target.Domain,
		Path:       target.Path,
		Mode:       "packet-up",
		UUID:       target.UUID,
	}})
	if err != nil {
		return enroll.Outcome{Unknown: true, Err: err}
	}
	if len(results) != 1 {
		return enroll.Outcome{Unknown: true}
	}
	result := results[0]
	if result.Unknown() {
		return enroll.Outcome{Unknown: true, Class: string(result.Class)}
	}
	if result.OK {
		return enroll.Outcome{}
	}
	return enroll.Outcome{Class: string(result.Class), Err: result.Err}
}

// unknownProber is what a server without an xray binary has: an honest "I cannot tell".
type unknownProber struct{}

func (unknownProber) Probe(ctx context.Context, target enroll.Target) enroll.Outcome {
	return enroll.Outcome{Unknown: true}
}
