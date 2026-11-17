package warp_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/shogun-kub/termit/internal/uv/warp"
)

// registrationAnswer is the shape Cloudflare returns: an id, a peer to reach, the addresses inside the
// tunnel and the three-byte client id that rides in the WireGuard header.
const registrationAnswer = `{
  "id": "00000000-0000-0000-0000-000000000001",
  "token": "an-access-token-that-must-not-be-kept",
  "config": {
    "client_id": "8BmS",
    "peers": [{"public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
               "endpoint": {"host": "162.159.192.1:2408"}}],
    "interface": {"addresses": {"v4": "172.16.0.2", "v6": "2606:4700:110:8d9c::2"}}
  }
}`

// registrarAnswer returns a registrar pointed at a fake API and a getter for the request it received.
// The getter is a function because the request only exists once Register has run.
func registrarAnswer(t *testing.T, status int, body string) (*warp.Registrar, func() *http.Request) {
	t.Helper()
	var mu sync.Mutex
	var captured *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the request body: %v", err)
		}
		clone := r.Clone(context.Background())
		clone.Body = io.NopCloser(strings.NewReader(string(payload)))
		mu.Lock()
		captured = clone
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return &warp.Registrar{
		BaseURL: server.URL,
		Now:     func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) },
	}, func() *http.Request {
		mu.Lock()
		defer mu.Unlock()
		return captured
	}
}

// The point of the client: a real key pair, the peer, the tunnel addresses and the reserved bytes, and
// no token — nothing this node does not need is kept.
func TestRegistrarTurnsAnAnswerIntoAnIdentity(t *testing.T) {
	t.Parallel()
	registrar, request := registrarAnswer(t, http.StatusOK, registrationAnswer)
	identity, err := registrar.Register(context.Background())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if identity.DeviceID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("device id = %q", identity.DeviceID)
	}
	if identity.Endpoint != "162.159.192.1:2408" {
		t.Errorf("endpoint = %q", identity.Endpoint)
	}
	if identity.PeerPublicKey == "" {
		t.Error("the peer public key is empty")
	}
	if strings.Join(identity.Addresses, ",") != "172.16.0.2/32,2606:4700:110:8d9c::2/128" {
		t.Errorf("addresses = %v, want both with prefix lengths", identity.Addresses)
	}
	if len(identity.Reserved) != 3 {
		t.Fatalf("reserved = %v, want the three bytes from the client id", identity.Reserved)
	}
	if !identity.RegisteredAt.Equal(time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("registered at = %v", identity.RegisteredAt)
	}

	// The pair must be a real WireGuard pair: the public half derived from the private one.
	privateKey, err := base64.StdEncoding.DecodeString(identity.PrivateKey)
	if err != nil || len(privateKey) != 32 {
		t.Fatalf("private key = %q (%v)", identity.PrivateKey, err)
	}
	publicKey, err := base64.StdEncoding.DecodeString(identity.PublicKey)
	if err != nil || len(publicKey) != 32 {
		t.Fatalf("public key = %q (%v)", identity.PublicKey, err)
	}
	derived, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if string(derived) != string(publicKey) {
		t.Error("the public key is not the one the private key derives")
	}

	// The request has to look like the client's: path, headers, and a base64 key in the body.
	capturedRequest := request()
	if capturedRequest == nil {
		t.Fatal("the fake API received no request")
	}
	if capturedRequest.URL.Path != "/reg" {
		t.Errorf("the request went to %q, want /reg", capturedRequest.URL.Path)
	}
	if got := capturedRequest.Header.Get("User-Agent"); got != "okhttp/3.12.1" {
		t.Errorf("user agent = %q", got)
	}
	if got := capturedRequest.Header.Get("CF-Client-Version"); got == "" {
		t.Error("no CF-Client-Version header")
	}
	if got := capturedRequest.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
	payload, _ := io.ReadAll(capturedRequest.Body)
	var sent map[string]any
	if err := json.Unmarshal(payload, &sent); err != nil {
		t.Fatalf("the request body is not JSON: %v", err)
	}
	if sent["warp_enabled"] != true {
		t.Errorf("warp_enabled = %v", sent["warp_enabled"])
	}
	if key, _ := sent["key"].(string); len(key) != 44 {
		t.Errorf("key in the request = %q, want a base64 Curve25519 public key", key)
	}
}

