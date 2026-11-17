package install

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// supportedHost is a host WARP could run on: a Linux box with a tun device.
func supportedHost(t *testing.T) *warp.Host {
	t.Helper()
	tun := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(tun, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return &warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: tun}
}

func warpConfig(t *testing.T, mode string) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Warp.Mode = mode
	if mode == config.WarpSelected {
		cfg.Warp.Selected = []string{"example.com"}
	}
	return cfg
}

func runWarpStep(t *testing.T, cfg *config.Config, host *warp.Host, exec *fakeExecutor) *Report {
	t.Helper()
	installer, err := New(Options{Config: cfg, Exec: exec, WarpHost: host})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := installer.Run(context.Background(), RunOptions{Command: "init", Only: []string{"warp"}})
	if err != nil {
		t.Fatalf("Run: %v\n%+v", err, report)
	}
	return report
}

func TestWarpStepIsOffWhenTheOwnerTurnedItOff(t *testing.T) {
	t.Parallel()
	exec := &fakeExecutor{handler: func(Cmd) (Result, error) { return okResult("") }}
	report := runWarpStep(t, warpConfig(t, config.WarpOff), supportedHost(t), exec)
	if got := report.Steps[0].Status; got != state.StepPresent {
		t.Errorf("status = %q, want present", got)
	}
	if !strings.Contains(report.Steps[0].Detail, "off") {
		t.Errorf("detail = %q", report.Steps[0].Detail)
	}
	if len(exec.calls) != 0 {
		t.Errorf("the step ran commands with WARP off: %v", exec.calls)
	}
}

// The refusal is the point: a container without TUN gets a reason, not a failed install.
func TestWarpStepRefusesAnUnsupportedHost(t *testing.T) {
	t.Parallel()
	exec := &fakeExecutor{handler: func(Cmd) (Result, error) { return okResult("") }}
	host := &warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: filepath.Join(t.TempDir(), "absent")}
	report := runWarpStep(t, warpConfig(t, config.WarpAll), host, exec)
	if got := report.Steps[0].Status; got != state.StepPresent {
		t.Fatalf("status = %q, want present (the refusal is a converged state)", got)
	}
	if !strings.Contains(report.Steps[0].Detail, "is missing") {
		t.Errorf("detail = %q, want the refusal reason", report.Steps[0].Detail)
	}
	if len(exec.calls) != 0 {
		t.Errorf("the step touched the host although WARP cannot run: %v", exec.calls)
	}
}

func TestWarpStepConvergesTheSidecar(t *testing.T) {
	t.Parallel()
	installed := false
	exec := &fakeExecutor{handler: func(cmd Cmd) (Result, error) {
		switch {
		case cmd.Name == "sh":
			installed = true
			return okResult("")
		case cmd.Name == warp.DefaultBinary && strings.Join(cmd.Args, " ") == "--version":
			if !installed {
				return failedResult(cmd, 127, "not installed")
			}
			return okResult("2026.1.1")
		case cmd.Name == warp.DefaultBinary && strings.Join(cmd.Args, " ") == "--accept-tos status":
			return okResult("Status update: Connected")
		case cmd.Name == warp.DefaultBinary && strings.Join(cmd.Args, " ") == "--accept-tos registration show":
			return failedResult(cmd, 1, "Registration missing")
		case cmd.Name == warp.DefaultBinary:
			return okResult("")
		}
		return failedResult(cmd, 1, "unexpected command")
	}}
	report := runWarpStep(t, warpConfig(t, config.WarpAll), supportedHost(t), exec)
	if got := report.Steps[0].Status; got != state.StepOK {
		t.Fatalf("status = %q, want ok: %+v", got, report.Steps[0])
	}
	var commands []string
	for _, call := range exec.calls {
		commands = append(commands, call.String())
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"sh -c " + warp.InstallScript,
		warp.DefaultBinary + " --accept-tos registration new",
		warp.DefaultBinary + " --accept-tos mode proxy",
		warp.DefaultBinary + " --accept-tos connect",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the step never ran %q; it ran:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "registration new") != 1 {
		t.Errorf("registration ran %d times, want once:\n%s", strings.Count(joined, "registration new"), joined)
	}
}

