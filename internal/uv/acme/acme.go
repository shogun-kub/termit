// Package acme obtains and renews the node's TLS certificate over ACME HTTP-01.
//
// It wraps golang.org/x/crypto/acme rather than a full ACME client library: the agent needs to own
// the challenge handler, the file layout and the renewal schedule anyway, and the wrapper keeps the
// dependency surface small. Certificates are written as PEM files that nginx and any other local
// service can read; nothing in this package talks to a proxy.
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/shogun-kub/termit/internal/uv/state"
)

const (
	// DefaultDirectory is the Let's Encrypt production directory.
	DefaultDirectory = "https://acme-v02.api.letsencrypt.org/directory"

	// DefaultRenewBefore renews a certificate this long before it expires. Let's Encrypt certificates
	// live 90 days, so this renews around day 60.
	DefaultRenewBefore = 30 * 24 * time.Hour

	accountKeyName = "account.key"
	certName       = "fullchain.pem"
	keyName        = "privkey.pem"

	challengePrefix = "/.well-known/acme-challenge/"
)

// Config describes one node's certificate.
type Config struct {
	// Domain is the single name the certificate is issued for.
	Domain string
	// Email is the ACME account contact. Optional but recommended: it is how a CA reaches the owner.
	Email string
	// DirectoryURL is the ACME directory; defaults to Let's Encrypt production.
	DirectoryURL string
	// SkipTLSVerify disables verification of the ACME server's own certificate. Lab only, and the
	// agent prints a warning while it is enabled.
	SkipTLSVerify bool
	// CertDir holds account.key, fullchain.pem and privkey.pem.
	CertDir string
	// ChallengeDir is the directory nginx serves at /.well-known/acme-challenge/. When it is set the
	// challenge is published as a file there instead of through ChallengeHandler: nginx owns :80, and
	// a second listener on that port is not an option.
	ChallengeDir string
	// RenewBefore overrides DefaultRenewBefore.
	RenewBefore time.Duration
	// KeyBits selects RSA (2048 or 4096) for the certificate key. Zero means ECDSA P-256.
	KeyBits int
}

// Result reports where the certificate is and when it expires.
type Result struct {
	CertPath string
	KeyPath  string
	NotAfter time.Time
	// Renewed is true when a new certificate was issued by this call.
	Renewed bool
}

// Manager issues and renews one domain's certificate.
type Manager struct {
	cfg        Config
	client     *acme.Client
	challenges *challenges
	now        func() time.Time
}

// New prepares a manager. It creates the certificate directory and the ACME account key if they do
// not exist; it performs no network I/O.
func New(cfg Config) (*Manager, error) {
	if cfg.Domain == "" {
		return nil, errors.New("acme: domain is required")
	}
	if strings.Contains(cfg.Domain, "/") {
		return nil, fmt.Errorf("acme: domain %q must be a host name, not a URL", cfg.Domain)
	}
	if cfg.CertDir == "" {
		return nil, errors.New("acme: cert dir is required")
	}
	if cfg.DirectoryURL == "" {
		cfg.DirectoryURL = DefaultDirectory
	}
	if cfg.RenewBefore <= 0 {
		cfg.RenewBefore = DefaultRenewBefore
	}
	if cfg.KeyBits != 2048 && cfg.KeyBits != 4096 {
		cfg.KeyBits = 0
	}

	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		return nil, fmt.Errorf("acme: create cert dir: %w", err)
	}
	accountKey, err := loadOrCreateAccountKey(filepath.Join(cfg.CertDir, accountKeyName))
	if err != nil {
		return nil, err
	}

	m := &Manager{
		cfg:        cfg,
		challenges: newChallenges(),
		now:        time.Now,
	}
	transport := &http.Transport{
		// #nosec G402 -- lab-only escape hatch, guarded by Config.SkipTLSVerify and reported by the agent.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.SkipTLSVerify},
	}
	m.client = &acme.Client{
		Key:          accountKey,
		DirectoryURL: cfg.DirectoryURL,
		HTTPClient:   &http.Client{Transport: transport},
	}
	return m, nil
}

// ChallengeHandler serves HTTP-01 challenges for the ACME server. Mount it on a listener that the
// node's domain reaches on the port the CA validates (80 in production), directly or via nginx.
//
// Production uses Config.ChallengeDir instead: nginx already owns :80, so the token is written into
// the directory nginx serves. This handler stays for tests and for a host where Termit owns the port.
func (m *Manager) ChallengeHandler() http.Handler { return m.challenges }

