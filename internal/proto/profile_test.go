package proto_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
)

const testUUID = "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5"

func TestProfileRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		profile proto.Profile
	}{
		{
			name:    "minimal",
			profile: proto.Profile{Domain: "vpn.example.com", UUID: testUUID, Path: "/a1b2c3d4e5f60718/"},
		},
		{
			name:    "with a label",
			profile: proto.Profile{Domain: "vpn.example.com", UUID: testUUID, Path: "/x/", Label: "home router"},
		},
		{
			name:    "punycode and a two-letter tld",
			profile: proto.Profile{Domain: "node1.example.io", UUID: testUUID, Path: "/p/"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uri, err := proto.BuildVLESSUrl(tc.profile)
			if err != nil {
				t.Fatalf("BuildVLESSUrl: %v", err)
			}
			got, err := proto.ParseVLESSUrl(uri)
			if err != nil {
				t.Fatalf("ParseVLESSUrl(%q): %v", uri, err)
			}
			// The builder names an unlabelled profile after the network, so that is the only field the
			// round trip is allowed to fill in.
			want := tc.profile
			if want.Label == "" {
				want.Label = proto.Network
			}
			if got != want {
				t.Errorf("round trip changed the profile:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestBuildVLESSUrlShape(t *testing.T) {
	t.Parallel()
	uri, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: "vpn.example.com",
		UUID:   testUUID,
		Path:   "/a1b2c3d4e5f60718/",
		Label:  "laptop",
	})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	for _, want := range []string{
		"vless://" + testUUID + "@vpn.example.com:443",
		"encryption=none",
		"security=tls",
		"sni=vpn.example.com",
		"type=xhttp",
		"mode=packet-up",
		"path=%2Fa1b2c3d4e5f60718%2F",
		"fp=firefox",
		"extra=%7B%22xPaddingBytes%22%3A%22100-1000%22%7D",
		"#laptop",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("uri %q is missing %q", uri, want)
		}
	}
	for _, forbidden := range []string{"allowInsecure", "insecure", "flow="} {
		if strings.Contains(uri, forbidden) {
			t.Errorf("uri %q contains %q, which the profile does not use", uri, forbidden)
		}
	}
	// A client parses extra as JSON after URL-decoding the query, so the round trip through
	// url.Query must give back an object, not a broken string.
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("the built URI does not parse: %v", err)
	}
	var extra map[string]string
	if err := json.Unmarshal([]byte(parsed.Query().Get("extra")), &extra); err != nil {
		t.Fatalf("extra is not a JSON object a client can read: %v", err)
	}
	if extra["xPaddingBytes"] == "" {
		t.Errorf("extra carries no padding: %v", extra)
	}
}

func TestParseVLESSUrlRejects(t *testing.T) {
	t.Parallel()
	valid := "vless://" + testUUID + "@vpn.example.com:443?encryption=none&security=tls&type=xhttp&path=%2Fp%2F"
	cases := []struct{ name, uri, want string }{
		{"scheme", "trojan://" + testUUID + "@vpn.example.com:443", "scheme"},
		{"no user", "vless://vpn.example.com:443", "no user id"},
		{"no port", "vless://" + testUUID + "@vpn.example.com?security=tls&type=xhttp", "no port"},
		{"wrong port", "vless://" + testUUID + "@vpn.example.com:8443?security=tls&type=xhttp", "port"},
		{"allowInsecure", valid + "&allowInsecure=1", "allowInsecure"},
		{"insecure", valid + "&insecure=1", "allowInsecure"},
		{"plaintext", "vless://" + testUUID + "@vpn.example.com:443?security=none&type=xhttp", "security"},
		{"ws transport", "vless://" + testUUID + "@vpn.example.com:443?security=tls&type=ws", "type"},
		{"bad uuid", "vless://not-a-uuid@vpn.example.com:443?security=tls&type=xhttp", "uuid"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := proto.ParseVLESSUrl(tc.uri)
			if err == nil {
				t.Fatalf("ParseVLESSUrl(%q) accepted a URI it must refuse", tc.uri)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateCollectsProblems(t *testing.T) {
	t.Parallel()
	err := proto.Profile{Domain: "not a domain", UUID: "x", Path: "no-slashes"}.Validate()
	if err == nil {
		t.Fatal("Validate accepted an invalid profile")
	}
	for _, want := range []string{"domain", "uuid", "path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