func TestWarpStepReportsAConvergedSidecarAsPresent(t *testing.T) {
	t.Parallel()
	exec := &fakeExecutor{handler: func(cmd Cmd) (Result, error) {
		switch {
		case strings.Join(cmd.Args, " ") == "--version":
			return okResult("2026.1.1")
		case strings.Join(cmd.Args, " ") == "--accept-tos status":
			return okResult("Status update: Connected")
		}
		return failedResult(cmd, 1, "unexpected command")
	}}
	report := runWarpStep(t, warpConfig(t, config.WarpSelected), supportedHost(t), exec)
	if got := report.Steps[0].Status; got != state.StepPresent {
		t.Fatalf("status = %q, want present", got)
	}
	if !strings.Contains(report.Steps[0].Detail, "connected") {
		t.Errorf("detail = %q", report.Steps[0].Detail)
	}
}

// wireguardRegistrar is a fake Cloudflare: it answers one registration and counts how many times it was
// asked, which is what proves the second run of init reuses the identity.
func wireguardRegistrar(t *testing.T, calls *int) warp.Registrar {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
		  "id": "device-1",
		  "config": {
		    "client_id": "8BmS",
		    "peers": [{"public_key": "peer-key", "endpoint": {"host": "162.159.192.1:2408"}}],
		    "interface": {"addresses": {"v4": "172.16.0.2", "v6": "2606:4700:110:8d9c::2"}}
		  }
		}`)
	}))
	t.Cleanup(server.Close)
	return warp.Registrar{BaseURL: server.URL}
}

// The wireguard transport does not install a sidecar: it registers a device once and keeps the identity.
func TestWarpStepRegistersTheWireGuardIdentityOnce(t *testing.T) {
	t.Parallel()
	cfg := warpConfig(t, config.WarpAll)
	cfg.Warp.Transport = config.WarpTransportWireGuard
	exec := &fakeExecutor{handler: func(Cmd) (Result, error) { return okResult("") }}
	calls := 0

	installer, err := New(Options{Config: cfg, Exec: exec, WarpHost: supportedHost(t), Registrar: wireguardRegistrar(t, &calls)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if report, err := installer.Run(ctx, RunOptions{Command: "init", Only: []string{"warp"}}); err != nil {
		t.Fatalf("Run: %v\n%+v", err, report)
	}
	if calls != 1 {
		t.Fatalf("the registrar was called %d times, want 1", calls)
	}
	identity, err := warp.LoadIdentity(cfg.Paths.WarpIdentityPath())
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if identity.DeviceID != "device-1" || identity.Endpoint != "162.159.192.1:2408" {
		t.Errorf("identity = %+v", identity)
	}

	// A second run must find the identity and leave Cloudflare alone.
	second, err := New(Options{Config: cfg, Exec: exec, WarpHost: supportedHost(t), Registrar: wireguardRegistrar(t, &calls)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if report, err := second.Run(ctx, RunOptions{Command: "init", Only: []string{"warp"}}); err != nil {
		t.Fatalf("second Run: %v\n%+v", err, report)
	}
	if calls != 1 {
		t.Errorf("the second run registered again (%d calls)", calls)
	}
	if !strings.Contains(second.Steps()[4].Name(), "warp") {
		t.Fatal("the pipeline moved; this test assumed warp is the fifth step")
	}
}

// Switching the transport rewrites the document in both directions, and neither direction loses the
// identity: it is what the node would need to switch back, and a device on Cloudflare's side is not
// something an installer should throw away.
func TestWarpTransportSwitchGeneratesBothDocuments(t *testing.T) {
	t.Parallel()
	cfg := warpConfig(t, config.WarpAll)
	cfg.Warp.Transport = config.WarpTransportWireGuard
	exec := &fakeExecutor{handler: func(Cmd) (Result, error) { return okResult("") }}
	calls := 0
	installer, err := New(Options{Config: cfg, Exec: exec, WarpHost: supportedHost(t), Registrar: wireguardRegistrar(t, &calls)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if report, err := installer.Run(ctx, RunOptions{Command: "init", Only: []string{"dirs", "warp", "identity", "xray-config"}}); err != nil {
		t.Fatalf("wireguard run: %v\n%+v", err, report)
	}
	if got := outboundProtocols(t, cfg); got["warp"] != "wireguard" {
		t.Fatalf("the wireguard document has %v, want a wireguard warp outbound", got)
	}
	if got := ruleSummary(t, cfg); !strings.Contains(got, "tcp,udp->warp-egress") {
		t.Errorf("the wireguard document routes %q, want one tcp,udp rule through the balancer", got)
	}

	// Now the other direction: the same node, told to use the sidecar.
	cfg.Warp.Transport = config.WarpTransportProxy
	second, err := New(Options{Config: cfg, Exec: exec, WarpHost: supportedHost(t), Registrar: wireguardRegistrar(t, &calls)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if report, err := second.Run(ctx, RunOptions{Command: "init", Only: []string{"xray-config"}}); err != nil {
		t.Fatalf("proxy run: %v\n%+v", err, report)
	}
	if got := outboundProtocols(t, cfg); got["warp"] != "socks" {
		t.Fatalf("the proxy document has %v, want a socks warp outbound", got)
	}
	if got := ruleSummary(t, cfg); !strings.Contains(got, "udp->direct") || !strings.Contains(got, "tcp->warp-egress") {
		t.Errorf("the proxy document routes %q, want the split that keeps UDP working", got)
	}
	if _, err := warp.LoadIdentity(cfg.Paths.WarpIdentityPath()); err != nil {
		t.Errorf("the identity did not survive the switch back: %v", err)
	}
}

// outboundProtocols maps an outbound tag to its protocol in the generated document.
func outboundProtocols(t *testing.T, cfg *config.Config) map[string]string {
	t.Helper()
	var doc struct {
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"outbounds"`
	}
	data, err := os.ReadFile(cfg.Paths.XrayConfig)
	if err != nil {
		t.Fatalf("read the generated config: %v", err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode the generated config: %v", err)
	}
	out := map[string]string{}
	for _, outbound := range doc.Outbounds {
		out[outbound.Tag] = outbound.Protocol
	}
	return out
}

