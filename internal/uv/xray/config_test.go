package xray_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/xray"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func testSpec() xray.Spec {
	spec := xray.DefaultSpec()
	spec.Path = "/a1b2c3d4e5f60718/"
	spec.Clients = []xray.Client{
		{ID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5", Email: "owner"},
		{ID: "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", Email: "family-tablet"},
	}
	return spec
}

// TestGenerateGolden pins the whole document. The config is the contract with the core: a change a
// reviewer cannot see in a diff is a change nobody can review.
func TestGenerateGolden(t *testing.T) {
	t.Parallel()
	data, err := xray.Generate(testSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden := filepath.Join("testdata", "xray.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden) // #nosec G304 -- a fixed test path.
	if err != nil {
		t.Fatalf("read golden file: %v (run `go test ./internal/uv/xray -update`)", err)
	}
	if !bytes.Equal(want, data) {
		t.Errorf("%s differs from the generated config; regenerate with -update and review the diff", golden)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	t.Parallel()
	first, err := xray.Generate(testSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := xray.Generate(testSpec())
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d produced different bytes", i+2)
		}
	}
}

func TestGenerateShape(t *testing.T) {
	t.Parallel()
	data, err := xray.Generate(testSpec())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var doc struct {
		API struct {
			Tag      string   `json:"tag"`
			Services []string `json:"services"`
		} `json:"api"`
		Inbounds []struct {
			Tag      string `json:"tag"`
			Listen   string `json:"listen"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			Settings struct {
				Clients    []xray.Client `json:"clients"`
				Decryption string        `json:"decryption"`
				Address    string        `json:"address"`
			} `json:"settings"`
			StreamSettings struct {
				Network  string `json:"network"`
				Security string `json:"security"`
				XHTTP    struct {
					Path string `json:"path"`
					Mode string `json:"mode"`
				} `json:"xhttpSettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the generated config is not JSON: %v", err)
	}

	if doc.API.Tag != xray.APITag {
		t.Errorf("api tag = %q, want %q", doc.API.Tag, xray.APITag)
	}
	if len(doc.API.Services) == 0 {
		t.Error("the api declares no services, so `xray api` would fail")
	}
	if len(doc.Inbounds) != 2 {
		t.Fatalf("inbounds = %d, want the api inbound and the proxy inbound", len(doc.Inbounds))
	}

	api := doc.Inbounds[0]
	if api.Tag != xray.APITag || api.Protocol != "dokodemo-door" {
		t.Errorf("first inbound = %+v, want the dokodemo-door api inbound", api)
	}
	if api.Listen != "127.0.0.1" {
		t.Errorf("api inbound listens on %q, want loopback only", api.Listen)
	}

	proxy := doc.Inbounds[1]
	if proxy.Tag != xray.InboundTag || proxy.Protocol != "vless" {
		t.Errorf("second inbound = %+v, want the vless proxy inbound", proxy)
	}
	if proxy.Listen != "127.0.0.1" {
		t.Errorf("proxy inbound listens on %q: Xray must never be reachable directly", proxy.Listen)
	}
	if proxy.StreamSettings.Security != "none" {
		t.Errorf("stream security = %q, want none: nginx terminates TLS", proxy.StreamSettings.Security)
	}
	if proxy.StreamSettings.XHTTP.Path != testSpec().Path {
		t.Errorf("xhttp path = %q, want %q", proxy.StreamSettings.XHTTP.Path, testSpec().Path)
	}
	if proxy.StreamSettings.XHTTP.Mode != "packet-up" {
		t.Errorf("xhttp mode = %q, want packet-up", proxy.StreamSettings.XHTTP.Mode)
	}
	if got := len(proxy.Settings.Clients); got != len(testSpec().Clients) {
		t.Errorf("clients = %d, want %d", got, len(testSpec().Clients))
	}
	if proxy.Settings.Decryption != "none" {
		t.Errorf("decryption = %q, want none for VLESS", proxy.Settings.Decryption)
	}

	if len(doc.Routing.Rules) != 1 || doc.Routing.Rules[0].OutboundTag != xray.APITag {
		t.Errorf("routing = %+v, want the api rule that `xray api` needs", doc.Routing.Rules)
	}
	if len(doc.Outbounds) != 2 {
		t.Errorf("outbounds = %d, want direct and block", len(doc.Outbounds))
	}
}

func TestGenerateRefusesBrokenSpecs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, want string
		mutate     func(*xray.Spec)
	}{
		{"no clients", "at least one client", func(s *xray.Spec) { s.Clients = nil }},
		{"bad uuid", "uuid", func(s *xray.Spec) { s.Clients[0].ID = "nope" }},
		{"duplicate email", "used twice", func(s *xray.Spec) { s.Clients[1].Email = s.Clients[0].Email }},
		{"no path", "path is required", func(s *xray.Spec) { s.Path = "" }},
		{"half a path", "must start and end with /", func(s *xray.Spec) { s.Path = "/abc" }},
		{"path with a quote", "cannot carry", func(s *xray.Spec) { s.Path = `/a"b/` }},
		{"bad port", "out of range", func(s *xray.Spec) { s.Port = 0 }},
		{"bad log level", "log level", func(s *xray.Spec) { s.LogLevel = "verbose" }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := testSpec()
			tc.mutate(&spec)
			_, err := xray.Generate(spec)
			if err == nil {
				t.Fatal("Generate accepted a spec it must refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestAddresses(t *testing.T) {
	t.Parallel()
	spec := testSpec()
	if got, want := spec.ProxyAddress(), "127.0.0.1:8443"; got != want {
		t.Errorf("ProxyAddress = %q, want %q", got, want)
	}
	if got, want := spec.APIAddress(), "127.0.0.1:10085"; got != want {
		t.Errorf("APIAddress = %q, want %q", got, want)
	}
}