// challengePath is where the token file goes when challenges are published as files.
func (m *Manager) challengePath(token string) string {
	return filepath.Join(m.cfg.ChallengeDir, ".well-known", "acme-challenge", token)
}

// Ensure returns an existing valid certificate, or issues a new one when there is none or it is due
// for renewal.
func (m *Manager) Ensure(ctx context.Context) (Result, error) {
	if res, ok := m.existing(); ok && !NeedsRenewal(res.NotAfter, m.now(), m.cfg.RenewBefore) {
		return res, nil
	}
	res, err := m.Issue(ctx)
	if err != nil {
		return Result{}, err
	}
	res.Renewed = true
	return res, nil
}

// Issue always obtains a new certificate, regardless of what is on disk.
func (m *Manager) Issue(ctx context.Context) (Result, error) {
	if err := m.register(ctx); err != nil {
		return Result{}, err
	}

	certKey, err := generateKey(m.cfg.KeyBits)
	if err != nil {
		return Result{}, err
	}

	order, err := m.client.AuthorizeOrder(ctx, acme.DomainIDs(m.cfg.Domain))
	if err != nil {
		return Result{}, fmt.Errorf("acme: authorize order: %w", err)
	}
	if err := m.satisfyAuthorizations(ctx, order); err != nil {
		return Result{}, err
	}

	csr, err := createCSR(m.cfg.Domain, certKey)
	if err != nil {
		return Result{}, err
	}
	if order.FinalizeURL == "" {
		return Result{}, fmt.Errorf("acme: order %s is %q without a finalize URL", order.URI, order.Status)
	}
	// CreateCertFromOrder (not the older CreateOrderCert) passes both the finalize and the order URL,
	// which the client needs when the CA answers with "processing" and it has to poll.
	chain, _, err := m.client.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return Result{}, fmt.Errorf("acme: finalize order: %w", err)
	}

	res, err := m.writeCertificate(chain, certKey)
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// publishChallenge makes the key authorization reachable for the CA, either as a file in the webroot
// nginx serves or through the in-memory handler.
func (m *Manager) publishChallenge(token, keyAuth string) error {
	if m.cfg.ChallengeDir == "" {
		m.challenges.put(token, keyAuth)
		return nil
	}
	// The token comes from the CA, but a path built from a foreign string deserves a check.
	if token == "" || strings.ContainsAny(token, `/\`) || strings.HasPrefix(token, ".") {
		return fmt.Errorf("acme: refusing to publish the challenge token %q", token)
	}
	dir := filepath.Dir(m.challengePath(token))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("acme: create the challenge directory: %w", err)
	}
	if err := state.WriteFileAtomic(m.challengePath(token), []byte(keyAuth), 0o644); err != nil {
		return fmt.Errorf("acme: publish the challenge: %w", err)
	}
	return nil
}

// retractChallenge removes what publishChallenge created, so the webroot does not keep tokens.
func (m *Manager) retractChallenge(token string) {
	if m.cfg.ChallengeDir == "" {
		m.challenges.drop(token)
		return
	}
	_ = os.Remove(m.challengePath(token))
	// Remove the now-empty .well-known tree: a webroot that keeps empty directories is a fingerprint
	// and blocks the removal of the webroot itself.
	for _, dir := range []string{
		filepath.Dir(m.challengePath(token)),
		filepath.Dir(filepath.Dir(m.challengePath(token))),
	} {
		_ = os.Remove(dir) // only succeeds when empty, which is the point
	}
}

// NeedsRenewal reports whether a certificate expiring at notAfter should be replaced at now.
func NeedsRenewal(notAfter, now time.Time, before time.Duration) bool {
	return !now.Before(notAfter.Add(-before))
}

// CertNotAfter reads the expiry of the first certificate in a PEM file.
func CertNotAfter(path string) (time.Time, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is built from Config.CertDir.
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return time.Time{}, fmt.Errorf("acme: %s contains no PEM block", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("acme: parse %s: %w", path, err)
	}
	return cert.NotAfter, nil
}

func (m *Manager) register(ctx context.Context) error {
	account := &acme.Account{Contact: []string{}}
	if m.cfg.Email != "" {
		account.Contact = []string{"mailto:" + m.cfg.Email}
	}
	_, err := m.client.Register(ctx, account, acme.AcceptTOS)
	if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fmt.Errorf("acme: register account: %w", err)
	}
	return nil
}

func (m *Manager) satisfyAuthorizations(ctx context.Context, order *acme.Order) error {
	for _, authzURL := range order.AuthzURLs {
		authz, err := m.client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return fmt.Errorf("acme: fetch authorization: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		ch := pickChallenge(authz.Challenges, "http-01")
		if ch == nil {
			return fmt.Errorf("acme: no http-01 challenge offered for %s", m.cfg.Domain)
		}
		keyAuth, err := m.client.HTTP01ChallengeResponse(ch.Token)
		if err != nil {
			return fmt.Errorf("acme: build challenge response: %w", err)
		}
		if err := m.publishChallenge(ch.Token, keyAuth); err != nil {
			return err
		}
		_, acceptErr := m.client.Accept(ctx, ch)
		waitErr := m.waitAuthorization(ctx, authz.URI)
		m.retractChallenge(ch.Token)
		if waitErr != nil {
			if acceptErr != nil {
				return fmt.Errorf("acme: accept challenge: %w", acceptErr)
			}
			return waitErr
		}
	}
	return nil
}

func (m *Manager) waitAuthorization(ctx context.Context, url string) error {
	// WaitAuthorization polls until the authorization is valid or invalid, honouring ctx.
	_, err := m.client.WaitAuthorization(ctx, url)
	if err != nil {
		return fmt.Errorf("acme: wait authorization: %w", err)
	}
	return nil
}

func (m *Manager) writeCertificate(chain [][]byte, key crypto.Signer) (Result, error) {
	if len(chain) == 0 {
		return Result{}, errors.New("acme: CA returned no certificates")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return Result{}, fmt.Errorf("acme: parse issued certificate: %w", err)
	}

	var certPEM []byte
	for _, der := range chain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Result{}, fmt.Errorf("acme: marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	certPath := filepath.Join(m.cfg.CertDir, certName)
	keyPath := filepath.Join(m.cfg.CertDir, keyName)
	// Write the key first: a reader that finds a new certificate must also find its key.
	if err := state.WriteFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return Result{}, err
	}
	if err := state.WriteFileAtomic(certPath, certPEM, 0o644); err != nil {
		return Result{}, err
	}
	return Result{CertPath: certPath, KeyPath: keyPath, NotAfter: leaf.NotAfter}, nil
}

func (m *Manager) existing() (Result, bool) {
	certPath := filepath.Join(m.cfg.CertDir, certName)
	keyPath := filepath.Join(m.cfg.CertDir, keyName)
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); err != nil {
			return Result{}, false
		}
	}
	notAfter, err := CertNotAfter(certPath)
	if err != nil {
		return Result{}, false
	}
	return Result{CertPath: certPath, KeyPath: keyPath, NotAfter: notAfter}, true
}

func pickChallenge(list []*acme.Challenge, typ string) *acme.Challenge {
	for _, ch := range list {
		if ch.Type == typ {
			return ch
		}
	}
	return nil
}

func createCSR(domain string, key crypto.Signer) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: domain},
		DNSNames: []string{domain},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("acme: create CSR: %w", err)
	}
	return der, nil
}

func generateKey(bits int) (crypto.Signer, error) {
	if bits == 2048 || bits == 4096 {
		key, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return nil, fmt.Errorf("acme: generate RSA key: %w", err)
		}
		return key, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: generate ECDSA key: %w", err)
	}
	return key, nil
}

func loadOrCreateAccountKey(path string) (crypto.Signer, error) {
	if data, err := os.ReadFile(path); err == nil { // #nosec G304 -- path is built from Config.CertDir.
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("acme: %s contains no PEM block", path)
		}
		if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
			return key, nil
		}
		if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if signer, ok := key.(crypto.Signer); ok {
				return signer, nil
			}
		}
		return nil, fmt.Errorf("acme: %s is not a usable private key", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("acme: read account key: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: generate account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("acme: marshal account key: %w", err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := state.WriteFileAtomic(path, block, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// challenges maps an ACME token to the key authorization served for it.
type challenges struct {
	mu   sync.RWMutex
	data map[string]string
}

func newChallenges() *challenges { return &challenges{data: map[string]string{}} }

func (c *challenges) put(token, keyAuth string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[token] = keyAuth
}

func (c *challenges) drop(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, token)
}

func (c *challenges) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(r.URL.Path, challengePrefix) {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, challengePrefix)
	c.mu.RLock()
	keyAuth, ok := c.data[token]
	c.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(keyAuth))
}
