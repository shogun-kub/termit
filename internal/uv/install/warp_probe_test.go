package install

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

func probeTestIdentity(t *testing.T, cfg *config.Config) {
	t.Helper()
	if err := os.MkdirAll(cfg.Paths.StateDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	identity := &warp.Identity{
		PrivateKey: "a2V5LWZvci10ZXN0cy1vbmx5", PublicKey: "cHViLWZvci10ZXN0cy1vbmx5",
		Addresses: []string{"172.16.0.2/32"}, PeerPublicKey: "cGVlci1rZXk=",
		Endpoint: "162.159.192.1:2408", Reserved: []byte{1, 2, 3}, DeviceID: "device-1",
	}
	if err := warp.SaveIdentity(cfg.Paths.WarpIdentityPath(), identity); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
}

// runProbeStep runs the egress probe alone, with the node identity already in place.
func runProbeStep(t *testing.T, cfg *config.Config, probe func(context.Context, *config.Config, *state.State) (warp.Trace, error)) *Report {
	t.Helper()
	installer, err := New(Options{
		Config:    cfg,
		Exec:      &fakeExecutor{handler: func(cmd Cmd) (Result, error) { return okResult("") }},
		WarpHost:  &warp.Host{GOOS: "windows", GOARCH: "amd64"},
		WarpProbe: probe,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := installer.Run(context.Background(),
		RunOptions{Command: "init", Only: []string{"dirs", "identity", "warp-egress"}})
	if err != nil {
		t.Fatalf("Run: %v\n%+v", err, report)
	}
	return report
}

// The whole point of the step: a tunnel that is configured but does not carry traffic is a warning the
// operator has to see, not a silent direct egress.
func TestWarpProbeWarnsWhenTheTunnelExitsDirectly(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Warp.Mode = config.WarpAll
	cfg.Warp.Transport = config.WarpTransportWireGuard
	probeTestIdentity(t, cfg)
	probed := false
	report := runProbeStep(t, cfg, func(context.Context, *config.Config, *state.State) (warp.Trace, error) {
		probed = true
		return warp.Trace{IP: "203.0.113.9", Warp: "off"}, nil
	})
	if !probed {
		t.Fatal("the probe never ran")
	}
	entry := report.Steps[len(report.Steps)-1]
	if entry.Status != state.StepPresent || !strings.Contains(entry.Detail, "exits at 203.0.113.9") {
		t.Errorf("step = %+v, want a present step naming the direct exit", entry)
	}
}

func TestWarpProbeAcceptsAWarpExit(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Warp.Mode = config.WarpAll
	cfg.Warp.Transport = config.WarpTransportWireGuard
	probeTestIdentity(t, cfg)
	report := runProbeStep(t, cfg, func(context.Context, *config.Config, *state.State) (warp.Trace, error) {
		return warp.Trace{IP: "2a09:bac5::1", Warp: "on"}, nil
	})
	if entry := report.Steps[len(report.Steps)-1]; entry.Status != state.StepOK {
		t.Errorf("step = %+v, want ok", entry)
	}
}

func TestWarpProbeWarnsWhenItCannotProbe(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Warp.Mode = config.WarpAll
	cfg.Warp.Transport = config.WarpTransportWireGuard
	probeTestIdentity(t, cfg)
	report := runProbeStep(t, cfg, func(context.Context, *config.Config, *state.State) (warp.Trace, error) {
		return warp.Trace{}, errors.New("the temporary core never listened")
	})
	entry := report.Steps[len(report.Steps)-1]
	if entry.Status != state.StepPresent || !strings.Contains(entry.Detail, "could not be verified") {
		t.Errorf("step = %+v, want a present step saying the probe failed", entry)
	}
}

// TestWarpProbeWaitsForTheSidecar: the MASQUE relay is enabled by the step before this one, and a probe
// that runs before it listens exits through the balancer's fallback and blames the tunnel for a race. The
// step says what happened instead.
func TestWarpProbeWaitsForTheSidecar(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.Warp.Mode = config.WarpAll
	cfg.Warp.Transport = config.WarpTransportUsque
	called := false
	report := runProbeStep(t, cfg, func(context.Context, *config.Config, *state.State) (warp.Trace, error) {
		called = true
		return warp.Trace{IP: "203.0.113.9", Warp: "off"}, nil
	})
	if called {
		t.Error("the probe ran although the sidecar was not listening")
	}
	entry := report.Steps[len(report.Steps)-1]
	if entry.Status != state.StepPresent || !strings.Contains(entry.Detail, "not listening") {
		t.Errorf("step = %+v, want a present step saying the sidecar is not listening", entry)
	}
}

// TestListeningFor covers the readiness check itself: an open port is ready, a closed one is not, and
// zero wait means one attempt.
func TestListeningFor(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if !listeningFor(context.Background(), addr, 0) {
		t.Error("an open port was not reported as listening")
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if listeningFor(context.Background(), addr, 0) {
		t.Error("a closed port was reported as listening")
	}
}

// Nothing to verify when WARP is off or the node was never registered — and the step must not start a
// core to find that out.
func TestWarpProbeSkipsWhenThereIsNoTunnel(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*config.Config){
		"warp off": func(cfg *config.Config) { cfg.Warp.Mode = config.WarpOff },
		"not registered": func(cfg *config.Config) {
			cfg.Warp.Mode = config.WarpAll
			cfg.Warp.Transport = config.WarpTransportWireGuard
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t)
			prepare(cfg)
			called := false
			report := runProbeStep(t, cfg, func(context.Context, *config.Config, *state.State) (warp.Trace, error) {
				called = true
				return warp.Trace{}, nil
			})
			if called {
				t.Error("the probe ran when there was no tunnel to verify")
			}
			if entry := report.Steps[len(report.Steps)-1]; entry.Status != state.StepPresent {
				t.Errorf("step = %+v, want present", entry)
			}
		})
	}
}
