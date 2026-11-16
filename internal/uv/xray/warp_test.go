package xray_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/xray"
)

// warpDoc is the part of the generated document the WARP tests look at.
type warpDoc struct {
	Outbounds []struct {
		Tag      string `json:"tag"`
		Protocol string `json:"protocol"`
		Settings struct {
			Servers []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
			} `json:"servers"`
			SecretKey   string   `json:"secretKey"`
			Address     []string `json:"address"`
			MTU         int      `json:"mtu"`
			NoKernelTun bool     `json:"noKernelTun"`
			Peers       []struct {
				PublicKey  string   `json:"publicKey"`
				Endpoint   string   `json:"endpoint"`
				AllowedIPs []string `json:"allowedIPs"`
				Reserved   []int    `json:"reserved"`
			} `json:"peers"`
		} `json:"settings"`
	} `json:"outbounds"`
	Routing struct {
		Rules []struct {
			Type        string   `json:"type"`
			InboundTag  []string `json:"inboundTag"`
			Domain      []string `json:"domain"`
			IP          []string `json:"ip"`
			Network     string   `json:"network"`
			OutboundTag string   `json:"outboundTag"`
			BalancerTag string   `json:"balancerTag"`
		} `json:"rules"`
		Balancers []struct {
			Tag         string   `json:"tag"`
			Selector    []string `json:"selector"`
			FallbackTag string   `json:"fallbackTag"`
			Strategy    struct {
				Type string `json:"type"`
			} `json:"strategy"`
		} `json:"balancers"`
	} `json:"routing"`
	Observatory *struct {
		SubjectSelector []string `json:"subjectSelector"`
		ProbeURL        string   `json:"probeUrl"`
		ProbeInterval   string   `json:"probeInterval"`
	} `json:"observatory"`
}

func warpGenerate(t *testing.T, warp xray.WarpSpec) warpDoc {
	t.Helper()
	spec := testSpec()
	spec.Warp = warp
	data, err := xray.Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var doc warpDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("generated config is not JSON: %v", err)
	}
	return doc
}

func findOutbound(t *testing.T, doc warpDoc, tag string) (protocol, address string, port int) {
	t.Helper()
	for _, out := range doc.Outbounds {
		if out.Tag != tag {
			continue
		}
		if len(out.Settings.Servers) == 0 {
			return out.Protocol, "", 0
		}
		return out.Protocol, out.Settings.Servers[0].Address, out.Settings.Servers[0].Port
	}
	t.Fatalf("no outbound %q in %+v", tag, doc.Outbounds)
	return "", "", 0
}

func TestWarpOffAddsNothing(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{})
	for _, out := range doc.Outbounds {
		if out.Tag == xray.WarpTag {
			t.Fatal("WARP is off but the config has a WARP outbound")
		}
	}
	if doc.Observatory != nil {
		t.Error("WARP is off but the config has an observatory")
	}
	if len(doc.Routing.Balancers) != 0 {
		t.Error("WARP is off but the config has a balancer")
	}
	if len(doc.Routing.Rules) != 1 {
		t.Errorf("rules = %+v, want only the API rule", doc.Routing.Rules)
	}
}

// Fail-open is the default: while WARP answers, traffic uses it; when it stops answering, the
// balancer moves to direct and the node keeps carrying traffic.
func TestWarpAllFailOpen(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{Mode: "all", FailOpen: true})
	protocol, address, port := findOutbound(t, doc, xray.WarpTag)
	if protocol != "socks" || address != "127.0.0.1" || port != 40000 {
		t.Fatalf("warp outbound = %s %s:%d, want socks 127.0.0.1:40000", protocol, address, port)
	}
	if doc.Observatory == nil {
		t.Fatal("fail-open needs an observatory")
	}
	if doc.Observatory.ProbeURL != xray.WarpProbeURL || doc.Observatory.ProbeInterval != xray.WarpProbeInterval {
		t.Errorf("observatory = %+v", doc.Observatory)
	}
	if len(doc.Observatory.SubjectSelector) != 1 || doc.Observatory.SubjectSelector[0] != xray.WarpTag {
		t.Errorf("observatory subjects = %v", doc.Observatory.SubjectSelector)
	}
	if len(doc.Routing.Balancers) != 1 {
		t.Fatalf("balancers = %+v", doc.Routing.Balancers)
	}
	balancer := doc.Routing.Balancers[0]
	if balancer.Tag != xray.WarpBalancerTag || balancer.FallbackTag != "direct" || balancer.Strategy.Type != "leastPing" {
		t.Errorf("balancer = %+v", balancer)
	}
	if len(balancer.Selector) != 1 || balancer.Selector[0] != xray.WarpTag {
		t.Errorf("balancer selector = %v", balancer.Selector)
	}
	// The order matters: the API rule first, then loopback (the canary), then the catch-all — which is
	// two rules, because UDP cannot go through a SOCKS proxy that has no UDP ASSOCIATE.
	if len(doc.Routing.Rules) != 4 {
		t.Fatalf("rules = %+v", doc.Routing.Rules)
	}
	if doc.Routing.Rules[1].OutboundTag != "direct" || len(doc.Routing.Rules[1].IP) == 0 {
		t.Errorf("the loopback rule is missing: %+v", doc.Routing.Rules[1])
	}
	proxied := doc.Routing.Rules[2]
	if proxied.BalancerTag != xray.WarpBalancerTag || proxied.Network != "tcp" {
		t.Errorf("the TCP catch-all does not use the balancer: %+v", proxied)
	}
	if len(proxied.Domain) != 0 || proxied.OutboundTag != "" {
		t.Errorf("all mode should not narrow the rule: %+v", proxied)
	}
	if udp := doc.Routing.Rules[3]; udp.Network != "udp" || udp.OutboundTag != "direct" {
		t.Errorf("UDP does not bypass the proxy: %+v", udp)
	}
}

