package main

import (
	"context"
	"crypto/x509"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/mn/certcheck"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/probe"
)

// proberState says whether checks can actually run, and where the worker comes from when they can.
//
// It is separate from the prober value on purpose: a server without a worker still has a prober, one
// that answers "unknown" for every node. That keeps the mesh safe, and this struct is what tells the
// operator — and /metrics, and `mn status` — why nothing is being checked.
type proberState struct {
	Up     bool
	Detail string
}

// newProber returns the prober the enrollment service uses.
//
// With no xray binary configured — a fresh install, or a machine that is not meant to probe — every
// check reports "unknown". That is the safe default: an unknown check never blocks a node, and it is
// visible in the log instead of silently looking like a healthy node.
func newProber(cfg *config.Config, logger *slog.Logger) (enroll.Prober, proberState, error) {
	if cfg.Prober.XrayBinary == "" {
		logger.Warn("no prober configured: enrollment checks will report unknown and nodes stay pending")
		return unknownProber{}, proberState{Detail: "prober.xray_binary is not set"}, nil
	}
	worker, err := probe.NewWorker(probe.Config{
		Binary:   cfg.Prober.XrayBinary,
		WorkDir:  filepath.Join(cfg.DataDir, "probe"),
		ProbeURL: cfg.Prober.URL,
		CAFile:   cfg.Prober.CAFile,
		Roots:    trustStore(cfg),
	})
	if err != nil {
		// A broken worker must not take the server down: agents keep enrolling, every check reports
		// unknown, and the operator sees exactly why in the log.
		logger.Error("prober unavailable; checks will report unknown", "err", err)
		return unknownProber{}, proberState{Detail: "the prober could not start: " + err.Error()}, nil
	}
	return workerProber{worker: worker}, proberState{Up: true, Detail: cfg.Prober.XrayBinary}, nil
}

// workerProber adapts the xray-core worker to the narrow interface enrollment needs.
type workerProber struct {
	worker *probe.Worker
}

func (p workerProber) Probe(ctx context.Context, target enroll.Target) enroll.Outcome {
	results, err := p.worker.Probe(ctx, []probe.Target{{
		ID:               target.Domain,
		Address:          target.Domain,
		Port:             443,
		ServerName:       target.Domain,
		Path:             target.Path,
		Mode:             "packet-up",
		UUID:             target.UUID,
		PinnedCertSHA256: target.Pin,
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

// certChecker adapts certcheck to the narrow interface enrollment needs.
type certChecker struct {
	roots *x509.CertPool
}

// trustStore parses prober.ca_file once. The same pool is used by the enrollment checker and by the
// prober's certificate precheck; xray itself gets the file through SSL_CERT_FILE. A private CA is a
// configuration value, not something to install into the host's trust store.
func trustStore(cfg *config.Config) *x509.CertPool {
	if cfg.Prober.CAFile == "" {
		return nil
	}
	pem, err := os.ReadFile(cfg.Prober.CAFile) // #nosec G304 -- the operator configured this path.
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	return pool
}

// newCertChecker builds the checker, trusting prober.ca_file when it is set.
func newCertChecker(cfg *config.Config) enroll.CertChecker {
	return certChecker{roots: trustStore(cfg)}
}

func (c certChecker) Check(ctx context.Context, domain string) (enroll.Pin, error) {
	result, err := certcheck.Check(ctx, domain, certcheck.Options{Roots: c.roots})
	return enroll.Pin{Fingerprint: result.Fingerprint, NotAfter: result.NotAfter}, err
}
