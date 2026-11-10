package install

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// certificateStep obtains the node's TLS certificate and keeps it current.
//
// The ACME challenge is published as a file in the webroot nginx already serves, not on a listener of
// our own: nginx owns :80 on this host, and a second process on that port would break direct traffic.
// Once the certificate exists the vhost changes shape (plain HTTP → TLS), so the step runs the web
// step afterwards rather than leaving the node half-configured until the next `termit-uv init`.
type certificateStep struct{ e *stepEnv }

func (*certificateStep) Name() string { return "certificate" }

func (s *certificateStep) paths() (certPath, keyPath string) {
	return LeafCertPath(s.e.cfg), filepath.Join(s.e.cfg.Paths.CertDir, keyFile)
}

// LeafCertPath is where the certificate nginx serves lives. Callers outside these steps — the agent
// reporting its pin on sync, for one — use it instead of spelling the filename again.
func LeafCertPath(cfg *config.Config) string {
	return filepath.Join(cfg.Paths.CertDir, certFile)
}

func (s *certificateStep) renewBefore() time.Duration {
	if days := s.e.cfg.ACME.RenewBeforeDays; days > 0 {
		return time.Duration(days) * 24 * time.Hour
	}
	return acme.DefaultRenewBefore
}

func (s *certificateStep) manager() (*acme.Manager, error) {
	manager, err := acme.New(acme.Config{
		Domain:        s.e.cfg.Domain,
		Email:         s.e.cfg.Email,
		DirectoryURL:  s.e.cfg.ACME.Directory,
		SkipTLSVerify: s.e.cfg.ACME.SkipTLSVerify,
		CertDir:       s.e.cfg.Paths.CertDir,
		ChallengeDir:  s.e.cfg.Paths.ACMEWebroot,
		RenewBefore:   s.renewBefore(),
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func (s *certificateStep) Check(context.Context) (CheckResult, error) {
	certPath, keyPath := s.paths()
	if !readable(certPath) || !readable(keyPath) {
		return CheckResult{Detail: "no certificate has been issued"}, nil
	}
	notAfter, err := acme.CertNotAfter(certPath)
	if err != nil {
		return CheckResult{Detail: "the certificate on disk is unreadable: " + err.Error()}, nil
	}
	if acme.NeedsRenewal(notAfter, s.e.now(), s.renewBefore()) {
		return CheckResult{Detail: fmt.Sprintf("the certificate expires %s and is due for renewal",
			notAfter.Format(time.RFC3339))}, nil
	}
	if recorded := s.e.st.Certificate; recorded == nil || !recorded.NotAfter.Equal(notAfter) {
		return CheckResult{Detail: "the state file does not record the certificate on disk"}, nil
	}
	return CheckResult{Satisfied: true, Detail: "valid until " + notAfter.Format(time.RFC3339)}, nil
}

func (s *certificateStep) Run(ctx context.Context) error {
	manager, err := s.manager()
	if err != nil {
		return err
	}
	// Ensure renews only when the certificate is inside the renewal window; a forced run replaces it
	// whatever the clock says, which is what a drill or a recovered CA needs.
	issue := manager.Ensure
	if s.e.forced {
		issue = manager.Issue
	}
	result, err := issue(ctx)
	if err != nil {
		return fmt.Errorf("obtain a certificate for %s: %w", s.e.cfg.Domain, err)
	}

	now := s.e.now()
	if recorded := s.e.st.Certificate; recorded == nil || result.Renewed {
		s.e.st.Certificate = &state.Certificate{IssuedAt: now, NotAfter: result.NotAfter}
	} else {
		recorded.NotAfter = result.NotAfter
	}
	s.e.st.AddPath(s.e.cfg.Paths.CertDir, state.KindDir, "certificate", now)
	s.e.st.AddPath(result.CertPath, state.KindFile, "certificate", now)
	s.e.st.AddPath(result.KeyPath, state.KindFile, "certificate", now)
	s.e.st.Touch()

	// A certificate changes the vhost's shape and has to be served. If the web step still has work to
	// do it rewrites and reloads; otherwise only the reload is missing, which is the renewal case.
	web := &webStep{s.e}
	switch check, err := web.Check(ctx); {
	case err != nil:
		return err
	case !check.Satisfied:
		if err := web.Run(ctx); err != nil {
			return err
		}
	case s.e.serviceActive(ctx, nginxUnit):
		if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"reload", nginxUnit}}); err != nil {
			return fmt.Errorf("reload nginx after the certificate: %w", err)
		}
	}

	s.e.log.Info("certificate is in place",
		"domain", s.e.cfg.Domain,
		"not_after", result.NotAfter.Format(time.RFC3339),
		"renewed", result.Renewed)
	return nil
}
