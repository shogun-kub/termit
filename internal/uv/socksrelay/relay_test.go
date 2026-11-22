package socksrelay

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSidecar is a SOCKS5 server that behaves like usque: it keys a UDP association on the TCP
// connection that asked for it and refuses (here: records) a datagram from any other address.
type fakeSidecar struct {
	ln     net.Listener
	tcpFor atomic.Int64 // the control connection's source port
	udpFor atomic.Int64 // the first datagram's source port
}

func newFakeSidecar(t *testing.T) *fakeSidecar {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeSidecar{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(conn)
		}
	}()
	return f
}

func (f *fakeSidecar) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// greeting: version, one method
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	req := make([]byte, 10)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}
	if req[1] != cmdUDP {
		// A CONNECT gets the success reply and an echo, which is enough for the relay's TCP test.
		if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
			return
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		_, _ = conn.Write(buf)
		return
	}

	remote, _ := conn.RemoteAddr().(*net.TCPAddr)
	f.tcpFor.Store(int64(remote.Port))
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return
	}
	defer func() { _ = udp.Close() }()
	relay := udp.LocalAddr().(*net.UDPAddr)
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, byte(relay.Port >> 8), byte(relay.Port)}); err != nil {
		return
	}

	buf := make([]byte, 2048)
	for {
		_ = udp.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, from, err := udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		f.udpFor.CompareAndSwap(0, int64(from.Port))
		// A datagram from a port other than the control connection's is refused, exactly as usque does.
		if int64(from.Port) != f.tcpFor.Load() {
			continue
		}
		_, _ = udp.WriteToUDP(buf[:n], from)
	}
}

func (f *fakeSidecar) addr() string { return f.ln.Addr().String() }

func startRelay(t *testing.T, upstream string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = ServeOn(ctx, ln, upstream, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})))
	}()
	return ln
}

// clientConn greets the relay and returns the connection ready for a request.
func clientConn(t *testing.T, relay net.Listener) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", relay.Addr().String())
	if err != nil {
		t.Fatalf("dial the relay: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("greeting reply: %v", err)
	}
	if reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("greeting reply = %v, want [5 0]", reply)
	}
	return conn
}

// TestUDPAssociationUsesTheControlPort: the relay must send the client's datagrams from the same port as
// its control connection, or the sidecar refuses them ("udp address … is not associated with tcp").
func TestUDPAssociationUsesTheControlPort(t *testing.T) {
	t.Parallel()
	sidecar := newFakeSidecar(t)
	relay := startRelay(t, sidecar.addr())

	conn := clientConn(t, relay)
	if _, err := conn.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("associate request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("associate reply: %v", err)
	}
	if reply[1] != 0 {
		t.Fatalf("associate reply = %v, want success", reply)
	}
	relayed := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(reply[8])<<8 | int(reply[9])}

	udp, err := net.DialUDP("udp", nil, relayed)
	if err != nil {
		t.Fatalf("dial the relay's UDP address: %v", err)
	}
	defer func() { _ = udp.Close() }()
	_ = udp.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := udp.Write([]byte("ping")); err != nil {
		t.Fatalf("send: %v", err)
	}
	buf := make([]byte, 64)
	n, err := udp.Read(buf)
	if err != nil {
		t.Fatalf("no answer through the relay (the sidecar refuses a mismatched source port): %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("answer = %q, want %q", buf[:n], "ping")
	}
	if got, want := sidecar.udpFor.Load(), sidecar.tcpFor.Load(); got != want {
		t.Errorf("the sidecar saw the datagram from port %d, its control connection was %d", got, want)
	}
}

// TestConnectPassthrough: a TCP stream goes through the relay to the sidecar and back.
func TestConnectPassthrough(t *testing.T) {
	t.Parallel()
	sidecar := newFakeSidecar(t)
	relay := startRelay(t, sidecar.addr())

	conn := clientConn(t, relay)
	if _, err := conn.Write([]byte{5, 1, 0, 1, 192, 0, 2, 1, 0x01, 0xbb}); err != nil {
		t.Fatalf("connect request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("connect reply: %v", err)
	}
	if reply[1] != 0 {
		t.Fatalf("connect reply = %v, want success", reply)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read the echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("echo = %q, want %q", buf, "ping")
	}
}

// TestUDPFromAnotherHostIsNotForwarded: the pump only accepts datagrams from the client's own host. A
// second loopback address stands in for "somebody else" — two ports on 127.0.0.1 are the same host, and
// the relay deliberately cannot tell them apart.
func TestUDPFromAnotherHostIsNotForwarded(t *testing.T) {
	t.Parallel()
	sidecar := newFakeSidecar(t)
	relay := startRelay(t, sidecar.addr())

	conn := clientConn(t, relay)
	if _, err := conn.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("associate request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("associate reply: %v", err)
	}
	relayed := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(reply[8])<<8 | int(reply[9])}

	stranger, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2)})
	if err != nil {
		t.Skipf("this host cannot bind 127.0.0.2: %v", err)
	}
	defer func() { _ = stranger.Close() }()
	if _, err := stranger.WriteToUDP([]byte("spoof"), relayed); err != nil {
		t.Fatalf("send: %v", err)
	}

	client, err := net.DialUDP("udp", nil, relayed)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("send: %v", err)
	}
	buf := make([]byte, 64)
	if n, err := client.Read(buf); err == nil && string(buf[:n]) == "spoof" {
		t.Error("a datagram from another host reached the client")
	}
}

var _ sync.Mutex // keep the import set stable while the suite grows
