package server

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// listenerFor wraps a loopback listener exactly as the server does, and returns the address to dial.
func listenerFor(t *testing.T, trusted *TrustedProxies) (net.Listener, string) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := NewProxyProtoListener(inner, trusted, 2*time.Second)
	t.Cleanup(func() { _ = ln.Close() })
	return ln, inner.Addr().String()
}

// speak dials the listener, writes what a proxy would write, and closes: the accepted connection then
// reads to EOF. It runs before the accept, so a queued connection is served at once and the test never
// touches t from another goroutine.
func speak(t *testing.T, addr, payload string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func acceptOne(t *testing.T, ln net.Listener) (net.Conn, string) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return conn, string(body)
}

func TestParseTrustedProxies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []string
		want    []string
		bad     string
	}{
		{name: "single addresses", entries: []string{"192.0.2.10", "2001:db8::1"}, want: []string{"192.0.2.10", "2001:db8::1"}},
		{name: "cidr blocks", entries: []string{"10.0.0.0/8"}, want: []string{"10.0.0.42"}},
		{name: "a host inside a block", entries: []string{"10.1.2.3/32"}, want: []string{"10.1.2.3"}},
		{name: "blank entries are skipped", entries: []string{"", "  "}, want: nil},
		{name: "a name is not an address", entries: []string{"proxy.example"}, bad: "neither an address nor a CIDR"},
		{name: "a broken block", entries: []string{"10.0.0.0/99"}, bad: "trusted proxy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trusted, err := ParseTrustedProxies(tc.entries)
			if tc.bad != "" {
				if err == nil || !strings.Contains(err.Error(), tc.bad) {
					t.Fatalf("error = %v, want one containing %q", err, tc.bad)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTrustedProxies(%v): %v", tc.entries, err)
			}
			for _, ip := range tc.want {
				if !trusted.Contains(net.ParseIP(ip)) {
					t.Errorf("%s should be trusted", ip)
				}
			}
			if tc.want != nil && trusted.Empty() {
				t.Error("a populated set reports itself empty")
			}
		})
	}
}

func TestNilOrEmptyTrustsNobody(t *testing.T) {
	t.Parallel()
	var nilSet *TrustedProxies
	if nilSet.Contains(net.ParseIP("192.0.2.1")) {
		t.Error("a nil set trusted an address")
	}
	empty, err := ParseTrustedProxies(nil)
	if err != nil {
		t.Fatalf("ParseTrustedProxies(nil): %v", err)
	}
	if !empty.Empty() {
		t.Error("an empty set is not empty")
	}
	if empty.Contains(net.ParseIP("192.0.2.1")) {
		t.Error("an empty set trusted an address")
	}
}