// testWarpIdentity is a shape Cloudflare's registration produces: a private key, the tunnel addresses,
// the peer, and the three reserved bytes that ride in the WireGuard header.
func testWarpIdentity() *xray.WireGuardIdentity {
	return &xray.WireGuardIdentity{
		SecretKey: "cHJpdmF0ZS1rZXktZm9yLXRlc3RzLW9ubHk=",
		Addresses: []string{"172.16.0.2/32", "2606:4700:110:8d9c::2/128"},
		PublicKey: "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
		Endpoint:  "162.159.192.1:2408",
		Reserved:  []int{240, 25, 146},
		MTU:       1280,
	}
}

// The whole point of the transport: one rule, both protocols, the tunnel inside the core. A UDP flow
// cannot be sent into a SOCKS proxy, so there is nothing to split.
func TestWarpWireGuardPutsBothProtocolsThroughOneRule(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{
		Mode: "all", FailOpen: true,
		Transport: xray.WarpTransportWireGuard, WireGuard: testWarpIdentity(),
	})

	var found bool
	for _, out := range doc.Outbounds {
		if out.Tag != xray.WarpTag {
			continue
		}
		found = true
		if out.Protocol != "wireguard" {
			t.Fatalf("warp outbound protocol = %q, want wireguard", out.Protocol)
		}
		if !out.Settings.NoKernelTun {
			t.Error("noKernelTun is off, so the core would create a TUN device and touch host routing")
		}
		if out.Settings.MTU != 1280 {
			t.Errorf("mtu = %d, want the configured 1280", out.Settings.MTU)
		}
		if len(out.Settings.Address) != 2 {
			t.Errorf("addresses = %v, want the v4 and v6 tunnel addresses", out.Settings.Address)
		}
		if len(out.Settings.Peers) != 1 {
			t.Fatalf("peers = %+v, want one", out.Settings.Peers)
		}
		peer := out.Settings.Peers[0]
		if peer.Endpoint != "162.159.192.1:2408" || peer.PublicKey == "" {
			t.Errorf("peer = %+v", peer)
		}
		if len(peer.Reserved) != 3 {
			t.Errorf("reserved = %v, want the three bytes from the registration", peer.Reserved)
		}
		if len(peer.AllowedIPs) != 2 {
			t.Errorf("allowedIPs = %v, want everything", peer.AllowedIPs)
		}
	}
	if !found {
		t.Fatalf("no outbound %q in %+v", xray.WarpTag, doc.Outbounds)
	}

	// Rules: the API one, the loopback one, and a single catch-all that names both protocols.
	if len(doc.Routing.Rules) != 3 {
		t.Fatalf("rules = %+v", doc.Routing.Rules)
	}
	last := doc.Routing.Rules[2]
	if last.Network != "tcp,udp" || last.BalancerTag != xray.WarpBalancerTag {
		t.Errorf("the tunnel rule does not carry both protocols through the balancer: %+v", last)
	}
	if len(last.Domain) != 0 {
		t.Errorf("all mode should not narrow the rule: %+v", last)
	}
}

func TestWarpWireGuardSelectedNarrowsTheTunnel(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{
		Mode: "selected", Selected: []string{"b.example.com", "a.example.com"}, FailOpen: true,
		Transport: xray.WarpTransportWireGuard, WireGuard: testWarpIdentity(),
	})
	if len(doc.Routing.Rules) != 3 {
		t.Fatalf("rules = %+v", doc.Routing.Rules)
	}
	last := doc.Routing.Rules[2]
	if strings.Join(last.Domain, ",") != "a.example.com,b.example.com" {
		t.Errorf("domains = %v, want a sorted list", last.Domain)
	}
	if last.Network != "tcp,udp" {
		t.Errorf("a selected tunnel rule must still cover UDP: %+v", last)
	}
}

