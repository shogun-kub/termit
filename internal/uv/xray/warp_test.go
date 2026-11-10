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
	// The order matters: the API rule first, then loopback (the canary), then the catch-all.
	if len(doc.Routing.Rules) != 3 {
		t.Fatalf("rules = %+v", doc.Routing.Rules)
	}
	if doc.Routing.Rules[1].OutboundTag != "direct" || len(doc.Routing.Rules[1].IP) == 0 {
		t.Errorf("the loopback rule is missing: %+v", doc.Routing.Rules[1])
	}
	last := doc.Routing.Rules[2]
	if last.BalancerTag != xray.WarpBalancerTag {
		t.Errorf("the catch-all rule does not use the balancer: %+v", last)
	}
	if len(last.Domain) != 0 || last.OutboundTag != "" {
		t.Errorf("all mode should not narrow the rule: %+v", last)
	}
}

func TestWarpSelectedNarrowsTheRule(t *testing.T) {
	t.Parallel()
	doc := warpGenerate(t, xray.WarpSpec{Mode: "selected", Selected: []string{"b.example.com", "a.example.com"}, FailOpen: true})
	if len(doc.Routing.Rules) != 3 {
		t.Fatalf("rules = %+v", doc.Routing.Rules)
	}
	last := doc.Routing.Rules[2]
	if strings.Join(last.Domain, ",") != "a.example.com,b.example.com" {
		t.Errorf("domains = %v, want a sorted list", last.Domain)
	}
	if last.BalancerTag != xray.WarpBalancerTag {
		t.Errorf("selected mode should still use the balancer: %+v", last)
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
	last := doc.Routing.Rules[len(doc.Routing.Rules)-1]
	if last.OutboundTag != xray.WarpTag || last.BalancerTag != "" {
		t.Errorf("fail-closed should route straight to WARP: %+v", last)
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
