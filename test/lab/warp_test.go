//go:build lab

package lab_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/xray"
	"github.com/shogun-kub/termit/test/lab"
)

// M4.1 on a lab that cannot run WARP. The containers are unprivileged LXC with no /dev/net/tun, so
// the tunnel itself is out of reach here; what is testable is everything around it, and that is
// exactly what tends to break:
//
//   - the refusal: WARP on by default, no tun, a clear reason instead of a failed install;
//   - the route: a real Xray config with the WARP outbound, the observatory and the fail-open
//     balancer, accepted by `xray -test` on the node;
//   - fail-open: with nothing listening on the sidecar's port, traffic still flows, direct;
//   - the switch back: `warp mode off` removes the route and traffic keeps flowing.
//
// The client that proves the last two points runs on the node itself and speaks to the node's
// loopback inbound, so the test needs no public address, no CA and no extra package on the lab.

const (
	warpWorkDir   = "/root/termit-warp"
	warpSocksPort = 18108
)

// warpFetch waits for the HTTP body the script prints through the tunnel. It asks 1.1.1.1 by
// address rather than by name, so the check exercises the egress path and not name resolution: a
// lookup that fails would fail the test for a reason that has nothing to do with this feature.
const warpFetch = `import socket, ssl, sys

port = int(sys.argv[1])
host = "1.1.1.1"
sock = socket.create_connection(("127.0.0.1", port), timeout=15)
sock.sendall(bytes([5, 1, 0]))
if sock.recv(2) != bytes([5, 0]):
    sys.exit("socks greeting failed")
request = bytes([5, 1, 0, 1, 1, 1, 1, 1]) + (443).to_bytes(2, "big")
sock.sendall(request)
reply = sock.recv(10)
if len(reply) < 2 or reply[1] != 0:
    sys.exit("socks connect refused: %r" % reply)
tls = ssl.create_default_context().wrap_socket(sock, server_hostname=host)
tls.sendall(b"GET /cdn-cgi/trace HTTP/1.1\r\nHost: 1.1.1.1\r\nConnection: close\r\n\r\n")
data = b""
while True:
    chunk = tls.recv(4096)
    if not chunk:
        break
    data += chunk
body = data.split(b"\r\n\r\n", 1)[-1].decode("utf-8", "replace")
if "warp=" not in body:
    sys.exit("no trace in the answer: %r" % body[:200])
print(body)
`

// nodeClient is the short-lived Xray client that carries the probe through the node's own inbound.
type nodeClient struct {
	machine *lab.Machine
	port    int
}

// startNodeClient parses the node's own URI and starts a client that sends everything through the
// node's VLESS inbound, exactly as the owner's device would.
func startNodeClient(t *testing.T, ctx context.Context, m *lab.Machine) *nodeClient {
	t.Helper()
	raw := strings.TrimSpace(m.MustRun(t, ctx, "termit-uv uri"))
	profile, err := proto.ParseVLESSUrl(raw)
	if err != nil {
		t.Fatalf("%s: parse its own URI: %v", m.Name, err)
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "socks", "listen": "127.0.0.1", "port": warpSocksPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"tag": "proxy", "protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": xray.DefaultPort,
				"users": []any{map[string]any{"id": profile.UUID, "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "none",
				"xhttpSettings": map[string]any{"path": profile.Path, "mode": "packet-up"},
			},
		}},
	}
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("encode the client config: %v", err)
	}
	m.MustRun(t, ctx, "mkdir -p "+warpWorkDir)
	if err := m.WriteFile(ctx, body, warpWorkDir+"/client.json", "0600"); err != nil {
		t.Fatalf("upload the client config: %v", err)
	}
	if err := m.WriteFile(ctx, []byte(warpFetch), warpWorkDir+"/fetch.py", "0600"); err != nil {
		t.Fatalf("upload the fetch script: %v", err)
	}
	if _, err := m.Run(ctx, "test -x /usr/lib/termit/bin/xray"); err != nil {
		t.Fatalf("%s has no core binary to run the client with", m.Name)
	}
	start := fmt.Sprintf("sh -c 'setsid nohup /usr/lib/termit/bin/xray run -config %s/client.json "+
		">%s/client.log 2>&1 </dev/null & echo $! >%s/client.pid'", warpWorkDir, warpWorkDir, warpWorkDir)
	if out, err := m.Run(ctx, start); err != nil {
		t.Fatalf("start the client: %v\n%s", err, out)
	}
	client := &nodeClient{machine: m, port: warpSocksPort}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if out, err := m.Run(cleanup, "kill $(cat "+warpWorkDir+"/client.pid) 2>/dev/null; true"); err != nil {
			t.Logf("%s: stop the client: %v\n%s", m.Name, err, out)
		}
		if out, err := m.Run(cleanup, "rm -rf "+warpWorkDir); err != nil {
			t.Logf("%s: remove the client directory: %v\n%s", m.Name, err, out)
		}
	})
	return client
}