// Cloudflare names its endpoint for both address families. The core resolves that name itself and can
// take the AAAA record on a host without IPv6, where the handshake dies with "network is unreachable"
// and the balancer quietly falls back to direct — so the address is decided once, here.
func TestRegistrarPinsTheEndpointToIpv4(t *testing.T) {
	t.Parallel()
	const named = `{
  "id": "00000000-0000-0000-0000-000000000002",
  "config": {
    "client_id": "8BmS",
    "peers": [{"public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
               "endpoint": {"host": "engage.cloudflareclient.com:2408"}}],
    "interface": {"addresses": {"v4": "172.16.0.2"}}
  }
}`
	cases := []struct {
		name   string
		lookup func(context.Context, string) ([]net.IP, error)
		want   string
	}{
		{
			"an IPv4 answer becomes the endpoint",
			func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("162.159.192.1")}, nil
			},
			"162.159.192.1:2408",
		},
		{
			"no IPv4 answer keeps the name for the core to resolve",
			func(context.Context, string) ([]net.IP, error) { return nil, errors.New("no such host") },
			"engage.cloudflareclient.com:2408",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registrar, _ := registrarAnswer(t, http.StatusOK, named)
			registrar.Lookup = tc.lookup
			identity, err := registrar.Register(context.Background())
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			if identity.Endpoint != tc.want {
				t.Errorf("endpoint = %q, want %q", identity.Endpoint, tc.want)
			}
		})
	}

	// An endpoint that is already an address must not be looked up at all.
	t.Run("an address is left alone", func(t *testing.T) {
		t.Parallel()
		registrar, _ := registrarAnswer(t, http.StatusOK, registrationAnswer)
		registrar.Lookup = func(context.Context, string) ([]net.IP, error) {
			t.Error("the registrar looked up an endpoint that is already an address")
			return nil, errors.New("unexpected")
		}
		identity, err := registrar.Register(context.Background())
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		if identity.Endpoint != "162.159.192.1:2408" {
			t.Errorf("endpoint = %q", identity.Endpoint)
		}
	})
}

// A refusal must not echo the response body: a failed registration can carry a half-created device.
func TestRegistrarRefusesAnHttpErrorWithoutEchoingIt(t *testing.T) {
	t.Parallel()
	registrar, _ := registrarAnswer(t, http.StatusForbidden, `{"error":"blocked","token":"a-secret"}`)
	_, err := registrar.Register(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Register = %v, want an error naming the status", err)
	}
	if strings.Contains(err.Error(), "a-secret") || strings.Contains(err.Error(), "blocked") {
		t.Errorf("the error echoes the response body: %v", err)
	}
}

func TestRegistrarRefusesAnAnswerItCannotUse(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no peer":         `{"id":"x","config":{"interface":{"addresses":{"v4":"172.16.0.2"}}}}`,
		"no address":      `{"id":"x","config":{"peers":[{"public_key":"k","endpoint":{"host":"h:1"}}]}}`,
		"no id":           `{"config":{"peers":[{"public_key":"k","endpoint":{"host":"h:1"}}],"interface":{"addresses":{"v4":"172.16.0.2"}}}}`,
		"short client id": `{"id":"x","config":{"client_id":"AAA=","peers":[{"public_key":"k","endpoint":{"host":"h:1"}}],"interface":{"addresses":{"v4":"172.16.0.2"}}}}`,
		"not json":        `not a registration`,
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			registrar, _ := registrarAnswer(t, http.StatusOK, answer)
			if _, err := registrar.Register(context.Background()); err == nil {
				t.Fatalf("Register accepted %s", answer)
			}
		})
	}
}
