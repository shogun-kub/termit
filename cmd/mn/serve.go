package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
	"github.com/shogun-kub/termit/internal/mn/cert"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/dns"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/sched"
	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// shutdownGrace is how long in-flight requests get after a termination signal.
const shutdownGrace = 15 * time.Second

// runServe runs the HTTP surface: health, metrics and (from M2.2) the agent API.
//
// TLS comes from the certificate in the data directory. Until ACME has issued one the server runs on
// plain HTTP and says so on every start: an unencrypted management server is a development state, not
// a deployment one.
func runServe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "path to config.yaml")
	verbose := flags.Bool("v", false, "log at info level")
	plainHTTP := flags.Bool("http", false, "serve plain HTTP; development only, never on a public address")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mn serve: %v\n", err)
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, store.Options{Path: cfg.DBPath})
	if err != nil {
		fmt.Fprintf(stderr, "mn serve: %v\n", err)
		return exitFailure
	}
	defer func() { _ = db.Close() }()

	scheduler := newScheduler(schedulerConfig{
		CheckInterval: cfg.Policy.CheckInterval,
		Retries:       cfg.Policy.ProbeRetries,
		Logger:        logger,
	}, db, schedProber(cfg, logger), newCertChecker(cfg))
	scheduler.PolicyFn = policyFromSettings(cfg, db)

	handler, err := buildHandler(cfg, db, logger)
	if err != nil {
		fmt.Fprintf(stderr, "mn serve: %v\n", err)
		return exitFailure
	}
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// The admin socket is best-effort at startup: a server that cannot offer its CLI must still serve
	// the agents, and the operator sees exactly why the CLI is missing.
	if listener, err := listenAdmin(cfg.AdminSocket); err != nil {
		logger.Warn("admin socket unavailable", "path", cfg.AdminSocket, "err", err)
	} else {
		adminService, err := admin.New(db)
		if err == nil {
			adminService.Master = mustMasterKey(cfg, logger)
			adminService.Runner = scheduler
		}
		if err != nil {
			fmt.Fprintf(stderr, "mn serve: %v\n", err)
			return exitFailure
		}
		logger.Info("admin socket ready", "path", cfg.AdminSocket)
		go func() {
			if err := admin.Serve(ctx, listener, adminService, logger); err != nil && ctx.Err() == nil {
				logger.Error("admin socket stopped", "err", err)
			}
		}()
	}

	// Retention runs in the server because nothing else is running: probe results are telemetry and age
	// out at 90 days, audit records are evidence and age out at a year.
	go retention(ctx, db, logger)

	// The scheduler is what turns probe results into state changes. It is optional: without a prober
	// every node would simply be unknown, and the server would still serve.
	go func() {
		if err := scheduler.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("scheduler stopped", "err", err)
		}
	}()

	serveErr := make(chan error, 1)
	if *plainHTTP {
		fmt.Fprintln(stderr, "mn serve: --http is set; the API is not encrypted")
		go func() { serveErr <- server.ListenAndServe() }()
	} else {
		// Issuing blocks startup on purpose: a management API that silently falls back to HTTP is
		// worse than one that refuses to start.
		tlsPaths, err := cert.Ensure(ctx, cert.Options{Config: cfg, Logger: logger})
		if err != nil {
			fmt.Fprintf(stderr, "mn serve: %v\n", err)
			return exitFailure
		}
		logger.Info("serving TLS", "domain", cfg.Domain, "listen", cfg.Listen,
			"not_after", tlsPaths.NotAfter.UTC().Format(time.RFC3339))
		go func() { serveErr <- server.ListenAndServeTLS(tlsPaths.CertPath, tlsPaths.KeyPath) }()
	}

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "mn serve: %v\n", err)
			return exitFailure
		}
		return exitOK
	case <-ctx.Done():
	}

	logger.Info("shutting down", "grace", shutdownGrace.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(stderr, "mn serve: shutdown: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// buildHandler wires the HTTP surface: the enrollment service needs a store, resolvers and a prober.
func buildHandler(cfg *config.Config, db *store.Store, logger *slog.Logger) (http.Handler, error) {
	resolvers := []enroll.Resolver{dns.System{}}
	if cfg.DNS.Secondary != "" {
		resolvers = append(resolvers, dns.Server{Addr: cfg.DNS.Secondary})
	}
	prober, err := newProber(cfg, logger)
	if err != nil {
		return nil, err
	}
	service, err := enroll.New(enroll.Options{
		Store:     db,
		Mode:      cfg.Enrollment.Mode,
		Policy:    cfg.Policy,
		Resolvers: resolvers,
		Prober:    prober,
		Certs:     newCertChecker(cfg),
		Master:    mustMasterKey(cfg, logger),
	})
	if err != nil {
		return nil, err
	}
	masterKey, err := store.ReadMasterKey(cfg.MasterKeyFile)
	if err != nil {
		return nil, err
	}
	return server.New(server.Options{
		Store:         db,
		Enrollment:    service,
		Domain:        cfg.Domain,
		Logger:        logger,
		Attempts:      cfg.Enrollment.Attempts,
		Window:        time.Duration(cfg.Enrollment.WindowSeconds) * time.Second,
		MasterKey:     masterKey,
		CheckInterval: cfg.Policy.CheckInterval,
		SyncInterval:  cfg.Policy.SyncInterval,
		MinSyncDelay:  2 * time.Hour,
	})
}

// mustMasterKey reads the master key once at startup. Without it neither enrollment nor sync can work,
// so a failure here is a refusal to start rather than a degraded mode.
func mustMasterKey(cfg *config.Config, logger *slog.Logger) []byte {
	key, err := store.ReadMasterKey(cfg.MasterKeyFile)
	if err != nil {
		logger.Error("cannot read the master key", "path", cfg.MasterKeyFile, "err", err)
		return nil
	}
	return key
}

// retention prunes on a daily tick. It runs once shortly after start as well, so a server that is
// restarted often still keeps up.
func retention(ctx context.Context, db *store.Store, logger *slog.Logger) {
	const (
		checkRetention = 90 * 24 * time.Hour
		auditRetention = 365 * 24 * time.Hour
	)
	prune := func() {
		now := time.Now().UTC()
		checks, err := db.PruneChecks(ctx, now.Add(-checkRetention))
		if err != nil && ctx.Err() == nil {
			logger.Warn("retention: checks", "err", err)
		}
		audit, err := db.PruneAudit(ctx, now.Add(-auditRetention))
		if err != nil && ctx.Err() == nil {
			logger.Warn("retention: audit", "err", err)
		}
		if checks > 0 || audit > 0 {
			logger.Info("retention pruned", "checks", checks, "audit", audit)
		}
	}
	select {
	case <-time.After(time.Minute):
		prune()
	case <-ctx.Done():
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			prune()
		case <-ctx.Done():
			return
		}
	}
}

// policyFromSettings lets `mn policy set` take effect on the next pass without a restart. An empty
// setting means "use the configuration", which is what an unconfigured install gets anyway.
func policyFromSettings(cfg *config.Config, db *store.Store) func(ctx context.Context) sched.Policy {
	return func(ctx context.Context) sched.Policy {
		policy := sched.DefaultPolicy()
		if cfg.Policy.CheckInterval > 0 {
			policy.Interval = cfg.Policy.CheckInterval
		}
		if cfg.Policy.ProbeRetries > 0 {
			policy.Retries = sched.RetryPolicy{Attempts: cfg.Policy.ProbeRetries, Gap: 10}
		}
		if value, err := db.GetSetting(ctx, "policy.check_interval"); err == nil && value != "" {
			if interval, err := time.ParseDuration(value); err == nil && interval > 0 {
				policy.Interval = interval
			}
		}
		if value, err := db.GetSetting(ctx, "policy.probe_retries"); err == nil && value != "" {
			if retries, err := strconv.Atoi(value); err == nil && retries > 0 {
				policy.Retries = sched.RetryPolicy{Attempts: retries, Gap: 10}
			}
		}
		return policy
	}
}