// fetch asks Cloudflare for its view of the connection through the tunnel. It retries because the
// balancer may still be deciding: the first probe through a dead sidecar has to fail before the
// fallback to direct engages.
func (c *nodeClient) fetch(t *testing.T, ctx context.Context, wait time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(wait)
	started := time.Now()
	var last string
	for time.Now().Before(deadline) {
		out, err := c.machine.Run(ctx, fmt.Sprintf("python3 %s/fetch.py %d", warpWorkDir, c.port))
		if err == nil && strings.Contains(out, "warp=") {
			t.Logf("traffic through the node worked after %s", time.Since(started).Round(time.Second))
			return out
		}
		last = out
		if pid, _ := c.machine.Run(ctx, "cat "+warpWorkDir+"/client.pid"); strings.TrimSpace(pid) == "" {
			t.Fatalf("the client process is gone:\n%s", c.machine.MustRun(t, ctx, "tail -20 "+warpWorkDir+"/client.log"))
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("no trace through the node within %s: %s\nclient log:\n%s", wait, last,
		c.machine.MustRun(t, ctx, "tail -20 "+warpWorkDir+"/client.log"))
	return ""
}

// warpHidden reports whether the trace says Cloudflare saw a direct connection rather than WARP.
func warpHidden(trace string) bool { return strings.Contains(trace, "warp=off") }

func warpStatusLine(status string) string {
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "warp:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func TestLabWarpRefusalAndFailOpen(t *testing.T) {
	l := drillLab(t)
	victim := drillVictim(t, l)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	domain := nodeDomain(t, ctx, victim)
	makeActive(t, ctx, l, victim, domain)
	// The lab nodes were installed before WARP defaulted to on, so their configuration carries the
	// old switch. The test ends where the node started: WARP off, the way the owner here wants it.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Minute)
		defer done()
		if out, err := victim.Run(cleanup, "termit-uv warp mode off"); err != nil {
			t.Logf("cleanup: %s warp mode off: %v\n%s", victim.Name, err, out)
		}
	})

	t.Run("an old configuration migrates to a mode", func(t *testing.T) {
		status := victim.MustRun(t, ctx, "termit-uv status")
		line := warpStatusLine(status)
		if line == "" {
			t.Fatalf("uv status has no warp line:\n%s", status)
		}
		// The state file was written by an older build with warp.enabled: false.
		if !strings.Contains(line, "off") {
			t.Errorf("warp line = %q, want off for a configuration that opted out", line)
		}
	})

	t.Run("WARP is requested but the host cannot run it", func(t *testing.T) {
		out, err := victim.Run(ctx, "termit-uv warp mode all")
		t.Logf("warp mode all:\n%s", out)
		if err != nil {
			t.Fatalf("warp mode all: %v", err)
		}
		for _, want := range []string{"cannot run on this host", "recorded", "egress mode: all"} {
			if !strings.Contains(out, want) {
				t.Errorf("output does not contain %q:\n%s", want, out)
			}
		}
		config := victim.MustRun(t, ctx, "cat /etc/termit/uv/config.yaml")
		if !strings.Contains(config, "mode: all") {
			t.Errorf("the configuration does not record the mode:\n%s", config)
		}
		if strings.Contains(config, "\n  enabled:") {
			t.Errorf("the old switch is still written beside the mode:\n%s", config)
		}
	})

	t.Run("the core accepts a config with the WARP route", func(t *testing.T) {
		config := victim.MustRun(t, ctx, "cat /etc/termit/uv/xray.json")
		for _, want := range []string{
			`"tag": "warp"`, `"protocol": "socks"`,
			`"balancerTag": "warp-egress"`, `"fallbackTag": "direct"`,
			`"observatory"`, `"probeUrl": "https://www.cloudflare.com/cdn-cgi/trace"`,
			`"127.0.0.0/8"`,
		} {
			if !strings.Contains(config, want) {
				t.Errorf("the generated config does not contain %q:\n%s", want, config)
			}
		}
		victim.MustRun(t, ctx, "/usr/lib/termit/bin/xray run -test -config /etc/termit/uv/xray.json")
	})

	// Nothing listens on 127.0.0.1:40000 in this container, so the sidecar is dead and the fail-open
	// balancer is the only reason a client can still reach the internet through the node.
	client := startNodeClient(t, ctx, victim)
	trace := client.fetch(t, ctx, 2*time.Minute)
	t.Logf("trace through the node:\n%s", trace)
	if !warpHidden(trace) {
		t.Errorf("the trace does not say warp=off; traffic did not fall back to direct:\n%s", trace)
	}

	t.Run("the node is still healthy for the server", func(t *testing.T) {
		if outcome := runCheckNow(t, ctx, l, domain); outcome.Event != "check_ok" {
			t.Errorf("the check failed while WARP was unreachable: %+v", outcome)
		}
		if state := showState(t, ctx, l, domain); state != "active" {
			t.Errorf("the node is %s, want active", state)
		}
	})

	t.Run("status and doctor tell the truth", func(t *testing.T) {
		status := victim.MustRun(t, ctx, "termit-uv status")
		line := warpStatusLine(status)
		if !strings.Contains(line, "all") || !strings.Contains(line, "unavailable") {
			t.Errorf("warp line = %q, want all and unavailable", line)
		}
		if !strings.Contains(line, "tun") {
			t.Errorf("warp line does not explain the refusal: %q", line)
		}
		doctor, _ := victim.Run(ctx, "termit-uv doctor")
		doctorWarp := doctorLine(doctor, "warp")
		if !strings.Contains(doctorWarp, "skip") {
			t.Errorf("doctor does not skip WARP with a reason: %q", doctorWarp)
		}
	})

	t.Run("turning it off removes the route and keeps the traffic", func(t *testing.T) {
		out, err := victim.Run(ctx, "termit-uv warp mode off")
		t.Logf("warp mode off:\n%s", out)
		if err != nil {
			t.Fatalf("warp mode off: %v", err)
		}
		config := victim.MustRun(t, ctx, "cat /etc/termit/uv/xray.json")
		if strings.Contains(config, `"tag": "warp"`) || strings.Contains(config, `"observatory"`) {
			t.Errorf("the WARP route survived the switch back to off:\n%s", config)
		}
		victim.MustRun(t, ctx, "/usr/lib/termit/bin/xray run -test -config /etc/termit/uv/xray.json")
		trace := client.fetch(t, ctx, time.Minute)
		if !warpHidden(trace) {
			t.Errorf("the trace does not say warp=off after the switch back:\n%s", trace)
		}
		status := victim.MustRun(t, ctx, "termit-uv status")
		if line := warpStatusLine(status); !strings.Contains(line, "off") {
			t.Errorf("warp line = %q, want off", line)
		}
		if outcome := runCheckNow(t, ctx, l, domain); outcome.Event != "check_ok" {
			t.Errorf("the check failed after the switch back: %+v", outcome)
		}
	})
}
