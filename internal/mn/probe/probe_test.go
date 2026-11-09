package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestBuildConfig(t *testing.T) {
	t.Parallel()
	targets := []Target{
		{ID: "a", Address: "10.0.0.11", Port: 443, ServerName: "uv1.lan", Path: "/s1/", Mode: "packet-up", UUID: "11111111-1111-4111-8111-111111111111", PinnedCertSHA256: "aa"},
		{ID: "b", Address: "10.0.0.12", Port: 443, ServerName: "uv2.lan", Path: "/s2/", Mode: "packet-up", UUID: "22222222-2222-4222-8222-222222222222"},
	}
	ports := []int{19001, 19002}

	cfg, err := buildConfig(targets, ports)
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}

	if len(cfg.Inbounds) != 2 {
		t.Fatalf("inbounds = %d, want 2", len(cfg.Inbounds))
	}
	for i, in := range cfg.Inbounds {
		if in.Port != ports[i] {
			t.Fatalf("inbound %d port = %d, want %d", i, in.Port, ports[i])
		}
		if in.Listen != "127.0.0.1" {
			t.Fatalf("inbound %d listens on %q, want loopback", i, in.Listen)
		}
		if in.Protocol != "socks" {
			t.Fatalf("inbound %d protocol = %q", i, in.Protocol)
		}
	}

	// One direct fallback plus one outbound per target.
	if len(cfg.Outbounds) != len(targets)+1 {
		t.Fatalf("outbounds = %d, want %d", len(cfg.Outbounds), len(targets)+1)
	}
	first := cfg.Outbounds[0]
	vless, ok := first.Settings.(xrayVLESSOut)
	if !ok {
		t.Fatalf("outbound settings type = %T", first.Settings)
	}
	if first.Protocol != "vless" || first.StreamSettings == nil {
		t.Fatalf("first outbound is not a vless stream: %+v", first)
	}
	if first.StreamSettings.Network != "xhttp" || first.StreamSettings.Security != "tls" {
		t.Fatalf("stream = %+v, want xhttp+tls", first.StreamSettings)
	}
	if got := first.StreamSettings.TLS.ServerName; got != "uv1.lan" {
		t.Fatalf("serverName = %q", got)
	}
	if got := first.StreamSettings.TLS.PinnedPeerCertSHA256; got != "aa" {
		t.Fatalf("pin = %q", got)
	}
	if got := first.StreamSettings.XHTTP.Path; got != "/s1/" {
		t.Fatalf("path = %q", got)
	}
	if got := vless.Vnext[0].Address; got != "10.0.0.11" {
		t.Fatalf("address = %q", got)
	}
	if got := vless.Vnext[0].Users[0].ID; got != targets[0].UUID {
		t.Fatalf("uuid = %q", got)
	}

	// The second target has no pin; the field must be omitted rather than set to an empty string.
	if got := cfg.Outbounds[1].StreamSettings.TLS.PinnedPeerCertSHA256; got != "" {
		t.Fatalf("second pin = %q, want empty", got)
	}

	if len(cfg.Routing.Rules) != len(targets)+1 {
		t.Fatalf("routing rules = %d, want %d", len(cfg.Routing.Rules), len(targets)+1)
	}
	if cfg.Routing.Rules[0].InboundTag[0] != "socks-a" || cfg.Routing.Rules[0].OutboundTag != "vless-a" {
		t.Fatalf("first rule = %+v", cfg.Routing.Rules[0])
	}
	if last := cfg.Routing.Rules[len(cfg.Routing.Rules)-1]; last.OutboundTag != "direct" {
		t.Fatalf("last rule = %+v, want direct fallback", last)
	}
}

func TestBuildConfigRejectsIncompleteTarget(t *testing.T) {
	t.Parallel()
	if _, err := buildConfig([]Target{{ID: "a"}}, []int{19001}); err == nil {
		t.Fatal("expected an error for an incomplete target")
	}
	if _, err := buildConfig([]Target{{ID: "a", Address: "x", Port: 1, UUID: "u", ServerName: "s"}}, []int{1, 2}); err == nil {
		t.Fatal("expected an error when targets and ports disagree")
	}
}

func TestSplitURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		host string
		port int
		ok   bool
	}{
		{"http://example.com/", "example.com", 80, true},
		{"https://example.com/", "example.com", 443, true},
		{"https://example.com:8443/generate_204", "example.com", 8443, true},
		{"http://10.0.0.1/x", "10.0.0.1", 80, true},
		{"ftp://example.com/", "", 0, false},
		{"example.com", "", 0, false},
	}
	for _, tc := range cases {
		host, port, err := splitURL(tc.in)
		if tc.ok && err != nil {
			t.Fatalf("splitURL(%q): %v", tc.in, err)
		}
		if !tc.ok {
			if err == nil {
				t.Fatalf("splitURL(%q): expected an error", tc.in)
			}
			continue
		}
		if host != tc.host || port != tc.port {
			t.Fatalf("splitURL(%q) = %q,%d want %q,%d", tc.in, host, port, tc.host, tc.port)
		}
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"nil", nil, ClassNone},
		{"deadline", context.DeadlineExceeded, ClassTimeout},
		{"eof", io.EOF, ClassClosed},
		{"reset", errors.New("read: connection reset by peer"), ClassClosed},
		{"socks rejected", errors.New("probe: socks connect failed with code 5"), ClassRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := classify(tc.err)
			if got != tc.want {
				t.Fatalf("classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestSocksConnectAndStatus exercises the client against a scripted SOCKS5 server.
func TestSocksConnectAndStatus(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		var greet [3]byte
		if _, err := io.ReadFull(conn, greet[:]); err != nil {
			return
		}
		if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
			return
		}
		var head [4]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			return
		}
		// Domain-form address: one length byte, the name, then the port.
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, conn, int64(l[0])+2); err != nil {
			return
		}
		// Success with an IPv4 bound address.
		reply := []byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 80}
		if _, err := conn.Write(reply); err != nil {
			return
		}
		var request [4]byte
		if _, err := io.ReadFull(conn, request[:]); err != nil {
			return
		}
		if _, err := conn.Write([]byte("HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n")); err != nil {
			return
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := socksConnect(ctx, ln.Addr().String(), "example.com", 80)
	if err != nil {
		t.Fatalf("socksConnect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	status, err := httpStatusThroughTunnel(conn, "example.com")
	if err != nil {
		t.Fatalf("httpStatus: %v", err)
	}
	if status != 204 {
		t.Fatalf("status = %d, want 204", status)
	}
	<-done
}

func TestFreePortsAreDistinctAndUsable(t *testing.T) {
	t.Parallel()
	ports, err := freePorts(3, 21000)
	if err != nil {
		t.Fatalf("freePorts: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("got %d ports", len(ports))
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if seen[p] {
			t.Fatalf("duplicate port %d", p)
		}
		seen[p] = true
	}
	// A freshly bound listener is what the caller needs; ports must at least be valid numbers.
	for _, p := range ports {
		if p < 1 || p > 65535 {
			t.Fatalf("nonsense port %d", p)
		}
	}
}

func TestEncodeSocksAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host string
		atyp byte
		len  int
	}{
		{"127.0.0.1", 0x01, 4},
		{"::1", 0x04, 16},
		{"example.com", 0x03, 1 + len("example.com")},
	}
	for _, tc := range cases {
		b, atyp, err := encodeSocksAddr(tc.host)
		if err != nil {
			t.Fatalf("encodeSocksAddr(%q): %v", tc.host, err)
		}
		if atyp != tc.atyp || len(b) != tc.len {
			t.Fatalf("encodeSocksAddr(%q) = atyp %d len %d, want %d/%d", tc.host, atyp, len(b), tc.atyp, tc.len)
		}
	}
	if b, _, _ := encodeSocksAddr("127.0.0.1"); binary.BigEndian.Uint32(b) != 0x7f000001 {
		t.Fatalf("ipv4 encoding = %x", b)
	}
}

func TestWorkerRejectsBadConfig(t *testing.T) {
	t.Parallel()
	if _, err := NewWorker(Config{}); err == nil {
		t.Fatal("expected an error for an empty config")
	}
	if _, err := NewWorker(Config{Binary: "/nonexistent/xray", ProbeURL: "http://x/", WorkDir: t.TempDir()}); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestLastLines(t *testing.T) {
	t.Parallel()
	got := lastLines("a\nb\nc\nd\n", 2)
	if got != "c | d" {
		t.Fatalf("lastLines = %q", got)
	}
	if first := firstLine([]byte("err1\nerr2\n")); first != "err1" {
		t.Fatalf("firstLine = %q", first)
	}
}

// TestAnUnmodelledErrorIsNotAVerdict: the class a check reports decides whether a node can be blocked.
// An error the prober does not recognise says nothing about the node, so it must not read as one.
func TestAnUnmodelledErrorIsNotAVerdict(t *testing.T) {
	t.Parallel()
	class, err := ClassifyForTest(errors.New("x509: certificate signed by unknown authority"))
	if class != ClassProbeError {
		t.Errorf("class = %q, want %q", class, ClassProbeError)
	}
	if err == nil {
		t.Error("the text of the error was dropped")
	}
	if !(Result{Class: class}).Unknown() {
		t.Error("an unmodelled error must count as unknown, not as evidence against the node")
	}
	// A reset is still the node's tunnel closing, and still counts as a failure.
	if class, _ := ClassifyForTest(errors.New("read tcp: connection reset by peer")); class != ClassClosed {
		t.Errorf("a reset was classified %q", class)
	}
}
