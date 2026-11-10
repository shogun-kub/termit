package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/web"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

const (
	certFile   = "fullchain.pem"
	keyFile    = "privkey.pem"
	stockSite  = "default"
	disabledBy = ".termit-disabled"
)

// webStep writes the nginx vhost that fronts the node.
//
// The vhost changes shape with the node: before a certificate exists it serves the decoy over plain
// HTTP so that ACME has somewhere to land, and afterwards it redirects :80 and terminates TLS on
// :443. Both shapes come from the same generator, so `termit-uv init` converges whichever state the host is
// in.
type webStep struct{ e *stepEnv }

func (*webStep) Name() string { return "web" }

func (s *webStep) spec() (web.SiteSpec, error) {
	spec := web.SiteSpec{
		Domain:      s.e.cfg.Domain,
		SiteDir:     s.e.cfg.Paths.SiteDir,
		ACMEWebroot: s.e.cfg.Paths.ACMEWebroot,
		Fragment:    s.e.cfg.Paths.DecoySnippet,
		AccessLog:   s.e.cfg.Paths.AccessLog,
		ErrorLog:    s.e.cfg.Paths.ErrorLog,
	}
	if s.e.st.Identity.XHTTPPath == "" {
		return spec, errors.New("the node identity has not been generated")
	}
	spec.SecretPath = "/" + s.e.st.Identity.XHTTPPath + "/"
	spec.XrayAddr = xrayAddress()

	// TLS is decided by what is on disk, not by a state flag: a half-written certificate must not
	// produce a vhost nginx refuses to load.
	if readable(filepath.Join(s.e.cfg.Paths.CertDir, certFile)) && readable(filepath.Join(s.e.cfg.Paths.CertDir, keyFile)) {
		spec.CertPath = filepath.Join(s.e.cfg.Paths.CertDir, certFile)
		spec.KeyPath = filepath.Join(s.e.cfg.Paths.CertDir, keyFile)
	}
	return spec, nil
}

func readable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (s *webStep) render() ([]byte, web.SiteSpec, error) {
	spec, err := s.spec()
	if err != nil {
		return nil, spec, err
	}
	data, err := web.GenerateSite(spec)
	return data, spec, err
}

func (s *webStep) Check(context.Context) (CheckResult, error) {
	want, spec, err := s.render()
	if err != nil {
		return CheckResult{Detail: err.Error()}, nil
	}
	got, err := os.ReadFile(s.e.cfg.Paths.NginxSite) // #nosec G304 -- a path from our own config.
	switch {
	case errors.Is(err, os.ErrNotExist):
		return CheckResult{Detail: s.e.cfg.Paths.NginxSite + " does not exist"}, nil
	case err != nil:
		return CheckResult{}, fmt.Errorf("read %s: %w", s.e.cfg.Paths.NginxSite, err)
	}
	if !bytes.Equal(got, want) {
		return CheckResult{Detail: "the vhost on disk does not match the generated one"}, nil
	}
	if _, err := os.Lstat(s.e.cfg.Paths.SitesEnabled); err != nil {
		return CheckResult{Detail: "the vhost is not enabled at " + s.e.cfg.Paths.SitesEnabled}, nil
	}
	mode := "http"
	if spec.TLS() {
		mode = "tls"
	}
	return CheckResult{Satisfied: true, Detail: fmt.Sprintf("%s vhost for %s", mode, spec.Domain)}, nil
}

func (s *webStep) Run(ctx context.Context) error {
	data, spec, err := s.render()
	if err != nil {
		return err
	}
	target := s.e.cfg.Paths.NginxSite
	previous, previousErr := os.ReadFile(target) // #nosec G304 -- a path from our own config.

	if err := state.WriteFileAtomic(target, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := s.enable(ctx, data); err != nil {
		return err
	}
	if err := s.disableStockSite(); err != nil {
		return err
	}

	// nginx -t is the gate: a vhost it refuses would take the whole web tier down at the next
	// reload, and that includes the certificate and ACME paths of an unrelated site.
	if _, err := s.e.exec.Run(ctx, Cmd{Name: "nginx", Args: []string{"-t", "-q"}}); err != nil {
		s.rollback(target, previous, previousErr == nil)
		return fmt.Errorf("nginx rejected the generated vhost: %w", err)
	}

	now := s.e.now()
	s.e.st.AddPath(target, state.KindFile, "web", now)
	s.e.st.AddPath(s.e.cfg.Paths.SitesEnabled, state.KindSymlink, "web", now)
	s.e.st.AddPath(filepath.Dir(s.e.cfg.Paths.DecoySnippet), state.KindDir, "web", now)
	s.e.st.Touch()

	if s.e.serviceActive(ctx, nginxUnit) {
		if _, err := s.e.exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"reload", "nginx.service"}}); err != nil {
			return fmt.Errorf("reload nginx: %w", err)
		}
	}
	mode := "http"
	if spec.TLS() {
		mode = "tls"
	}
	s.e.log.Info("wrote the nginx vhost", "path", target, "mode", mode, "domain", spec.Domain)
	return nil
}

// enable puts the vhost into sites-enabled. The link is replaced rather than assumed: a stale link to
// a file we just rewrote is fine, a link to something else is not.
func (s *webStep) enable(_ context.Context, content []byte) error {
	link := s.e.cfg.Paths.SitesEnabled
	if current, err := os.Readlink(link); err == nil && current == s.e.cfg.Paths.NginxSite {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(link), err)
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace %s: %w", link, err)
	}
	if err := linkSite(s.e.cfg.Paths.NginxSite, link, content); err != nil {
		return fmt.Errorf("enable %s: %w", link, err)
	}
	return nil
}

// disableStockSite moves Debian's welcome page out of the way. Without it a request to the bare IP
// would answer "Welcome to nginx!" — a fingerprint louder than the decoy is quiet. The file is
// renamed, not deleted, and `termit-uv uninstall` puts it back.
func (s *webStep) disableStockSite() error {
	link := filepath.Join(filepath.Dir(s.e.cfg.Paths.SitesEnabled), stockSite)
	// Anything inside sites-enabled is loaded: Debian's include is a glob, so renaming in place would
	// keep nginx reading the file. It is parked next to the available vhosts, where nothing loads it.
	disabled := filepath.Join(filepath.Dir(s.e.cfg.Paths.NginxSite), stockSite+disabledBy)
	if _, err := os.Lstat(link); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", link, err)
	}
	if err := os.Rename(link, disabled); err != nil {
		return fmt.Errorf("disable the stock nginx site: %w", err)
	}
	s.e.log.Info("disabled the stock nginx site", "from", link, "to", disabled)
	return nil
}

// rollback restores the previous vhost after nginx refused the new one.
func (s *webStep) rollback(target string, previous []byte, hadPrevious bool) {
	if hadPrevious {
		_ = state.WriteFileAtomic(target, previous, 0o644)
		return
	}
	_ = os.Remove(target)
	_ = os.Remove(s.e.cfg.Paths.SitesEnabled)
}

// xrayAddress is the loopback address nginx proxies the secret path to.
func xrayAddress() string {
	spec := xray.DefaultSpec()
	return spec.ProxyAddress()
}
