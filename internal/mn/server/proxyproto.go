package server

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// proxyHeaderWait bounds how long a trusted peer may take to send the PROXY header. A proxy writes it
// before the TLS handshake, so it arrives with the first bytes; the wait only covers a stalled peer.
const proxyHeaderWait = 5 * time.Second

// maxProxyHeader is the longest version-1 header the specification allows, terminator included.
const maxProxyHeader = 108

// TrustedProxies is the set of addresses whose PROXY protocol header the server believes.
//
// The list is the whole security argument for the wrapping listener: a header is read only when the
// peer that sent it is on the list, so a client cannot claim an address by sending one itself. Behind
// a proxy that terminates TLS (instead of passing it through) the peer is the proxy for every node,
// and the enrollment DNS proof — "the domain must resolve to the address the request came from" —
// would compare against the proxy. This is what lets that proof see the node again.
type TrustedProxies struct {
	nets []net.IPNet
}

// ParseTrustedProxies reads addresses and CIDR blocks. An entry without a prefix is a single host.
func ParseTrustedProxies(entries []string) (*TrustedProxies, error) {
	trusted := &TrustedProxies{}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			_, block, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", entry, err)
			}
			trusted.nets = append(trusted.nets, *block)
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR block", entry)
		}
		bits := 128
		if ip.To4() != nil {
			ip, bits = ip.To4(), 32
		}
		trusted.nets = append(trusted.nets, net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return trusted, nil
}

// Contains reports whether ip may speak for a client.
func (t *TrustedProxies) Contains(ip net.IP) bool {
	if t == nil || ip == nil {
		return false
	}
	for _, block := range t.nets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// Empty reports that nothing is trusted and the listener should not be wrapped at all.
func (t *TrustedProxies) Empty() bool {
	return t == nil || len(t.nets) == 0
}

// NewProxyProtoListener wraps inner so that a connection from a trusted address has its version-1
// PROXY header read and removed before the HTTP server sees it: RemoteAddr then reports the client,
// which is what the enrollment DNS proof, the rate-limit key and the recorded last address are made
// of.
//
// A trusted peer that sends no header keeps its own address, and a peer that is not trusted is passed
// through untouched: the listener must not turn "the server is upgraded before the proxy is" into an
// outage.
func NewProxyProtoListener(inner net.Listener, trusted *TrustedProxies, wait time.Duration) net.Listener {
	if wait <= 0 {
		wait = proxyHeaderWait
	}
	return &proxyProtoListener{inner: inner, trusted: trusted, wait: wait}
}

type proxyProtoListener struct {
	inner   net.Listener
	trusted *TrustedProxies
	wait    time.Duration
}

func (l *proxyProtoListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		if !l.trusted.Contains(peerIP(conn.RemoteAddr())) {
			return conn, nil
		}
		wrapped, err := readProxyHeader(conn, l.wait)
		if err == nil {
			return wrapped, nil
		}
		// The peer is trusted, so the bytes were meant to be a header and are not: half a connection
		// is worse than none, and a loop is safe because each round consumed a real connection.
		_ = conn.Close()
	}
}

func (l *proxyProtoListener) Close() error { return l.inner.Close() }

func (l *proxyProtoListener) Addr() net.Addr { return l.inner.Addr() }

// readProxyHeader peeks the first bytes of a trusted connection. The PROXY line is removed when it is
// there; everything else is handed to the HTTP server byte for byte.
func readProxyHeader(conn net.Conn, wait time.Duration) (net.Conn, error) {
	buffered := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	peek, err := buffered.Peek(len("PROXY "))
	if err != nil || string(peek) != "PROXY " {
		// Nothing arrived yet, or the connection is not speaking the protocol: keep what was buffered
		// and let the HTTP server read its own handshake with its own deadlines.
		return &bufferedConn{Conn: conn, r: buffered}, nil
	}
	line, err := readHeaderLine(buffered)
	if err != nil {
		return nil, err
	}
	addr, err := parseProxyLine(line)
	if err != nil {
		return nil, err
	}
	return &bufferedConn{Conn: conn, r: buffered, addr: addr}, nil
}

// readHeaderLine reads one header line, refusing anything longer than the specification allows.
func readHeaderLine(buffered *bufio.Reader) (string, error) {
	var line strings.Builder
	for i := 0; i < maxProxyHeader; i++ {
		b, err := buffered.ReadByte()
		if err != nil {
			return "", err
		}
		line.WriteByte(b)
		if b == '\n' {
			return line.String(), nil
		}
	}
	return "", errors.New("proxy protocol header is longer than the specification allows")
}

// parseProxyLine turns one version-1 header line into the client's address. UNKNOWN is a valid line
// that names no client, and returns a nil address: the peer stays what the kernel said it was.
func parseProxyLine(line string) (net.Addr, error) {
	fields := strings.Split(strings.TrimRight(line, "\r\n"), " ")
	if len(fields) < 2 || fields[0] != "PROXY" {
		return nil, fmt.Errorf("not a PROXY protocol line: %q", line)
	}
	switch fields[1] {
	case "UNKNOWN":
		return nil, nil
	case "TCP4", "TCP6":
		if len(fields) != 6 {
			return nil, fmt.Errorf("PROXY %s line has %d fields, want 6", fields[1], len(fields))
		}
		source := net.ParseIP(fields[2])
		if source == nil {
			return nil, fmt.Errorf("PROXY %s source %q is not an address", fields[1], fields[2])
		}
		if fields[1] == "TCP4" && source.To4() == nil {
			return nil, fmt.Errorf("PROXY TCP4 source %q is not IPv4", fields[2])
		}
		if fields[1] == "TCP6" && source.To4() != nil {
			return nil, fmt.Errorf("PROXY TCP6 source %q is not IPv6", fields[2])
		}
		if net.ParseIP(fields[3]) == nil {
			return nil, fmt.Errorf("PROXY %s destination %q is not an address", fields[1], fields[3])
		}
		port, err := strconv.Atoi(fields[4])
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("PROXY %s source port %q is not a port", fields[1], fields[4])
		}
		return &net.TCPAddr{IP: source, Port: port}, nil
	default:
		return nil, fmt.Errorf("PROXY family %q is not TCP4, TCP6 or UNKNOWN", fields[1])
	}
}

// bufferedConn keeps what the PROXY negotiation already read, and reports the client the header named.
type bufferedConn struct {
	net.Conn
	r    *bufio.Reader
	addr net.Addr
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *bufferedConn) RemoteAddr() net.Addr {
	if c.addr != nil {
		return c.addr
	}
	return c.Conn.RemoteAddr()
}

// peerIP is the address of the far end of a connection, or nil when it cannot be told.
func peerIP(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}
