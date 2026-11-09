package certcheck_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/certcheck"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// issue creates a certificate and returns it with its key; notAfter and the SANs are the knobs the
// tests turn.
func issue(t *testing.T, dnsNames []string, notAfter time.Time) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		NotBefore:             testNow.Add(-time.Hour),
		NotAfter:              notAfter,
		DNSNames:              dnsNames,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, parsed
}

// serve starts a TLS listener that answers with cert until the test ends.
func serve(t *testing.T, cert tls.Certificate) int {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = conn.(*tls.Conn).Handshake()
				_ = conn.Close()
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func rootsWith(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

func TestCheckAcceptsAGoodCertificateAndPinsIt(t *testing.T) {
	t.Parallel()
	const domain = "node.example.com"
	chain, leaf := issue(t, []string{domain}, testNow.Add(60*24*time.Hour))
	port := serve(t, chain)

	result, err := certcheck.Check(context.Background(), domain, certcheck.Options{
		Port: port, Address: "127.0.0.1", Roots: rootsWith(leaf), Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.ChainVerified {
		t.Error("the chain was not verified against the supplied root")
	}
	sum := sha256.Sum256(leaf.Raw)
	if want := hex.EncodeToString(sum[:]); result.Fingerprint != want {
		t.Errorf("fingerprint = %s, want %s", result.Fingerprint, want)
	}
	if !result.NotAfter.Equal(leaf.NotAfter) {
		t.Errorf("NotAfter = %v, want %v", result.NotAfter, leaf.NotAfter)
	}
}

func TestCheckRefusesAnUntrustedChain(t *testing.T) {
	t.Parallel()
	const domain = "node.example.com"
	chain, _ := issue(t, []string{domain}, testNow.Add(60*24*time.Hour))
	port := serve(t, chain)

	// The certificate is fine, but the prober has no reason to trust it.
	result, err := certcheck.Check(context.Background(), domain, certcheck.Options{
		Port: port, Address: "127.0.0.1", Now: func() time.Time { return testNow },
	})
	var refused *certcheck.Error
	if !errors.As(err, &refused) || refused.Class != certcheck.ClassInvalidChain {
		t.Fatalf("Check = %v, want an invalid chain", err)
	}
	if result.Fingerprint == "" {
		t.Error("the fingerprint is missing even though a certificate was read")
	}
}

func TestCheckRefusesTheWrongHostname(t *testing.T) {
	t.Parallel()
	chain, leaf := issue(t, []string{"other.example.com"}, testNow.Add(60*24*time.Hour))
	port := serve(t, chain)

	_, err := certcheck.Check(context.Background(), "node.example.com", certcheck.Options{
		Port: port, Address: "127.0.0.1", Roots: rootsWith(leaf), Now: func() time.Time { return testNow },
	})
	var refused *certcheck.Error
	if !errors.As(err, &refused) || refused.Class != certcheck.ClassHostname {
		t.Fatalf("Check = %v, want a hostname mismatch", err)
	}
}

func TestCheckRefusesAnExpiringCertificate(t *testing.T) {
	t.Parallel()
	const domain = "node.example.com"
	chain, leaf := issue(t, []string{domain}, testNow.Add(24*time.Hour))
	port := serve(t, chain)

	_, err := certcheck.Check(context.Background(), domain, certcheck.Options{
		Port: port, Address: "127.0.0.1", Roots: rootsWith(leaf), Now: func() time.Time { return testNow },
	})
	var refused *certcheck.Error
	if !errors.As(err, &refused) || refused.Class != certcheck.ClassExpiring {
		t.Fatalf("Check = %v, want an expiring certificate", err)
	}
	if refused.Detail == "" {
		t.Error("the refusal does not say when the certificate expires")
	}
}

func TestCheckReportsAnUnreachableNode(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: the check must say so instead of waiting.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	_, err = certcheck.Check(context.Background(), "127.0.0.1", certcheck.Options{
		Port: port, Timeout: 2 * time.Second, Now: func() time.Time { return testNow },
	})
	var refused *certcheck.Error
	if !errors.As(err, &refused) || refused.Class != certcheck.ClassUnreachable {
		t.Fatalf("Check = %v, want an unreachable node", err)
	}
}

func TestCheckRejectsAnEmptyHostname(t *testing.T) {
	t.Parallel()
	if _, err := certcheck.Check(context.Background(), "", certcheck.Options{}); err == nil {
		t.Fatal("Check accepted an empty hostname")
	}
}
