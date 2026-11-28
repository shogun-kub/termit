package sub_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/sub"
	"github.com/shogun-kub/termit/internal/proto"
)

func peers() []sub.Peer {
	return []sub.Peer{
		{ID: "uv_2", Label: "beta", Domain: "two.example.com", UUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5", Path: "/b/"[:3] + "/"},
		{ID: "uv_1", Label: "alpha", Domain: "one.example.com", UUID: "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", Path: "/aaa/"},
	}
}

func decodeList(t *testing.T, body string) []string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("the list is not base64: %v", err)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestRenderListIsOrderedAndStable(t *testing.T) {
	t.Parallel()
	body, err := sub.RenderList(peers(), sub.Options{})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	lines := decodeList(t, body)
	if len(lines) != 2 {
		t.Fatalf("the list has %d lines, want 2", len(lines))
	}
	// Order is label, then id: alpha comes first even though beta was supplied first.
	if !strings.Contains(lines[0], "one.example.com") || !strings.Contains(lines[1], "two.example.com") {
		t.Errorf("the list is not ordered by label: %v", lines)
	}
	for _, line := range lines {
		profile, err := proto.ParseVLESSUrl(line)
		if err != nil {
			t.Fatalf("a rendered URI does not parse: %v", err)
		}
		if profile.Path == "" || profile.UUID == "" {
			t.Errorf("a rendered URI is incomplete: %s", line)
		}
	}

	// The same peers in another order produce the same bytes.
	reversed := []sub.Peer{peers()[1], peers()[0]}
	again, err := sub.RenderList(reversed, sub.Options{})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	if again != body {
		t.Error("the same peer set rendered differently after reordering")
	}
}

func TestRenderListMarksDegradedPeers(t *testing.T) {
	t.Parallel()
	list := peers()
	list[0].Degraded = true
	body, err := sub.RenderList(list, sub.Options{})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	for _, line := range decodeList(t, body) {
		if strings.Contains(line, "two.example.com") && !strings.Contains(line, "degraded") {
			t.Errorf("a degraded peer is not marked: %s", line)
		}
	}
}

func TestRenderListCarriesHappsFragmentParameter(t *testing.T) {
	t.Parallel()
	const param = "100-200,1-3,tlshello,6"
	body, err := sub.RenderList(peers(), sub.Options{FragmentParam: param})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	lines := decodeList(t, body)
	if len(lines) != 2 {
		t.Fatalf("the list has %d lines, want 2", len(lines))
	}
	for _, line := range lines {
		// It has to sit in the query and leave the label alone: Happ reads the parameter from the
		// link, everything else (including our own parser) must still see a valid URI.
		hash := strings.IndexByte(line, '#')
		if hash < 0 {
			t.Fatalf("a rendered URI lost its label: %s", line)
		}
		if !strings.Contains(line[:hash], "&fragment="+param) {
			t.Errorf("the fragmentation parameter is missing from %s", line)
		}
		if strings.Contains(line[hash:], "fragment") {
			t.Errorf("the parameter leaked into the label: %s", line)
		}
		if _, err := proto.ParseVLESSUrl(line); err != nil {
			t.Errorf("a URI with the parameter does not parse: %v", err)
		}
	}

	// Without it the links stay byte-identical, so an operator who never sets the option is not
	// silently changed by this feature.
	plain, err := sub.RenderList(peers(), sub.Options{})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	for _, line := range decodeList(t, plain) {
		if strings.Contains(line, "fragment=") {
			t.Errorf("a plain list carries the parameter: %s", line)
		}
	}
}

// TestRenderListCarriesPodkopsSwitches: podkop-engine and the patched sing-box builds take a link, not
// a profile, and read the two switches from the query string. The same subscription has to serve them
// and Happ, so the list carries both dialects.
func TestRenderListCarriesPodkopsSwitches(t *testing.T) {
	t.Parallel()
	const param = "100-200,1-3,tlshello,6"
	body, err := sub.RenderList(peers(), sub.Options{Fragment: true, RecordFragment: true, FragmentParam: param})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	for _, line := range decodeList(t, body) {
		hash := strings.IndexByte(line, '#')
		if hash < 0 {
			t.Fatalf("a rendered URI lost its label: %s", line)
		}
		for _, want := range []string{"&fragment=" + param, "sb.tls.fragment=1", "sb.tls.record_fragment=1"} {
			if !strings.Contains(line[:hash], want) {
				t.Errorf("%q is missing from %s", want, line)
			}
		}
		if _, err := proto.ParseVLESSUrl(line); err != nil {
			t.Errorf("a URI with the switches does not parse: %v", err)
		}
	}
}

func TestRenderListOfNothing(t *testing.T) {
	t.Parallel()
	body, err := sub.RenderList(nil, sub.Options{})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	if body != "" {
		t.Errorf("an empty peer set rendered %q", body)
	}
}

func TestRenderJSON(t *testing.T) {
	t.Parallel()
	body, err := sub.RenderJSON(peers(), sub.Options{SubscriptionURL: "https://mn.example.com/sub/token"})
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var profile struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     map[string]any   `json:"route"`
		Termit    string           `json:"termit_subscription_url"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatalf("the profile is not JSON: %v", err)
	}
	if profile.Termit != "https://mn.example.com/sub/token" {
		t.Errorf("subscription URL = %q", profile.Termit)
	}
	// Two peers, a selector and a direct outbound.
	if len(profile.Outbounds) != 4 {
		t.Fatalf("the profile has %d outbounds, want 4", len(profile.Outbounds))
	}
	if profile.Outbounds[0]["tag"] != "alpha" {
		t.Errorf("the first outbound is %v, want alpha", profile.Outbounds[0]["tag"])
	}
	// The client must send the profile's ClientHello, and must not offer h2: the node's nginx
	// terminates TLS and forwards HTTP/1.1, so an h2 offer only gets in the way.
	tlsSettings, ok := profile.Outbounds[0]["tls"].(map[string]any)
	if !ok {
		t.Fatalf("the first outbound has no tls settings: %v", profile.Outbounds[0])
	}
	if _, has := tlsSettings["alpn"]; has {
		t.Error("the tls settings carry alpn, which this transport cannot use")
	}
	utls, ok := tlsSettings["utls"].(map[string]any)
	if !ok {
		t.Fatalf("the tls settings carry no utls block: %v", tlsSettings)
	}
	if utls["enabled"] != true || utls["fingerprint"] != proto.Fingerprint {
		t.Errorf("utls = %v, want enabled with fingerprint %q", utls, proto.Fingerprint)
	}
	// The padding knob is Xray-only: sing-box validates strictly and would reject the field.
	if _, has := tlsSettings["extra"]; has {
		t.Error("the sing-box profile carries the Xray xhttp extra block")
	}
	if profile.Outbounds[2]["type"] != "selector" || profile.Outbounds[3]["tag"] != "direct" {
		t.Errorf("the profile does not end with a selector and a direct outbound: %v", profile.Outbounds)
	}
	// The route must keep private addresses off the mesh.
	rules, ok := profile.Route["rules"].([]any)
	if !ok || len(rules) == 0 {
		t.Fatalf("the route has no rules: %v", profile.Route)
	}
	if profile.Route["final"] != "proxy" {
		t.Errorf("route.final = %v, want proxy", profile.Route["final"])
	}
}

// TestRenderJSONCarriesTheFragmentationSwitches pins the only client-side defence the profile can
// offer: the fields appear when the operator asks for them and are absent otherwise, because a
// client that does not know them must not receive them.
func TestRenderJSONCarriesTheFragmentationSwitches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		opts           sub.Options
		wantFragment   bool
		wantRecordFrag bool
	}{
		{"off", sub.Options{}, false, false},
		{"plain fragment", sub.Options{Fragment: true}, true, false},
		{"record only", sub.Options{RecordFragment: true}, false, true},
		{"both", sub.Options{Fragment: true, RecordFragment: true}, true, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, err := sub.RenderJSON(peers(), tc.opts)
			if err != nil {
				t.Fatalf("RenderJSON: %v", err)
			}
			var profile struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if err := json.Unmarshal(body, &profile); err != nil {
				t.Fatalf("the profile is not JSON: %v", err)
			}
			tlsSettings, ok := profile.Outbounds[0]["tls"].(map[string]any)
			if !ok {
				t.Fatalf("the first outbound has no tls settings: %v", profile.Outbounds[0])
			}
			for key, want := range map[string]bool{
				"fragment":        tc.wantFragment,
				"record_fragment": tc.wantRecordFrag,
			} {
				got, has := tlsSettings[key]
				if !want {
					if has {
						t.Errorf("%s is present with %v, but the option is off", key, got)
					}
					continue
				}
				if got != true {
					t.Errorf("%s = %v, want true", key, got)
				}
			}
		})
	}
}

func TestETagChangesWithTheBody(t *testing.T) {
	t.Parallel()
	first := sub.ETag([]byte("a"))
	if first != sub.ETag([]byte("a")) {
		t.Error("the same body produced two tags")
	}
	if first == sub.ETag([]byte("b")) {
		t.Error("two different bodies share a tag")
	}
	if !strings.HasPrefix(first, `"`) || !strings.HasSuffix(first, `"`) {
		t.Errorf("the tag %q is not quoted the way HTTP wants it", first)
	}
}

// TestRenderListAddsAQUICLinkOnlyForANodeThatServesOne: a link to a transport the node does not run is
// worse than no link, so the second line appears only when the node reported that kind.
func TestRenderListAddsAQUICLinkOnlyForANodeThatServesOne(t *testing.T) {
	t.Parallel()
	nodes := peers()
	nodes[0].Transports = []string{proto.KindVLESS, proto.KindHysteria2}
	nodes[1].Transports = []string{proto.KindVLESS}
	body, err := sub.RenderList(nodes, sub.Options{})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	lines := decodeList(t, body)
	if len(lines) != 3 {
		t.Fatalf("the list has %d entries, want 3:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	want := proto.HysteriaUser(nodes[0].UUID) + ":" + proto.HysteriaPassword(nodes[0].UUID) + "@" + nodes[0].Domain + ":443"
	var found bool
	for _, line := range lines {
		if strings.HasPrefix(line, "hy2://"+want) {
			found = true
		}
		if strings.Contains(line, nodes[1].Domain) && strings.HasPrefix(line, "hy2://") {
			t.Errorf("a node that serves only the TLS profile got a QUIC link: %s", line)
		}
	}
	if !found {
		t.Errorf("no QUIC link for %s in:\n%s", nodes[0].Domain, strings.Join(lines, "\n"))
	}
}

// TestRenderJSONAddsAHysteria2Outbound: the sing-box profile carries the same second transport, with the
// password derived from the same pair credential the URI uses.
func TestRenderJSONAddsAHysteria2Outbound(t *testing.T) {
	t.Parallel()
	nodes := peers()
	nodes[0].Transports = []string{proto.KindVLESS, proto.KindHysteria2}
	body, err := sub.RenderJSON(nodes, sub.Options{})
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var profile struct {
		Outbounds []struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			Server     string `json:"server"`
			ServerPort int    `json:"server_port"`
			Password   string `json:"password"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var found bool
	for _, outbound := range profile.Outbounds {
		if outbound.Type != "hysteria2" {
			continue
		}
		if outbound.Server != nodes[0].Domain || outbound.ServerPort != 443 {
			t.Errorf("the QUIC outbound points at %s:%d", outbound.Server, outbound.ServerPort)
		}
		if outbound.Password != proto.HysteriaPassword(nodes[0].UUID) {
			t.Errorf("the password is not the pair's derivation")
		}
		found = true
	}
	if !found {
		t.Error("the profile has no hysteria2 outbound")
	}
}

// TestRenderJSONUsesTheDNSFormatSingBoxReads: sing-box 1.14 removed the legacy DNS server format and
// refuses the whole profile when it meets one, so the servers must be typed. Nothing in the unit tests
// would notice a regression here; a client would, with "decode config: legacy DNS server formats".
func TestRenderJSONUsesTheDNSFormatSingBoxReads(t *testing.T) {
	t.Parallel()
	body, err := sub.RenderJSON(peers(), sub.Options{})
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var profile struct {
		DNS struct {
			Servers []struct {
				Type   string `json:"type"`
				Tag    string `json:"tag"`
				Server string `json:"server"`
			} `json:"servers"`
			Final string `json:"final"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(profile.DNS.Servers) != 2 {
		t.Fatalf("the profile has %d DNS servers", len(profile.DNS.Servers))
	}
	for _, server := range profile.DNS.Servers {
		if server.Type == "" {
			t.Errorf("a DNS server has no type (legacy format): %+v", server)
		}
	}
	if profile.DNS.Final == "" {
		t.Error("the profile names no default DNS server")
	}
}

// A node that serves the profile a second way gets a second link: same credential, same host, the ws type
// and the derived path. A node that never reported one keeps its single link.
func TestTheSecondRenderingBecomesASecondLink(t *testing.T) {
	t.Parallel()
	one, err := proto.BuildVLESSUrl(proto.Profile{
		Domain: "one.example.com", UUID: "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
		Path: "/a1b2c3d4e5f60718/", Label: "alpha",
	})
	if err != nil {
		t.Fatalf("BuildVLESSUrl: %v", err)
	}
	if one == "" {
		t.Fatal("no baseline link")
	}
	body, err := sub.RenderList([]sub.Peer{{
		ID: "uv_one", Label: "alpha", Domain: "one.example.com",
		UUID:       "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
		Path:       "/a1b2c3d4e5f60718/",
		Transports: []string{proto.KindVLESS},
		Renderings: []string{proto.RenderingWebSocket},
	}}, sub.Options{})
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	lines := decodeList(t, body)
	if len(lines) != 2 {
		t.Fatalf("the list has %d lines, want 2: %v", len(lines), lines)
	}
	ws, err := proto.ParseVLESSUrl(lines[1])
	if err != nil {
		t.Fatalf("the ws URI does not parse: %v", err)
	}
	if ws.Rendering != proto.RenderingWebSocket {
		t.Errorf("the second link renders %q", ws.Rendering)
	}
	if want := proto.WebSocketPath("/a1b2c3d4e5f60718/"); ws.Path != want {
		t.Errorf("the second link's path is %q, want %q", ws.Path, want)
	}
	if ws.UUID != "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5" || ws.Domain != "one.example.com" {
		t.Errorf("the second link renames the node: %+v", ws)
	}
}
