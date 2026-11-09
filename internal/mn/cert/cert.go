// Package cert gives the management server its own TLS certificate.
//
// The server terminates TLS itself — unlike a node, which puts nginx in front — so it needs the ACME
// HTTP-01 challenge answered on port 80 while an order is in flight. The listener exists only for the
// duration of the order: a server that keeps :80 open for a challenge endpoint nobody reads is a
// server with one more thing to get wrong.
package cert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/mn/config"
)

// Paths are the files nginx-less TLS needs.
type Paths struct {
	CertPath string
	KeyPath  string
	NotAfter time.Time
	// Renewed is true when this call issued a new certificate.
	Renewed bool
}

// Options configure Ensure.
type Options struct {
	Config *config.Config
	Logger *slog.Logger
	Now    func() time.Time
}

// Ensure returns a valid certificate, issuing one when there is none or it is due for renewal.
//
// A failure to issue is returned, not logged away: the server cannot start encrypted without it, and
// running the management API over plain HTTP is a state the operator has to choose explicitly.
func Ensure(ctx context.Context, opts Options) (Paths, error) {
	if opts.Config == nil {
		return Paths{}, errors.New("cert: a configuration is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cfg := opts.Config
	paths := Paths{
		CertPath: filepath.Join(cfg.CertDir(), "fullchain.pem"),
		KeyPath:  filepath.Join(cfg.CertDir(), "privkey.pem"),
	}

	renewBefore := time.Duration(cfg.ACME.RenewBeforeDays) * 24 * time.Hour
	if renewBefore <= 0 {
		renewBefore = acme.DefaultRenewBefore
	}

	// A certificate on disk that is still fresh needs no network at all.
	if notAfter, err := acme.CertNotAfter(paths.CertPath); err == nil {
		paths.NotAfter = notAfter
		if !acme.NeedsRenewal(notAfter, opts.Now(), renewBefore) {
			if fileExists(paths.KeyPath) {
				return paths, nil
			}
		}
	}

	manager, err := acme.New(acme.Config{
		Domain:        cfg.Domain,
		Email:         cfg.ACME.Email,
		DirectoryURL:  cfg.ACME.Directory,
		SkipTLSVerify: cfg.ACME.SkipTLSVerify,
		CertDir:       cfg.CertDir(),
		RenewBefore:   renewBefore,
	})
	if err != nil {
		return Paths{}, err
	}

	stop, err := serveChallenges(ctx, cfg.ACME.ChallengeListen, manager.ChallengeHandler())
	if err != nil {
		return Paths{}, err
	}
	defer stop()

	result, err := manager.Issue(ctx)
	if err != nil {
		return Paths{}, fmt.Errorf("cert: issue for %s: %w", cfg.Domain, err)
	}
	paths.CertPath, paths.KeyPath = result.CertPath, result.KeyPath
	paths.NotAfter, paths.Renewed = result.NotAfter, true
	if opts.Logger != nil {
		opts.Logger.Info("issued a certificate",
			"domain", cfg.Domain, "not_after", result.NotAfter.UTC().Format(time.RFC3339))
	}
	return paths, nil
}

// serveChallenges listens for ACME HTTP-01 requests and returns a function that stops listening.
func serveChallenges(ctx context.Context, listen string, handler http.Handler) (func(), error) {
	if listen == "" {
		listen = ":80"
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("cert: listen on %s for the ACME challenge: %w", listen, err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The challenge listener is temporary; a failure here surfaces as a failed order.
			_ = err
		}
	}()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-done
		// The listener must not survive the order, even when Shutdown raced with a connection.
		_ = listener.Close()
	}
	// A cancelled run must not leave the port bound.
	go func() {
		<-ctx.Done()
		stop()
	}()
	return stop, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
