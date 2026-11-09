//go:build pebble

// Integration test for the ACME path against a real CA (Pebble).
//
// It is excluded from the default build because it needs a reachable ACME directory, a domain that
// resolves to the machine running the test, and the right to bind port 80. Run it as a cross-compiled
// test binary on the node:
//
//	GOOS=linux GOARCH=amd64 go test -c -tags=pebble ./internal/uv/acme -o acme.test
//	TERMIT_PEBBLE_DIR=https://termit-mn.lan:14000/dir TERMIT_TEST_DOMAIN=termit-uv1.lan ./acme.test -test.v
package acme

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPebbleIssue(t *testing.T) {
	directory := os.Getenv("TERMIT_PEBBLE_DIR")
	domain := os.Getenv("TERMIT_TEST_DOMAIN")
	if directory == "" || domain == "" {
		t.Skip("TERMIT_PEBBLE_DIR and TERMIT_TEST_DOMAIN must be set")
	}
	certDir := os.Getenv("TERMIT_TEST_CERTDIR")
	if certDir == "" {
		certDir = t.TempDir()
	}

	m, err := New(Config{
		Domain:        domain,
		DirectoryURL:  directory,
		SkipTLSVerify: true, // Pebble's own listener certificate is self-signed.
		CertDir:       certDir,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The CA validates by fetching http://<domain>:80/.well-known/acme-challenge/<token>.
	ln, err := net.Listen("tcp", ":80")
	if err != nil {
		t.Fatalf("listen on :80: %v", err)
	}
	srv := &http.Server{Handler: m.ChallengeHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	started := time.Now()
	res, err := m.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Logf("issued in %s: cert=%s notAfter=%s", time.Since(started).Round(time.Millisecond), res.CertPath, res.NotAfter)

	if !res.Renewed {
		t.Fatal("expected a fresh certificate")
	}
	if !res.NotAfter.After(time.Now()) {
		t.Fatalf("NotAfter = %v, want in the future", res.NotAfter)
	}

	data, err := os.ReadFile(res.CertPath)
	if err != nil {
		t.Fatalf("read chain: %v", err)
	}
	var certs []*x509.Certificate
	for block, rest := pem.Decode(data); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse certificate: %v", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		t.Fatal("chain contains no certificates")
	}
	leaf := certs[0]
	if err := leaf.VerifyHostname(domain); err != nil {
		t.Fatalf("certificate does not match %s: %v", domain, err)
	}
	if !strings.Contains(leaf.Issuer.CommonName, "Pebble") {
		t.Fatalf("issuer = %q, want a Pebble CA", leaf.Issuer.CommonName)
	}

	// A second Ensure must reuse the certificate rather than hitting the CA again.
	again, err := m.Ensure(ctx)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if again.Renewed {
		t.Fatal("second Ensure reissued a valid certificate")
	}
	if !again.NotAfter.Equal(res.NotAfter) {
		t.Fatalf("NotAfter changed between calls: %v vs %v", again.NotAfter, res.NotAfter)
	}

	if info, err := os.Stat(res.KeyPath); err == nil && info.Mode().Perm() != 0o600 {
		t.Fatalf("private key permissions = %o, want 600", info.Mode().Perm())
	}
	t.Logf("chain length: %d, files in %s", len(certs), filepath.Dir(res.CertPath))
}
