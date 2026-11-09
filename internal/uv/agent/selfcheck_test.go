package agent_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// writeCertificate puts a self-signed leaf with the given lifetime where nginx expects it.
func writeCertificate(t *testing.T, dir string, notAfter time.Time) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "one.example.com"},
		DNSNames:     []string{"one.example.com"},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), data, 0o644); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
}

func healthySelfCheckOptions(t *testing.T, now time.Time) agent.SelfCheckOptions {
	t.Helper()
	cfg := config.Default()
	cfg.Domain = "one.example.com"
	cfg.Paths.CertDir = t.TempDir()
	writeCertificate(t, cfg.Paths.CertDir, now.Add(60*24*time.Hour))
	return agent.SelfCheckOptions{
		Config: cfg,
		Now:    func() time.Time { return now },
		Active: func(context.Context, string) (bool, error) { return true, nil },
		Dial:   func(context.Context, string) error { return nil },
	}
}

func TestSelfCheckReportsAHealthyNode(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	report := agent.RunSelfCheck(context.Background(), healthySelfCheckOptions(t, now))
	if !report.OK {
		t.Fatalf("healthy node reported as broken: %+v", report)
	}
	if report.ErrorClass() != "" {
		t.Errorf("error class = %q", report.ErrorClass())
	}
	names := map[string]bool{}
	for _, item := range report.Items {
		names[item.Name] = true
	}
	for _, want := range []string{"xray", "proxy", "api", "web", "certificate", "warp"} {
		if !names[want] {
			t.Errorf("the self-check has no %q item", want)
		}
	}
}

func TestSelfCheckNamesTheBrokenPart(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

	t.Run("the core is not running", func(t *testing.T) {
		t.Parallel()
		opts := healthySelfCheckOptions(t, now)
		opts.Active = func(context.Context, string) (bool, error) { return false, nil }
		report := agent.RunSelfCheck(context.Background(), opts)
		if report.OK || report.ErrorClass() != "xray" {
			t.Errorf("report = %+v, want a failed xray item", report)
		}
	})

	t.Run("the proxy port is closed", func(t *testing.T) {
		t.Parallel()
		opts := healthySelfCheckOptions(t, now)
		opts.Dial = func(_ context.Context, addr string) error {
			if addr == "127.0.0.1:8443" {
				return errors.New("connection refused")
			}
			return nil
		}
		report := agent.RunSelfCheck(context.Background(), opts)
		if report.OK || report.ErrorClass() != "proxy" {
			t.Errorf("report = %+v, want a failed proxy item", report)
		}
	})

	t.Run("the certificate is inside the renewal window", func(t *testing.T) {
		t.Parallel()
		opts := healthySelfCheckOptions(t, now)
		writeCertificate(t, opts.Config.Paths.CertDir, now.Add(5*24*time.Hour))
		report := agent.RunSelfCheck(context.Background(), opts)
		if report.OK || report.ErrorClass() != "certificate" {
			t.Errorf("report = %+v, want a failed certificate item", report)
		}
	})

	t.Run("the certificate is missing", func(t *testing.T) {
		t.Parallel()
		opts := healthySelfCheckOptions(t, now)
		if err := os.Remove(filepath.Join(opts.Config.Paths.CertDir, "fullchain.pem")); err != nil {
			t.Fatalf("remove: %v", err)
		}
		report := agent.RunSelfCheck(context.Background(), opts)
		if report.OK || report.ErrorClass() != "certificate" {
			t.Errorf("report = %+v, want a failed certificate item", report)
		}
	})
}

func TestSelfCheckTreatsAnUnanswerableCheckAsUnknown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	opts := healthySelfCheckOptions(t, now)
	opts.Active = nil
	opts.Dial = nil
	report := agent.RunSelfCheck(context.Background(), opts)
	if !report.OK {
		t.Errorf("unknown checks made the node unhealthy: %+v", report)
	}
	unknown := 0
	for _, item := range report.Items {
		if item.Unknown {
			unknown++
		}
	}
	if unknown != 4 {
		t.Errorf("%d unknown items, want xray, proxy, api and web", unknown)
	}
	if report.ErrorClass() != "" {
		t.Errorf("error class = %q, want empty for unknown-only", report.ErrorClass())
	}
}

func TestSelfCheckReportsWarpAsUnknownWhenItIsConfigured(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	opts := healthySelfCheckOptions(t, now)
	opts.Config.Warp.Enabled = true
	report := agent.RunSelfCheck(context.Background(), opts)
	for _, item := range report.Items {
		if item.Name == "warp" {
			if !item.Unknown || item.OK {
				t.Errorf("warp item = %+v, want unknown", item)
			}
			return
		}
	}
	t.Fatal("no warp item")
}
