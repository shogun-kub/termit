package install

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/state"
)

func TestCertificateCheck(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	sb := newSandbox()
	inst := newTestInstaller(t, cfg, sb)
	step := &certificateStep{inst.env}
	certPath := filepath.Join(cfg.Paths.CertDir, certFile)
	keyPath := filepath.Join(cfg.Paths.CertDir, keyFile)

	check, err := step.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if check.Satisfied {
		t.Fatal("Check reported a certificate that does not exist")
	}
	if !strings.Contains(check.Detail, "no certificate") {
		t.Errorf("detail = %q, want a missing-certificate message", check.Detail)
	}

	if err := os.MkdirAll(cfg.Paths.CertDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	notAfter := engineNow.Add(80 * 24 * time.Hour)
	writeSelfSigned(t, certPath, keyPath, notAfter)

	// On disk but not in the state: the node would not know what it is serving.
	check, err = step.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if check.Satisfied {
		t.Error("Check accepted a certificate the state does not record")
	}
	if !strings.Contains(check.Detail, "does not record") {
		t.Errorf("detail = %q, want a message about the state", check.Detail)
	}

	// Recorded and fresh: nothing to do.
	inst.env.st.Certificate = &state.Certificate{IssuedAt: engineNow, NotAfter: notAfter}
	check, err = step.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !check.Satisfied {
		t.Errorf("Check refused a valid certificate: %s", check.Detail)
	}
	if !strings.Contains(check.Detail, "valid until") {
		t.Errorf("detail = %q, want the expiry", check.Detail)
	}

	// Inside the renewal window the step must run again, even though the file is fine.
	soon := engineNow.Add(10 * 24 * time.Hour)
	writeSelfSigned(t, certPath, keyPath, soon)
	inst.env.st.Certificate = &state.Certificate{IssuedAt: engineNow, NotAfter: soon}
	check, err = step.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if check.Satisfied {
		t.Error("Check accepted a certificate that is due for renewal")
	}
	if !strings.Contains(check.Detail, "due for renewal") {
		t.Errorf("detail = %q, want a renewal message", check.Detail)
	}
}

func TestCertificateRenewBeforeFollowsConfig(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.ACME.RenewBeforeDays = 45
	inst := newTestInstaller(t, cfg, newSandbox())
	step := &certificateStep{inst.env}
	if got, want := step.renewBefore(), 45*24*time.Hour; got != want {
		t.Errorf("renewBefore = %v, want %v", got, want)
	}
}

// writeSelfSigned creates the pair the way the ACME client would: a certificate and its key.
func writeSelfSigned(t *testing.T, certPath, keyPath string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "vpn.example.com"},
		DNSNames:     []string{"vpn.example.com"},
		NotBefore:    engineNow.Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("placeholder key"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
