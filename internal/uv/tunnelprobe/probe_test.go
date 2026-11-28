package tunnelprobe

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// The probe runs the node's core with a generated client config, so what is testable without a core is the
// config it writes and everything around starting one: the refusals, the port it picks, and the wait that
// tells "the core exited" from "the core is slow".

func TestFetchRefusesAnIncompleteRequest(t *testing.T) {
	t.Parallel()
	complete := Options{
		XrayBinary: "/usr/lib/termit/bin/xray",
		ProxyAddr:  "127.0.0.1:8443",
		Path:       "/0123456789abcdef/",
		UUID:       "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
		TargetURL:  "https://www.cloudflare.com/cdn-cgi/trace",
	}
	for _, missing := range []string{"XrayBinary", "ProxyAddr", "Path", "UUID", "TargetURL"} {
		opts := complete
		switch missing {
		case "XrayBinary":
			opts.XrayBinary = ""
		case "ProxyAddr":
			opts.ProxyAddr = ""
		case "Path":
			opts.Path = ""
		case "UUID":
			opts.UUID = ""
		case "TargetURL":
			opts.TargetURL = ""
		}
		if _, err := Fetch(context.Background(), opts); err == nil {
			t.Errorf("Fetch accepted a request without %s", missing)
		}
	}
}

// The config is what tells the temporary core which path and device to use: a wrong one probes something
// that is not this node.
func TestTheClientConfigCarriesTheNodesIdentity(t *testing.T) {
	t.Parallel()
	opts := Options{
		ProxyAddr: "127.0.0.1:8443",
		Path:      "/0123456789abcdef/",
		UUID:      "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
	}
	body := config(40001, opts)

	var parsed struct {
		Inbounds []struct {
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
		Outbounds []struct {
			Protocol string `json:"protocol"`
			Settings struct {
				Vnext []struct {
					Port  int `json:"port"`
					Users []struct {
						ID string `json:"id"`
					} `json:"users"`
				} `json:"vnext"`
			} `json:"settings"`
			StreamSettings struct {
				Network string `json:"network"`
				XHTTP   struct {
					Path string `json:"path"`
					Mode string `json:"mode"`
				} `json:"xhttpSettings"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("the generated config is not JSON: %v\n%s", err, body)
	}
	if len(parsed.Inbounds) != 1 || parsed.Inbounds[0].Port != 40001 || parsed.Inbounds[0].Listen != "127.0.0.1" {
		t.Errorf("the SOCKS inbound is %+v", parsed.Inbounds)
	}
	out := parsed.Outbounds[0]
	if out.Protocol != "vless" || len(out.Settings.Vnext) != 1 {
		t.Fatalf("the outbound is %+v", out)
	}
	if got := out.Settings.Vnext[0].Port; got != 8443 {
		t.Errorf("the outbound port is %d, want the proxy address's 8443", got)
	}
	if got := out.Settings.Vnext[0].Users[0].ID; got != opts.UUID {
		t.Errorf("the outbound device is %q", got)
	}
	if out.StreamSettings.Network != "xhttp" || out.StreamSettings.XHTTP.Path != opts.Path {
		t.Errorf("the transport is %+v", out.StreamSettings)
	}
}

func TestPortOf(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		address string
		want    int
	}{
		{"127.0.0.1:8443", 8443},
		{"[::1]:8443", 8443},
		{"127.0.0.1", 0},
		{"127.0.0.1:not-a-port", 0},
		{"", 0},
	} {
		if got := portOf(test.address); got != test.want {
			t.Errorf("portOf(%q) = %d, want %d", test.address, got, test.want)
		}
	}
}

func TestFreeLoopbackPortIsUsable(t *testing.T) {
	t.Parallel()
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatalf("freeLoopbackPort: %v", err)
	}
	if port == 0 {
		t.Fatal("freeLoopbackPort returned 0")
	}
}

func TestWaitListenerAcceptsAListener(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitListener(ctx, listener.Addr().String()); err != nil {
		t.Fatalf("waitListener: %v", err)
	}
}

// A core that exits instead of listening is a failed probe: the wait ends with the context, not with the
// five-second deadline.
func TestWaitListenerEndsWithItsContext(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = waitListener(ctx, address)
	if err == nil {
		t.Fatal("waitListener accepted a port nothing listens on")
	}
	if !strings.Contains(err.Error(), "never listened") && ctx.Err() == nil {
		t.Errorf("error = %v", err)
	}
}
