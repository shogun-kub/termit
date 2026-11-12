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
	body, err := sub.RenderList(peers())
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
	again, err := sub.RenderList(reversed)
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
	body, err := sub.RenderList(list)
	if err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	for _, line := range decodeList(t, body) {
		if strings.Contains(line, "two.example.com") && !strings.Contains(line, "degraded") {
			t.Errorf("a degraded peer is not marked: %s", line)
		}
	}
}

func TestRenderListOfNothing(t *testing.T) {
	t.Parallel()
	body, err := sub.RenderList(nil)
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