func TestWarpWireGuardRefusesAnIncompleteIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*xray.WireGuardIdentity)
		want   string
	}{
		{"no identity at all", nil, "needs an identity"},
		{"no secret key", func(id *xray.WireGuardIdentity) { id.SecretKey = "" }, "no secret key"},
		{"no peer key", func(id *xray.WireGuardIdentity) { id.PublicKey = "" }, "no peer public key"},
		{"no endpoint", func(id *xray.WireGuardIdentity) { id.Endpoint = "" }, "no endpoint"},
		{"no address", func(id *xray.WireGuardIdentity) { id.Addresses = nil }, "no tunnel address"},
		{"two reserved bytes", func(id *xray.WireGuardIdentity) { id.Reserved = []int{1, 2} }, "want 3"},
		{"an mtu out of range", func(id *xray.WireGuardIdentity) { id.MTU = 9000 }, "out of range"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := testSpec()
			spec.Warp = xray.WarpSpec{Mode: "all", Transport: xray.WarpTransportWireGuard}
			if tc.mutate != nil {
				identity := testWarpIdentity()
				tc.mutate(identity)
				spec.Warp.WireGuard = identity
			}
			_, err := xray.Generate(spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestWarpRefusesAnUnknownTransport(t *testing.T) {
	t.Parallel()
	spec := testSpec()
	spec.Warp = xray.WarpSpec{Mode: "all", Transport: "masque"}
	_, err := xray.Generate(spec)
	if err == nil || !strings.Contains(err.Error(), "must be proxy or wireguard") {
		t.Fatalf("Generate = %v, want a refusal naming the two transports", err)
	}
}

func TestWarpSelectedNarrowsTheRule(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{Mode: "selected", Selected: []string{"b.example.com", "a.example.com"}, FailOpen: true})
	if len(doc.Routing.Rules) != 4 {
		t.Fatalf("rules = %+v", doc.Routing.Rules)
	}
	proxied := doc.Routing.Rules[2]
	if strings.Join(proxied.Domain, ",") != "a.example.com,b.example.com" {
		t.Errorf("domains = %v, want a sorted list", proxied.Domain)
	}
	if proxied.BalancerTag != xray.WarpBalancerTag || proxied.Network != "tcp" {
		t.Errorf("selected mode should still use the balancer for TCP: %+v", proxied)
	}
	// The same domains, but UDP goes direct: the narrower the set, the more surprising a silent black
	// hole would be.
	if udp := doc.Routing.Rules[3]; udp.Network != "udp" || udp.OutboundTag != "direct" ||
		strings.Join(udp.Domain, ",") != "a.example.com,b.example.com" {
		t.Errorf("UDP does not bypass the proxy for the selected domains: %+v", udp)
	}
}

// Fail-closed is the opposite trade: if WARP stops answering, the node stops carrying traffic rather
// than revealing its own address.
func TestWarpFailClosedHasNoFallback(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{Mode: "all", FailOpen: false})
	if doc.Observatory != nil {
		t.Error("fail-closed has no reason to probe")
	}
	if len(doc.Routing.Balancers) != 0 {
		t.Error("fail-closed has no reason for a balancer")
	}
	proxied := doc.Routing.Rules[len(doc.Routing.Rules)-2]
	if proxied.OutboundTag != xray.WarpTag || proxied.BalancerTag != "" || proxied.Network != "tcp" {
		t.Errorf("fail-closed should route TCP straight to WARP: %+v", proxied)
	}
	if udp := doc.Routing.Rules[len(doc.Routing.Rules)-1]; udp.Network != "udp" || udp.OutboundTag != "direct" {
		t.Errorf("UDP does not bypass the proxy: %+v", udp)
	}
}

func TestWarpUsesTheConfiguredSocksAddress(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{Mode: "all", Socks: "127.0.0.1:41000"})
	_, address, port := findOutbound(t, doc, xray.WarpTag)
	if address != "127.0.0.1" || port != 41000 {
		t.Errorf("warp outbound = %s:%d, want the configured address", address, port)
	}
}

func TestWarpRefusesBrokenSpecs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		warp xray.WarpSpec
		want string
	}{
		{"an unknown mode", xray.WarpSpec{Mode: "sometimes"}, "must be off, all or selected"},
		{"selected without domains", xray.WarpSpec{Mode: "selected"}, "at least one domain"},
		{"a domain with a slash", xray.WarpSpec{Mode: "selected", Selected: []string{"a.example.com/x"}}, "is not a domain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := testSpec()
			spec.Warp = tc.warp
			_, err := xray.Generate(spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
