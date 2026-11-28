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
			profile: proto.Profile{Domain: "node.example.com", UUID: testUUID, Path: "/a1b2c3d4e5f60718/"},
		},
		{
			name:    "with a label",
			profile: proto.Profile{Domain: "node.example.com", UUID: testUUID, Path: "/x/", Label: "home router"},
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
		Domain: "node.example.com",
		UUID:   testUUID,
		Path:   "/a1b2c3d4e5f60718/",
		Label:  "laptop",
	})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	for _, want := range []string{
		"vless://" + testUUID + "@node.example.com:443",
		"encryption=none",
		"security=tls",
		"sni=node.example.com",
		"type=xhttp",
		"mode=packet-up",
		"path=%2Fa1b2c3d4e5f60718%2F",
		"fp=firefox",
		"extra=",
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
	var extra map[string]any
	if err := json.Unmarshal([]byte(parsed.Query().Get("extra")), &extra); err != nil {
		t.Fatalf("extra is not a JSON object a client can read: %v", err)
	}
	if extra["xPaddingBytes"] == nil {
		t.Errorf("extra carries no padding: %v", extra)
	}
	xmux, ok := extra["xmux"].(map[string]any)
	if !ok {
		t.Fatalf("extra carries no xmux block: %v", extra)
	}
	if xmux["maxConcurrency"] == nil || xmux["hMaxReusableSecs"] == nil {
		t.Errorf("xmux is missing the reuse limits: %v", xmux)
	}
}

func TestParseVLESSUrlRejects(t *testing.T) {
	t.Parallel()
	valid := "vless://" + testUUID + "@node.example.com:443?encryption=none&security=tls&type=xhttp&path=%2Fp%2F"
	cases := []struct{ name, uri, want string }{
		{"scheme", "trojan://" + testUUID + "@node.example.com:443", "scheme"},
		{"no user", "vless://node.example.com:443", "no user id"},
		{"no port", "vless://" + testUUID + "@node.example.com?security=tls&type=xhttp", "no port"},
		{"wrong port", "vless://" + testUUID + "@node.example.com:8443?security=tls&type=xhttp", "port"},
		{"allowInsecure", valid + "&allowInsecure=1", "allowInsecure"},
		{"insecure", valid + "&insecure=1", "allowInsecure"},
		{"plaintext", "vless://" + testUUID + "@node.example.com:443?security=none&type=xhttp", "security"},
		{"bad uuid", "vless://not-a-uuid@node.example.com:443?security=tls&type=xhttp", "uuid"},
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

func TestHappFragmentIsValidatedAndAppended(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"100-200,1-3,tlshello,6", "300-500,1-5,tlshello", "1-3,1-3,1-2"} {
		if !proto.ValidHappFragment(value) {
			t.Errorf("ValidHappFragment rejected %q", value)
		}
	}
	for _, value := range []string{"", "tlshello", "100-200&fragment=1", "1-2,3-4,tlshello#x", "1-2,3-4,5-6,7,8"} {
		if proto.ValidHappFragment(value) {
			t.Errorf("ValidHappFragment accepted %q", value)
		}
	}

	uri, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: "node.example.com",
		UUID:   "11111111-2222-4333-8444-555555555555",
		Path:   "/0123456789abcdef/",
		Label:  "node.example.com",
	})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	withFragment := proto.AddHappFragment(uri, "100-200,1-3,tlshello,6")
	if !strings.Contains(withFragment, "&fragment=100-200,1-3,tlshello,6") {
		t.Errorf("the parameter is not in the URI: %s", withFragment)
	}
	if !strings.HasSuffix(withFragment, "#node.example.com") {
		t.Errorf("the label moved: %s", withFragment)
	}
	if _, err := proto.ParseVLESSUrl(withFragment); err != nil {
		t.Errorf("a URI with the parameter does not parse: %v", err)
	}
	if got := proto.AddHappFragment(uri, ""); got != uri {
		t.Errorf("an empty value changed the URI: %s", got)
	}
}

// TestAddSingBoxFragmentSetsPodkopsSwitches: the same two settings the profile carries as tls.fragment
// and tls.record_fragment, spelled the way a client that reads a link understands them.
func TestAddSingBoxFragmentSetsPodkopsSwitches(t *testing.T) {
	t.Parallel()
	uri, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: "node.example.com",
		UUID:   "11111111-2222-4333-8444-555555555555",
		Path:   "/0123456789abcdef/",
		Label:  "node.example.com",
	})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}

	both := proto.AddSingBoxFragment(uri, true, true)
	if !strings.Contains(both, "sb.tls.fragment=1") || !strings.Contains(both, "sb.tls.record_fragment=1") {
		t.Errorf("the switches are not in the URI: %s", both)
	}
	if !strings.HasSuffix(both, "#node.example.com") {
		t.Errorf("the label moved: %s", both)
	}
	if _, err := proto.ParseVLESSUrl(both); err != nil {
		t.Errorf("a URI with the switches does not parse: %v", err)
	}

	segmentOnly := proto.AddSingBoxFragment(uri, true, false)
	if !strings.Contains(segmentOnly, "sb.tls.fragment=1") || strings.Contains(segmentOnly, "sb.tls.record_fragment") {
		t.Errorf("segment-only is wrong: %s", segmentOnly)
	}
	if got := proto.AddSingBoxFragment(uri, false, false); got != uri {
		t.Errorf("with both off the URI changed: %s", got)
	}
}

