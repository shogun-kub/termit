package warp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

// DefaultRegistrationURL is where the WARP client creates a device. The version in the path is
// Cloudflare's, not ours: the official client sends it, the API accepts it, and a refusal is reported
// as a refusal rather than worked around by guessing a newer one.
const DefaultRegistrationURL = "https://api.cloudflareclient.com/v0a2158"

// clientVersion and userAgent are the headers the official WARP client sends. They are part of the
// protocol as much as the body is: the API has refused requests it could not identify as a client.
const (
	warpClientVersion = "a-6.11-2223"
	warpUserAgent     = "okhttp/3.12.1"
	// warpTOS is the terms-of-service timestamp the client reports. It is a date, not a signature.
	warpTOS = "2020-08-04T00:00:00.000+02:00"
)

// Registrar creates a WARP identity by speaking Cloudflare's registration API. It is a value, not a
// singleton: the tests point it at a local server and the node points it at Cloudflare.
type Registrar struct {
	// BaseURL overrides the registration endpoint; empty means DefaultRegistrationURL.
	BaseURL string
	// Client overrides the HTTP client; the zero value gets one with a timeout.
	Client *http.Client
	// Now is the clock for the identity's timestamp; tests set it.
	Now func() time.Time
}

// Register creates a device on Cloudflare's side and returns the identity the core needs. It never
// returns a key it did not just generate, and it never logs one.
func (r Registrar) Register(ctx context.Context) (*Identity, error) {
	privateKey, publicKey, err := generateKeyPair()
	if err != nil {
		return nil, err
	}
	request := registrationRequest{
		Key:     base64.StdEncoding.EncodeToString(publicKey),
		Install: "", FCMToken: "",
		TOS: warpTOS, Model: "PC", Type: "Linux", Locale: "en_US",
		WarpEnabled: true,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("warp: encode the registration request: %w", err)
	}
	url := strings.TrimSuffix(r.baseURL(), "/") + "/reg"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("warp: build the registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", warpUserAgent)
	req.Header.Set("CF-Client-Version", warpClientVersion)

	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("warp: register: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status, never the body: a failed registration can come back with the account it did
		// create, and that is a credential.
		return nil, fmt.Errorf("warp: register: the API answered %s", resp.Status)
	}
	// 64 KiB is far more than a registration response and bounds what a hostile or broken endpoint can
	// make the node read.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("warp: read the registration response: %w", err)
	}
	var answer registrationResponse
	if err := json.Unmarshal(payload, &answer); err != nil {
		return nil, fmt.Errorf("warp: decode the registration response: %w", err)
	}
	identity, err := answer.identity(privateKey, publicKey)
	if err != nil {
		return nil, err
	}
	if r.Now != nil {
		identity.RegisteredAt = r.Now().UTC()
	} else {
		identity.RegisteredAt = time.Now().UTC()
	}
	return identity, nil
}

func (r Registrar) baseURL() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return DefaultRegistrationURL
}

// generateKeyPair makes a WireGuard key pair: 32 random bytes, clamped the way the protocol requires,
// with the public half derived on Curve25519's base point.
func generateKeyPair() (privateKey, publicKey []byte, err error) {
	privateKey = make([]byte, 32)
	if _, err := rand.Read(privateKey); err != nil {
		return nil, nil, fmt.Errorf("warp: generate a key: %w", err)
	}
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64
	publicKey, err = curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		return nil, nil, fmt.Errorf("warp: derive the public key: %w", err)
	}
	return privateKey, publicKey, nil
}

// registrationRequest is the body the client sends. The empty install and FCM fields are what the
// desktop client sends when it has no push channel; the API accepts them.
type registrationRequest struct {
	Key         string `json:"key"`
	Install     string `json:"install_id"`
	FCMToken    string `json:"fcm_token"`
	TOS         string `json:"tos"`
	Model       string `json:"model"`
	Type        string `json:"type"`
	Locale      string `json:"locale"`
	WarpEnabled bool   `json:"warp_enabled"`
}

// registrationResponse is the part of the answer this node uses. The rest — token, account, license —
// is not needed for a plain WARP tunnel and is deliberately not kept.
type registrationResponse struct {
	ID     string `json:"id"`
	Config struct {
		ClientID string `json:"client_id"`
		Peers    []struct {
			PublicKey string `json:"public_key"`
			Endpoint  struct {
				Host string `json:"host"`
			} `json:"endpoint"`
		} `json:"peers"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
}

// identity turns an answer into the file the node keeps. Every field it refuses to invent comes from
// the response: a missing peer or address is a registration this code cannot use, not something to
// fill in with a guess.
func (a registrationResponse) identity(privateKey, publicKey []byte) (*Identity, error) {
	var problems []error
	if a.ID == "" {
		problems = append(problems, errors.New("the registration has no id"))
	}
	if len(a.Config.Peers) == 0 {
		problems = append(problems, errors.New("the registration has no peer"))
	}
	if a.Config.Interface.Addresses.V4 == "" && a.Config.Interface.Addresses.V6 == "" {
		problems = append(problems, errors.New("the registration has no tunnel address"))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("warp: register: %w", errors.Join(problems...))
	}

	identity := &Identity{
		PrivateKey:    base64.StdEncoding.EncodeToString(privateKey),
		PublicKey:     base64.StdEncoding.EncodeToString(publicKey),
		PeerPublicKey: a.Config.Peers[0].PublicKey,
		Endpoint:      a.Config.Peers[0].Endpoint.Host,
		DeviceID:      a.ID,
	}
	for _, address := range []string{a.Config.Interface.Addresses.V4, a.Config.Interface.Addresses.V6} {
		if address != "" {
			identity.Addresses = append(identity.Addresses, withPrefix(address))
		}
	}
	// The client id is the three bytes Cloudflare wants in the WireGuard header. It is base64 in the
	// response and must stay exactly three bytes long.
	if a.Config.ClientID != "" {
		reserved, err := base64.StdEncoding.DecodeString(a.Config.ClientID)
		if err != nil {
			return nil, fmt.Errorf("warp: register: the client id is not base64: %w", err)
		}
		if len(reserved) != 3 {
			return nil, fmt.Errorf("warp: register: the client id is %d bytes, want 3", len(reserved))
		}
		identity.Reserved = reserved
	}
	return identity, nil
}

// withPrefix adds the /32 or /128 a bare address from the API is missing, because the core wants an
// address with a prefix length.
func withPrefix(address string) string {
	if strings.Contains(address, "/") {
		return address
	}
	if strings.Contains(address, ":") {
		return address + "/128"
	}
	return address + "/32"
}