func TestTrustedPeerGetsTheClientAddress(t *testing.T) {
	t.Parallel()
	trusted, err := ParseTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ln, addr := listenerFor(t, trusted)
	speak(t, addr, "PROXY TCP4 203.0.113.9 192.0.2.1 40000 443\r\nGET /healthz HTTP/1.1\r\n")

	conn, body := acceptOne(t, ln)
	if got, want := conn.RemoteAddr().String(), "203.0.113.9:40000"; got != want {
		t.Errorf("RemoteAddr = %q, want %q", got, want)
	}
	if want := "GET /healthz HTTP/1.1\r\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestTrustedPeerWithoutAHeaderKeepsItsAddress(t *testing.T) {
	t.Parallel()
	trusted, err := ParseTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ln, addr := listenerFor(t, trusted)
	speak(t, addr, "GET /healthz HTTP/1.1\r\n")

	conn, body := acceptOne(t, ln)
	if strings.HasPrefix(conn.RemoteAddr().String(), "127.0.0.1:") == false {
		t.Errorf("RemoteAddr = %q, want the loopback peer", conn.RemoteAddr())
	}
	if want := "GET /healthz HTTP/1.1\r\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestUntrustedPeerIsReadAsPlainBytes(t *testing.T) {
	t.Parallel()
	trusted, err := ParseTrustedProxies([]string{"198.51.100.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ln, addr := listenerFor(t, trusted)
	payload := "PROXY TCP4 203.0.113.9 192.0.2.1 40000 443\r\nGET / HTTP/1.1\r\n"
	speak(t, addr, payload)

	conn, body := acceptOne(t, ln)
	if strings.HasPrefix(conn.RemoteAddr().String(), "127.0.0.1:") == false {
		t.Errorf("RemoteAddr = %q, want the loopback peer", conn.RemoteAddr())
	}
	if body != payload {
		t.Errorf("body = %q, want the bytes untouched", body)
	}
}

func TestUnknownFamilyKeepsThePeer(t *testing.T) {
	t.Parallel()
	trusted, err := ParseTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ln, addr := listenerFor(t, trusted)
	speak(t, addr, "PROXY UNKNOWN\r\nGET / HTTP/1.1\r\n")

	conn, body := acceptOne(t, ln)
	if strings.HasPrefix(conn.RemoteAddr().String(), "127.0.0.1:") == false {
		t.Errorf("RemoteAddr = %q, want the loopback peer", conn.RemoteAddr())
	}
	if want := "GET / HTTP/1.1\r\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestMalformedHeaderDropsThatConnection(t *testing.T) {
	t.Parallel()
	trusted, err := ParseTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ln, addr := listenerFor(t, trusted)

	bad, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := io.WriteString(bad, "PROXY NONSENSE whatever\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = bad.Close()
	// The listener drops the malformed connection and keeps accepting: the next one is served.
	time.Sleep(100 * time.Millisecond)
	speak(t, addr, "PROXY TCP4 203.0.113.7 192.0.2.1 40001 443\r\nfine")

	conn, body := acceptOne(t, ln)
	if got, want := conn.RemoteAddr().String(), "203.0.113.7:40001"; got != want {
		t.Errorf("RemoteAddr = %q, want %q", got, want)
	}
	if body != "fine" {
		t.Errorf("body = %q, want %q", body, "fine")
	}
}

func TestTheHTTPRequestSeesTheClient(t *testing.T) {
	t.Parallel()
	trusted, err := ParseTrustedProxies([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ln, addr := listenerFor(t, trusted)

	seen := make(chan string, 1)
	srv := &http.Server{ //nolint:gosec // a test server on the loopback, serving one request
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- r.RemoteAddr
			w.WriteHeader(http.StatusNoContent)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	request := "PROXY TCP4 203.0.113.9 192.0.2.1 40000 443\r\n" +
		"GET /healthz HTTP/1.1\r\nHost: mn.example.com\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.Copy(io.Discard, conn)

	select {
	case got := <-seen:
		if want := "203.0.113.9:40000"; got != want {
			t.Errorf("r.RemoteAddr = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran")
	}
}

func TestParseProxyLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		line   string
		want   string
		nobody bool
		bad    bool
	}{
		{name: "tcp4", line: "PROXY TCP4 203.0.113.9 192.0.2.1 40000 443\r\n", want: "203.0.113.9:40000"},
		{name: "tcp6", line: "PROXY TCP6 2001:db8::1 2001:db8::2 40000 443\r\n", want: "[2001:db8::1]:40000"},
		{name: "unknown", line: "PROXY UNKNOWN\r\n", nobody: true},
		{name: "unknown with the trailing spec text", line: "PROXY UNKNOWN ffff:f...:ffff 0 0\r\n", nobody: true},
		{name: "an http request is not a header", line: "GET / HTTP/1.1\r\n", bad: true},
		{name: "a missing field", line: "PROXY TCP4 203.0.113.9 192.0.2.1 40000\r\n", bad: true},
		{name: "a short family name", line: "PROXY TCP 203.0.113.9 192.0.2.1 40000 443\r\n", bad: true},
		{name: "an address that is not one", line: "PROXY TCP4 nope 192.0.2.1 40000 443\r\n", bad: true},
		{name: "tcp4 with an ipv6 source", line: "PROXY TCP4 2001:db8::1 192.0.2.1 40000 443\r\n", bad: true},
		{name: "a port that is not one", line: "PROXY TCP4 203.0.113.9 192.0.2.1 0 443\r\n", bad: true},
		{name: "a port that is not a number", line: "PROXY TCP4 203.0.113.9 192.0.2.1 http 443\r\n", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, err := parseProxyLine(tc.line)
			switch {
			case tc.bad:
				if err == nil {
					t.Fatalf("parseProxyLine(%q) = %v, want an error", tc.line, addr)
				}
			case tc.nobody:
				if err != nil {
					t.Fatalf("parseProxyLine(%q): %v", tc.line, err)
				}
				if addr != nil {
					t.Errorf("parseProxyLine(%q) = %v, want no address", tc.line, addr)
				}
			default:
				if err != nil {
					t.Fatalf("parseProxyLine(%q): %v", tc.line, err)
				}
				if got := addr.String(); got != tc.want {
					t.Errorf("parseProxyLine(%q) = %q, want %q", tc.line, got, tc.want)
				}
			}
		})
	}
}
