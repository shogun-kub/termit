package main

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/mn/sched"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// profileTargeter turns a node's stored profile into a probe target. The profile is what the node
// registered with, so the probe reaches the same path the node's own clients use.
type profileTargeter struct{}

func (profileTargeter) Target(node store.UV) (probe.Target, bool) {
	if node.Domain == "" || node.ProbeUUID == "" {
		// A node without a probe credential cannot be checked the way a client would be: unknown.
		return probe.Target{}, false
	}
	path := "/"
	var stored proto.Profile
	if node.ProfileJSON != "" && node.ProfileJSON != "{}" && json.Unmarshal([]byte(node.ProfileJSON), &stored) == nil && stored.Path != "" {
		path = stored.Path
	}
	return probe.Target{
		ID:         node.ID,
		Address:    node.Domain,
		Port:       443,
		ServerName: node.Domain,
		Path:       path,
		Mode:       "packet-up",
		UUID:       node.ProbeUUID,
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
func newScheduler(cfg schedulerConfig, db *store.Store, prober sched.Prober) *sched.Runner {
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
		Targeter: profileTargeter{},
		Policy:   policy,
		Logger:   cfg.Logger,
	}
}
