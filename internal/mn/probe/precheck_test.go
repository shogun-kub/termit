package probe

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
	"math/big"
	"net"
	"testing"
	"time"
)

// The certificate precheck is what keeps a renewed certificate from reading as a failure and an
// expired one from reading as a success. These tests serve real certificates on loopback: the class
// is the contract, not the wording.

var precheckNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func precheckCert(t *testing.T, dnsNames []string, notBefore, notAfter time.Time) (tls.Certificate, *x509.Certificate) {
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
		NotBefore:             notBefore,
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

func precheckServe(t *testing.T, cert tls.Certificate) int {
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

func precheckFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func TestCertificatePrecheckClassifiesTheLeaf(t *testing.T) {
	t.Parallel()
	const domain = "node.example.com"

	validCert, validLeaf := precheckCert(t, []string{domain}, precheckNow.Add(-48*time.Hour), precheckNow.Add(90*24*time.Hour))
	validPort := precheckServe(t, validCert)
	expiredCert, expiredLeaf := precheckCert(t, []string{domain}, precheckNow.Add(-72*time.Hour), precheckNow.Add(-24*time.Hour))
	expiredPort := precheckServe(t, expiredCert)

	roots := x509.NewCertPool()
	roots.AddCert(validLeaf)
	roots.AddCert(expiredLeaf)
	worker := &Worker{cfg: Config{Roots: roots, Timeout: 5 * time.Second, Now: func() time.Time { return precheckNow }}}

	tests := []struct {
		name  string
		port  int
		pin   string
		want  Class
		known bool // whether the class is a verdict about the node
	}{
		{"the pinned certificate is accepted", validPort, precheckFingerprint(validLeaf), "", true},
		{"a renewed certificate is unknown, not a failure", validPort, precheckFingerprint(expiredLeaf), ClassCertChanged, false},
		{"an expired certificate is a failure", expiredPort, precheckFingerprint(expiredLeaf), ClassCertExpired, true},
		{"an expired certificate fails even with a stale pin", expiredPort, precheckFingerprint(validLeaf), ClassCertExpired, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, failed := worker.certificateResult(context.Background(), Target{
				ID: "uv_test", Address: "127.0.0.1", Port: tc.port, ServerName: domain, PinnedCertSHA256: tc.pin,
			})
			if got := failed; got != (tc.want != "") {
				t.Fatalf("failed = %v, want %v (result %+v)", got, tc.want != "", result)
			}
			if result.Class != tc.want {
				t.Errorf("class = %q, want %q (err %v)", result.Class, tc.want, result.Err)
			}
			if tc.want != "" && result.Err == nil {
				t.Error("a refusal came back without a reason")
			}
			if got := result.Unknown(); got == tc.known {
				t.Errorf("Unknown() = %v for class %q", got, result.Class)
			}
		})
	}
}

func TestCertificatePrecheckFailsAClosedPort(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: the node is not answering, which is a verdict (a timeout), not an
	// unknown.
	worker := &Worker{cfg: Config{Timeout: 2 * time.Second}}
	result, failed := worker.certificateResult(context.Background(), Target{
		ID: "uv_test", Address: "127.0.0.1", Port: 1, ServerName: "node.example.com",
	})
	if !failed || result.Class != ClassTimeout {
		t.Fatalf("result = %+v, want a timeout", result)
	}
	if result.Unknown() {
		t.Error("a node that does not answer was reported as unknown")
	}
}
