package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNeedsRenewal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		notAfter time.Time
		before   time.Duration
		want     bool
	}{
		{"far in the future", now.Add(80 * 24 * time.Hour), 30 * 24 * time.Hour, false},
		{"exactly at the renewal window", now.Add(30 * 24 * time.Hour), 30 * 24 * time.Hour, true},
		{"inside the window", now.Add(10 * 24 * time.Hour), 30 * 24 * time.Hour, true},
		{"already expired", now.Add(-time.Hour), 30 * 24 * time.Hour, true},
		{"zero window", now.Add(time.Minute), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NeedsRenewal(tc.notAfter, now, tc.before); got != tc.want {
				t.Fatalf("NeedsRenewal(%v) = %v, want %v", tc.notAfter, got, tc.want)
			}
		})
	}
}

func TestChallengeHandler(t *testing.T) {
	t.Parallel()
	c := newChallenges()
	c.put("token-1", "token-1.thumbprint")

	t.Run("serves a presented token", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, challengePrefix+"token-1", nil)
		c.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Body.String(); got != "token-1.thumbprint" {
			t.Fatalf("body = %q", got)
		}
	})

	t.Run("unknown token is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, challengePrefix+"nope", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("other path is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("dropped token is 404", func(t *testing.T) {
		c.put("token-2", "x")
		c.drop("token-2")
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, challengePrefix+"token-2", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("POST is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, challengePrefix+"token-1", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})
}

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{CertDir: t.TempDir()}); err == nil {
		t.Fatal("expected an error for a missing domain")
	}
	if _, err := New(Config{Domain: "https://example.com/", CertDir: t.TempDir()}); err == nil {
		t.Fatal("expected an error for a URL instead of a host name")
	}
	if _, err := New(Config{Domain: "example.com"}); err == nil {
		t.Fatal("expected an error for a missing cert dir")
	}
}

func TestNewCreatesAccountKeyOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := Config{Domain: "example.com", CertDir: dir}

	m1, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := m1.client.Key.(*ecdsa.PrivateKey); !ok {
		t.Fatalf("account key type = %T, want ECDSA", m1.client.Key)
	}
	first, err := os.ReadFile(filepath.Join(dir, accountKeyName))
	if err != nil {
		t.Fatalf("read account key: %v", err)
	}

	// A second manager must reuse the key rather than replacing the account.
	if _, err := New(cfg); err != nil {
		t.Fatalf("second New: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, accountKeyName))
	if err != nil {
		t.Fatalf("read account key: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("account key was regenerated (credentials would be lost)")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, accountKeyName))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("account key permissions = %o, want 600", perm)
		}
	}
}

func TestWriteCertificatePersistsFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := &Manager{cfg: Config{Domain: "example.com", CertDir: dir}, now: time.Now}

	der, key := selfSigned(t, "example.com")
	res, err := m.writeCertificate([][]byte{der}, key)
	if err != nil {
		t.Fatalf("writeCertificate: %v", err)
	}
	if res.CertPath != filepath.Join(dir, certName) {
		t.Fatalf("cert path = %q", res.CertPath)
	}
	if !res.NotAfter.After(time.Now()) {
		t.Fatalf("NotAfter = %v, want in the future", res.NotAfter)
	}

	if _, err := CertNotAfter(res.CertPath); err != nil {
		t.Fatalf("CertNotAfter: %v", err)
	}
	if _, err := os.Stat(res.KeyPath); err != nil {
		t.Fatalf("private key missing: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(res.KeyPath)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("private key permissions = %o, want 600", perm)
		}
	}
}

func TestEnsureReusesValidCertificate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := &Manager{cfg: Config{Domain: "example.com", CertDir: dir}, now: time.Now}
	der, key := selfSigned(t, "example.com")
	if _, err := m.writeCertificate([][]byte{der}, key); err != nil {
		t.Fatalf("seed certificate: %v", err)
	}

	// No ACME server is configured, so any network attempt would fail loudly.
	res, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if res.Renewed {
		t.Fatal("Ensure reissued a valid certificate")
	}
}

func TestCertNotAfterRejectsGarbage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CertNotAfter(path); err == nil {
		t.Fatal("expected an error for a non-PEM file")
	}
}

// selfSigned returns a certificate and key for tests that do not need a CA.
func selfSigned(t *testing.T, domain string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: domain},
		DNSNames:              []string{domain},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return der, key
}
