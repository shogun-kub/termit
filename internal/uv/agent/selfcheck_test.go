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
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/warp"
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
		// A host that cannot run WARP, so the default configuration (WARP on) is a converged state
		// and the test does not depend on the machine it runs on.
		WarpHost: &warp.Host{GOOS: "windows", GOARCH: "amd64"},
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

// WARP has three different answers and they must not be flattened: off is fine, a host that cannot
// run it is fine with a reason, and a tunnel that should work but does not is a failure.
func TestSelfCheckWarp(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	tun := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(tun, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	supported := &warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: tun}
	healthy := func(context.Context) (warp.Status, error) {
		return warp.Status{Installed: true, State: "connected", Detail: "via 203.0.113.7 (AMS)",
			Trace: &warp.Trace{IP: "203.0.113.7", Warp: "on"}}, nil
	}

	tests := []struct {
		name    string
		mode    string
		host    *warp.Host
		probe   func(context.Context) (warp.Status, error)
		wantOK  bool
		wantUnk bool
		want    string
	}{
		{"off", config.WarpOff, supported, healthy, true, false, "off"},
		{"a host that cannot run it", config.WarpAll, &warp.Host{GOOS: "windows"}, nil, true, false, "traffic exits directly"},
		{"a healthy tunnel", config.WarpAll, supported, healthy, true, false, "203.0.113.7"},
		{"a tunnel that is down and fails open", config.WarpAll, supported, func(context.Context) (warp.Status, error) {
			return warp.Status{Installed: true, State: "disconnected", Detail: "warp-cli reports disconnected"}, nil
		}, false, false, "exits directly"},
		{"a probe that cannot run", config.WarpAll, supported, func(context.Context) (warp.Status, error) {
			return warp.Status{}, errors.New("no route")
		}, false, true, "could not ask"},
		{"no probe at all", config.WarpAll, supported, nil, false, true, "needs a probe"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := healthySelfCheckOptions(t, now)
			opts.Config.Warp.Mode = tc.mode
			opts.WarpHost = tc.host
			opts.Warp = tc.probe
			report := agent.RunSelfCheck(context.Background(), opts)
			for _, item := range report.Items {
				if item.Name != "warp" {
					continue
				}
				if item.OK != tc.wantOK || item.Unknown != tc.wantUnk {
					t.Errorf("warp item = %+v, want ok=%v unknown=%v", item, tc.wantOK, tc.wantUnk)
				}
				if !strings.Contains(item.Detail, tc.want) {
					t.Errorf("detail = %q, want it to mention %q", item.Detail, tc.want)
				}
				return
			}
			t.Fatal("no warp item")
		})
	}
}