// ruleSummary renders the WARP-related routing rules as "network->target", which is what the two
// transports differ in.
func ruleSummary(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var doc struct {
		Routing struct {
			Rules []struct {
				Network     string `json:"network"`
				OutboundTag string `json:"outboundTag"`
				BalancerTag string `json:"balancerTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	data, err := os.ReadFile(cfg.Paths.XrayConfig)
	if err != nil {
		t.Fatalf("read the generated config: %v", err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode the generated config: %v", err)
	}
	parts := make([]string, 0, len(doc.Routing.Rules))
	for _, rule := range doc.Routing.Rules {
		target := rule.OutboundTag
		if target == "" {
			target = rule.BalancerTag
		}
		if rule.Network == "" {
			continue
		}
		parts = append(parts, rule.Network+"->"+target)
	}
	return strings.Join(parts, ",")
}

// Cloudflare is unreachable from networks that block it, and that must not be the thing that stops an
// install: the step converges into a warning, the document goes out directly, and the run succeeds.
func TestWarpRegistrationFailureIsAWarning(t *testing.T) {
	t.Parallel()
	cfg := warpConfig(t, config.WarpAll)
	cfg.Warp.Transport = config.WarpTransportWireGuard
	exec := &fakeExecutor{handler: func(Cmd) (Result, error) { return okResult("") }}
	// Nothing listens on port 1, which is what a blocked or filtered Cloudflare looks like to the step.
	installer, err := New(Options{Config: cfg, Exec: exec, WarpHost: supportedHost(t),
		Registrar: warp.Registrar{BaseURL: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := installer.Run(context.Background(),
		RunOptions{Command: "init", Only: []string{"dirs", "warp", "identity", "xray-config"}})
	if err != nil {
		t.Fatalf("Run: %v\n%+v (an unreachable Cloudflare must not fail the install)", err, report)
	}
	if got := report.Steps[1]; got.Status != state.StepPresent || !strings.Contains(got.Detail, "not registered") {
		t.Errorf("warp step = %+v, want present with the registration warning", got)
	}
	if got := outboundProtocols(t, cfg); got["warp"] != "" {
		t.Errorf("the document has a warp outbound %v, want none without an identity", got["warp"])
	}
	if got := ruleSummary(t, cfg); got != "" {
		t.Errorf("the document routes %q, want everything direct", got)
	}
	if _, err := os.Stat(cfg.Paths.WarpIdentityPath()); !os.IsNotExist(err) {
		t.Errorf("an identity file exists after a failed registration: %v", err)
	}
}
