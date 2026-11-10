package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writePair writes a self-signed pair for serial into dir and returns its paths.
func writePair(t *testing.T, dir string, serial int64) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "mn.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		DNSNames:     []string{"mn.example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// serialOf reads the serial the loader is currently serving.
func serialOf(t *testing.T, pair *tls.Certificate) int64 {
	t.Helper()
	if len(pair.Certificate) == 0 {
		t.Fatal("the pair holds no certificate")
	}
	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return parsed.SerialNumber.Int64()
}

func TestLoaderFollowsTheFileOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, 1)
	loader := NewLoader(certPath, keyPath)

	first, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got := serialOf(t, first); got != 1 {
		t.Fatalf("serial = %d, want 1", got)
	}

	// A renewal replaces both files; the running process must serve the new one without a restart.
	writePair(t, dir, 2)
	second, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate after renewal: %v", err)
	}
	if got := serialOf(t, second); got != 2 {
		t.Errorf("serial after renewal = %d, want 2", got)
	}
}

func TestLoaderDoesNotRereadAnUnchangedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, 1)
	loader := NewLoader(certPath, keyPath)

	if _, err := loader.GetCertificate(nil); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	first := loader.pair

	if _, err := loader.GetCertificate(nil); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if loader.pair != first {
		t.Error("an unchanged file was parsed again")
	}
}

func TestLoaderKeepsServingThroughABrokenRenewal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, 1)
	loader := NewLoader(certPath, keyPath)

	good, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}

	// A half-written pair — the certificate replaced, the key gone — must not take TLS down.
	if err := os.WriteFile(certPath, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	stillGood, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate with a broken pair: %v", err)
	}
	if serialOf(t, stillGood) != serialOf(t, good) {
		t.Error("the loader swapped in a broken pair instead of keeping the working one")
	}
}

func TestLoaderWithoutAnyCertificateFails(t *testing.T) {
	t.Parallel()
	loader := NewLoader(filepath.Join(t.TempDir(), "missing.pem"), filepath.Join(t.TempDir(), "missing.key"))
	if _, err := loader.GetCertificate(nil); err == nil {
		t.Fatal("GetCertificate succeeded without a certificate")
	}
}

func TestKeepFreshRenewsAndSurvivesAFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logs := filepath.Join(dir, "log")
	logFile, err := os.Create(logs)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))

	calls := make(chan struct{}, 4)
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go KeepFresh(ctx, time.Millisecond, func(context.Context) (bool, error) {
		attempts++
		calls <- struct{}{}
		if attempts == 1 {
			return false, os.ErrDeadlineExceeded
		}
		return true, nil
	}, logger)

	// One failed tick, then a successful one: a failure must not end the loop.
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("the loop made %d attempts, want at least %d", attempts, i+1)
		}
	}
	cancel()

	// The failure was logged, and the loop did not treat it as fatal.
	content, err := os.ReadFile(logs)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(content) == 0 {
		t.Error("a failed renewal was not logged")
	}
}
