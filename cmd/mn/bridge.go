package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/mn/sched"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// errNoProber is what a result carries when the prober could not be built at all: the node is unknown,
// which is the honest answer, and nothing moves.
var errNoProber = errors.New("the prober is not available on this server")

// profileTargeter turns a node's stored profile into a probe target.
//
// The pin matters more than it looks: xray verifies the node's certificate against its own embedded
// roots and ignores the system store (SPIKES S15), so a node behind a private CA — which is every node
// on the lab — fails the handshake unless the probe pins the leaf it saw at enrollment. A node that
// predates the pin column gets one on its first scheduled check, from the same certificate check
// enrollment uses.
type profileTargeter struct {
	store  *store.Store
	certs  enroll.CertChecker
	logger *slog.Logger
}

func (t profileTargeter) Target(ctx context.Context, node store.UV) (probe.Target, bool) {
	if node.Domain == "" || node.ProbeUUID == "" {
		// A node without a probe credential cannot be checked the way a client would be: unknown.
		return probe.Target{}, false
	}
	path := "/"
	var stored proto.Profile
	if node.ProfileJSON != "" && node.ProfileJSON != "{}" &&
		json.Unmarshal([]byte(node.ProfileJSON), &stored) == nil && stored.Path != "" {
		path = stored.Path
	}
	pin := node.CertSHA256
	if pin == "" && t.certs != nil {
		checked, err := t.certs.Check(ctx, node.Domain)
		if err != nil {
			// Without a pin the probe would fail for a reason that is ours, not the node's: leave it
			// unknown rather than blame the node.
			if t.logger != nil {
				t.logger.Warn("cannot pin the node certificate", "node", node.Domain, "err", err)
			}
			return probe.Target{}, false
		}
		pin = checked.Fingerprint
		if t.store != nil {
			if err := t.store.SetUVCertPin(ctx, node.ID, pin); err != nil && t.logger != nil {
				t.logger.Warn("cannot store the node certificate pin", "node", node.Domain, "err", err)
			}
		}
	}
	return probe.Target{
		ID:               node.ID,
		Address:          node.Domain,
		Port:             443,
		ServerName:       node.Domain,
		Path:             path,
		Mode:             "packet-up",
		UUID:             node.ProbeUUID,
		PinnedCertSHA256: pin,
	}, true
}

// schedulerConfig is the part of the configuration the runner needs, kept small so a test does not have
// to build a whole config.
type schedulerConfig struct {
	CheckInterval time.Duration
	Retries       int
	Logger        *slog.Logger
}

// newScheduler builds the runner. A nil prober is allowed: the runner then reports every node as
// unknown, which is exactly right, and the server keeps serving.
func newScheduler(cfg schedulerConfig, db *store.Store, prober sched.Prober, certs enroll.CertChecker) *sched.Runner {
	policy := sched.DefaultPolicy()
	if cfg.CheckInterval > 0 {
		policy.Interval = cfg.CheckInterval
	}
	if cfg.Retries > 0 {
		// The retry count is configurable; the ten-minute gap is not, because a shorter one turns a
		// flapping mesh into a stampede.
		policy.Retries = sched.RetryPolicy{Attempts: cfg.Retries, Gap: 10}
	}
	return &sched.Runner{
		Store:    db,
		Prober:   prober,
		Targeter: profileTargeter{store: db, certs: certs, logger: cfg.Logger},
		Policy:   policy,
		Logger:   cfg.Logger,
	}
}

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
