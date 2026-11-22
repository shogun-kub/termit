package xray_test

import (
	"encoding/json"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/xray"
)

// The MASQUE sidecar carries UDP, so its document must not keep proxy mode's split. With the split, a
// client's first flow — DNS — leaves the node on the node's own address, which is the identifier WARP
// exists to hide.
func TestUsqueTransportCarriesUDPThroughWarp(t *testing.T) {
	t.Parallel()
	spec := testSpec()
	spec.Warp = xray.WarpSpec{Mode: "all", Transport: xray.WarpTransportUsque, FailOpen: true}
	data, err := xray.Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var document struct {
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
				Network     string   `json:"network"`
				OutboundTag string   `json:"outboundTag"`
				BalancerTag string   `json:"balancerTag"`
				IP          []string `json:"ip"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("the document is not JSON: %v", err)
	}

	var hop *struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
	}
	for _, outbound := range document.Outbounds {
		if outbound.Tag != xray.WarpTag {
			continue
		}
		if outbound.Protocol != "socks" {
			t.Fatalf("the WARP outbound is %q, want socks", outbound.Protocol)
		}
		if len(outbound.Settings.Servers) != 1 {
			t.Fatalf("the WARP outbound has %d servers", len(outbound.Settings.Servers))
		}
		hop = &outbound.Settings.Servers[0]
	}
	if hop == nil {
		t.Fatal("there is no WARP outbound")
	}
	if hop.Address != "127.0.0.1" || hop.Port != 40001 {
		t.Errorf("the sidecar hop is %s:%d, want 127.0.0.1:40001", hop.Address, hop.Port)
	}

	var viaWarp, viaDirect bool
	for _, rule := range document.Routing.Rules {
		if rule.BalancerTag == xray.WarpBalancerTag {
			if rule.Network != "tcp,udp" {
				t.Errorf("the WARP rule covers %q, want tcp,udp", rule.Network)
			}
			viaWarp = true
		}
		if rule.Network == "udp" && rule.OutboundTag == "direct" {
			viaDirect = true
		}
		if rule.OutboundTag == "block" && rule.Network == "tcp,udp" && len(rule.IP) > 0 {
			t.Errorf("the usque document refuses a family: %v", rule.IP)
		}
	}
	if !viaWarp {
		t.Error("no rule sends traffic to the WARP balancer")
	}
	if viaDirect {
		t.Error("UDP still goes direct, which is the proxy transport's gap")
	}
}