// The WebSocket rendering is the same profile spoken differently: same name, same device, same path
// shape, and a URI a stock sing-box can read.
func TestTheWebSocketRenderingRoundTrips(t *testing.T) {
	t.Parallel()
	want := proto.Profile{
		Domain:    "node.example.com",
		UUID:      testUUID,
		Path:      "/a1b2c3d4e5f60718/",
		Rendering: proto.RenderingWebSocket,
	}
	uri, err := proto.BuildVLESSUrl(want)
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	for _, wantPart := range []string{
		"vless://" + testUUID + "@node.example.com:443",
		"security=tls",
		"sni=node.example.com",
		"type=ws",
		"host=node.example.com",
		"path=%2Fa1b2c3d4e5f60718%2F",
		"fp=firefox",
		"#ws",
	} {
		if !strings.Contains(uri, wantPart) {
			t.Errorf("the URI does not contain %q: %s", wantPart, uri)
		}
	}
	// An xhttp-only query is not a WebSocket URI: mode and extra mean the client will speak XHTTP.
	for _, forbidden := range []string{"mode=", "extra="} {
		if strings.Contains(uri, forbidden) {
			t.Errorf("the WebSocket URI carries %q: %s", forbidden, uri)
		}
	}

	got, err := proto.ParseVLESSUrl(uri)
	if err != nil {
		t.Fatalf("ParseVLESSUrl(%q): %v", uri, err)
	}
	// An unlabelled profile is named after its rendering, as the xhttp one is named after the network.
	want.Label = proto.NetworkWS
	if got != want {
		t.Errorf("round trip changed the profile:\n got %+v\nwant %+v", got, want)
	}
}

// A URI that mixes the two renderings is refused rather than guessed: a client cannot speak both.
func TestAURICannotMixTheRenderings(t *testing.T) {
	t.Parallel()
	base := "vless://" + testUUID + "@node.example.com:443?security=tls"
	for _, raw := range []string{
		base + "&type=ws&mode=packet-up",
		base + "&type=ws&host=other.example.com",
		base + "&type=" + proto.RenderingWebSocket,
	} {
		if _, err := proto.ParseVLESSUrl(raw); err == nil {
			t.Errorf("ParseVLESSUrl accepted %q", raw)
		}
	}
}

func TestTheDefaultRenderingIsXHTTP(t *testing.T) {
	t.Parallel()
	if got := (proto.Profile{}).RenderingOrDefault(); got != proto.RenderingXHTTP {
		t.Errorf("the default rendering is %q", got)
	}
	uri, err := proto.BuildVLESSUrl(proto.Profile{Domain: "node.example.com", UUID: testUUID, Path: "/p/"})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	parsed, err := proto.ParseVLESSUrl(uri)
	if err != nil {
		t.Fatalf("ParseVLESSUrl: %v", err)
	}
	if parsed.Rendering != "" {
		t.Errorf("an xhttp URI parsed as the rendering %q", parsed.Rendering)
	}
}

func TestAnUnknownRenderingIsRefused(t *testing.T) {
	t.Parallel()
	profile := proto.Profile{Domain: "node.example.com", UUID: testUUID, Path: "/p/", Rendering: "grpc"}
	if err := profile.Validate(); err == nil {
		t.Error("Validate accepted a rendering this build does not write")
	}
	if _, err := proto.BuildVLESSUrl(profile); err == nil {
		t.Error("BuildVLESSUrl accepted a rendering this build does not write")
	}
}

// The second rendering's path is derived, not stored: the same node always computes the same one, and it
// is unguessable without the original path.
func TestTheWebSocketPathIsDerivedFromTheProfilePath(t *testing.T) {
	t.Parallel()
	secret := "/a1b2c3d4e5f60718/"
	first := proto.WebSocketPath(secret)
	second := proto.WebSocketPath(secret)
	if first != second {
		t.Errorf("the derived path is not stable: %q then %q", first, second)
	}
	if first == secret {
		t.Error("the second rendering reuses the first one's path")
	}
	if !strings.HasPrefix(first, "/ws/") || !strings.HasSuffix(first, "/") {
		t.Errorf("derived path %q does not look like a path", first)
	}
	if other := proto.WebSocketPath("/fedcba9876543210/"); other == first {
		t.Error("two nodes derive the same websocket path")
	}
	if body := strings.TrimSuffix(strings.TrimPrefix(first, "/ws/"), "/"); len(body) != 16 {
		t.Errorf("the derived path carries %d characters of secret, want 16", len(body))
	}
}
