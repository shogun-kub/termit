package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/version"
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

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           routes(cfg, db, logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	certPath := filepath.Join(cfg.CertDir(), "fullchain.pem")
	keyPath := filepath.Join(cfg.CertDir(), "privkey.pem")
	serveErr := make(chan error, 1)
	if fileExists(certPath) && fileExists(keyPath) {
		logger.Info("serving TLS", "domain", cfg.Domain, "listen", cfg.Listen)
		go func() { serveErr <- server.ListenAndServeTLS(certPath, keyPath) }()
	} else {
		fmt.Fprintf(stderr, "mn serve: no certificate at %s; serving plain HTTP — finish ACME or install one\n", certPath)
		go func() { serveErr <- server.ListenAndServe() }()
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

// routes builds the handler. Health and metrics are the whole surface until enrollment lands.
func routes(cfg *config.Config, db *store.Store, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		body := map[string]any{
			"status":         "ok",
			"api_version":    version.APIVersion,
			"build":          version.Version,
			"schema_version": store.SchemaVersion,
			"domain":         cfg.Domain,
		}
		if err := db.Ping(ctx); err != nil {
			body["status"] = "degraded"
			body["database"] = err.Error()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})

	// Metrics are loopback-only: they describe the server's internals, and a monitoring agent on the
	// same host is the only intended reader.
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r.RemoteAddr) {
			http.Error(w, "metrics are available on loopback only", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP termit_mn_up Whether the server answers.\n# TYPE termit_mn_up gauge\ntermit_mn_up 1\n")
		fmt.Fprintf(w, "# HELP termit_mn_build_info Build identity.\n# TYPE termit_mn_build_info gauge\n")
		fmt.Fprintf(w, "termit_mn_build_info{version=%q} 1\n", version.Version)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("unknown request", "method", r.Method, "path", r.URL.Path)
		http.NotFound(w, r)
	})
	return mux
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// sha256Of is the short identity of the master key: never the key itself.
func sha256Of(key []byte) []byte {
	sum := sha256.Sum256(key)
	return sum[:]
}
