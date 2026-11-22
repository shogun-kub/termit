// Package socksrelay is a loopback SOCKS5 relay that makes the MASQUE sidecar's UDP association usable.
//
// usque's SOCKS5 server keys an association on the TCP connection that asked for it: a UDP datagram whose
// source address is not that TCP client's address is refused ("udp address … is not associated with
// tcp"). Xray's SOCKS client opens a fresh UDP socket for every flow, so every UDP flow through the
// sidecar dies there. This relay asks the sidecar for the association over a control connection and then
// sends the client's datagrams from a socket bound to that connection's own port, so the addresses match
// and the sidecar carries the flow; the datagrams themselves are the same SOCKS5 frames either way.
package socksrelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"
)

const (
	socksVersion = 0x05

	cmdConnect = 0x01
	cmdUDP     = 0x03

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	repSuccess             = 0x00
	repGeneralFailure      = 0x01
	repCommandNotSupported = 0x07

	handshakeTimeout = 15 * time.Second
	// udpIdleTimeout ends an association nothing has used for a while. Xray opens a new one for the next
	// flow, so a quiet flow costs nothing by being forgotten.
	udpIdleTimeout = 2 * time.Minute
	// bindAttempts covers the rare case where the TCP source port is already taken for UDP.
	bindAttempts = 8
)

// Serve listens on listen and relays SOCKS5 to the SOCKS5 server at upstream until ctx is done.
func Serve(ctx context.Context, listen, upstream string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("socks relay: listen %s: %w", listen, err)
	}
	return ServeOn(ctx, listener, upstream, log)
}

// ServeOn is Serve on a listener the caller already has, which is how a test picks the port and the
// command reports what it bound.
func ServeOn(ctx context.Context, listener net.Listener, upstream string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	defer func() { _ = listener.Close() }()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	log.Info("socks relay listening", "listen", listener.Addr().String(), "upstream", upstream)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("socks relay: accept: %w", err)
		}
		go handle(ctx, conn, upstream, log)
	}
}

func handle(ctx context.Context, client net.Conn, upstream string, log *slog.Logger) {
	defer func() { _ = client.Close() }()
	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := negotiate(client); err != nil {
		log.Debug("socks relay: negotiation", "err", err)
		return
	}
	cmd, req, err := readRequest(client)
	if err != nil {
		log.Debug("socks relay: request", "err", err)
		return
	}
	_ = client.SetDeadline(time.Time{})

	switch cmd {
	case cmdConnect:
		if err := connect(ctx, client, upstream, req); err != nil {
			log.Debug("socks relay: connect", "err", err)
		}
	case cmdUDP:
		if err := associate(ctx, client, upstream, log); err != nil {
			log.Debug("socks relay: associate", "err", err)
		}
	default:
		_ = writeReply(client, repCommandNotSupported, net.IPv4zero)
	}
}

// request is a client request with its address left in the wire form, so it can be forwarded unchanged.
type request struct {
	atyp byte
	addr []byte
	port []byte
}

// negotiate answers the greeting: version 5, no authentication.
func negotiate(conn net.Conn) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("read the greeting: %w", err)
	}
	if head[0] != socksVersion {
		return fmt.Errorf("socks version %d", head[0])
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return fmt.Errorf("read the methods: %w", err)
	}
	_, err := conn.Write([]byte{socksVersion, 0x00})
	return err
}

// readRequest reads one CONNECT or UDP ASSOCIATE request.
func readRequest(conn net.Conn) (byte, request, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, request{}, fmt.Errorf("read the request: %w", err)
	}
	if head[0] != socksVersion {
		return 0, request{}, fmt.Errorf("request version %d", head[0])
	}
	req := request{atyp: head[3]}
	switch head[3] {
	case atypIPv4:
		req.addr = make([]byte, 4)
	case atypIPv6:
		req.addr = make([]byte, 16)
	case atypDomain:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return 0, request{}, fmt.Errorf("read the domain length: %w", err)
		}
		req.addr = make([]byte, int(length[0]))
	default:
		return 0, request{}, fmt.Errorf("address type %d", head[3])
	}
	if _, err := io.ReadFull(conn, req.addr); err != nil {
		return 0, request{}, fmt.Errorf("read the address: %w", err)
	}
	req.port = make([]byte, 2)
	if _, err := io.ReadFull(conn, req.port); err != nil {
		return 0, request{}, fmt.Errorf("read the port: %w", err)
	}
	return head[1], req, nil
}

// writeRequest sends a request to the upstream server.
func writeRequest(conn net.Conn, cmd byte, req request) error {
	buf := []byte{socksVersion, cmd, 0x00, req.atyp}
	if req.atyp == atypDomain {
		buf = append(buf, byte(len(req.addr)))
	}
	buf = append(buf, req.addr...)
	buf = append(buf, req.port...)
	_, err := conn.Write(buf)
	return err
}

// readReply reads the upstream server's answer: the reply code and, for UDP, its relay address.
func readReply(conn net.Conn) (byte, *net.UDPAddr, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, nil, fmt.Errorf("read the reply: %w", err)
	}
	addr := &net.UDPAddr{}
	switch head[3] {
	case atypIPv4:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return 0, nil, fmt.Errorf("read the reply address: %w", err)
		}
		addr.IP = net.IP(raw)
	case atypIPv6:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return 0, nil, fmt.Errorf("read the reply address: %w", err)
		}
		addr.IP = net.IP(raw)
	default:
		return 0, nil, fmt.Errorf("reply address type %d", head[3])
	}
	raw := make([]byte, 2)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return 0, nil, fmt.Errorf("read the reply port: %w", err)
	}
	addr.Port = int(raw[0])<<8 | int(raw[1])
	return head[1], addr, nil
}

// writeReply answers the client: IPv4, the given address.
func writeReply(conn net.Conn, rep byte, ip net.IP) error {
	v4 := ip.To4()
	if v4 == nil {
		v4 = net.IPv4zero.To4()
	}
	_, err := conn.Write([]byte{socksVersion, rep, 0x00, atypIPv4, v4[0], v4[1], v4[2], v4[3], 0x00, 0x00})
	return err
}

// writeAssociateReply answers a UDP ASSOCIATE with the relay's own address, which is where the client
// sends its datagrams.
func writeAssociateReply(conn net.Conn, addr *net.UDPAddr) error {
	v4 := addr.IP.To4()
	if v4 == nil {
		v4 = net.IPv4(127, 0, 0, 1).To4()
	}
	port := []byte{byte(addr.Port >> 8), byte(addr.Port)}
	buf := append([]byte{socksVersion, repSuccess, 0x00, atypIPv4}, v4...)
	buf = append(buf, port...)
	_, err := conn.Write(buf)
	return err
}

// connect relays one TCP stream through the upstream server.
func connect(ctx context.Context, client net.Conn, upstream string, req request) error {
	up, err := dialUpstream(ctx, upstream)
	if err != nil {
		_ = writeReply(client, repGeneralFailure, net.IPv4zero)
		return err
	}
	defer func() { _ = up.Close() }()
	if err := writeRequest(up, cmdConnect, req); err != nil {
		_ = writeReply(client, repGeneralFailure, net.IPv4zero)
		return err
	}
	rep, _, err := readReply(up)
	if err != nil {
		_ = writeReply(client, repGeneralFailure, net.IPv4zero)
		return err
	}
	if rep != repSuccess {
		_ = writeReply(client, rep, net.IPv4zero)
		return fmt.Errorf("the upstream refused the connection: %d", rep)
	}
	if err := writeReply(client, repSuccess, net.IPv4zero); err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, up); done <- struct{}{} }()
	<-done
	return nil
}

// associate sets up the UDP association and stays with it until either control connection ends.
func associate(ctx context.Context, client net.Conn, upstream string, log *slog.Logger) error {
	for attempt := 0; attempt < bindAttempts; attempt++ {
		up, err := dialUpstream(ctx, upstream)
		if err != nil {
			_ = writeReply(client, repGeneralFailure, net.IPv4zero)
			return err
		}
		local, ok := up.LocalAddr().(*net.TCPAddr)
		if !ok {
			_ = up.Close()
			return errors.New("the upstream connection has no TCP address")
		}
		// The port is the whole point: it is the address the sidecar keys the association on.
		udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: local.IP, Port: local.Port})
		if err != nil {
			_ = up.Close()
			continue
		}
		zero := request{atyp: atypIPv4, addr: net.IPv4zero.To4(), port: []byte{0, 0}}
		if err := writeRequest(up, cmdUDP, zero); err != nil {
			_ = udp.Close()
			_ = up.Close()
			_ = writeReply(client, repGeneralFailure, net.IPv4zero)
			return err
		}
		rep, relay, err := readReply(up)
		if err != nil || rep != repSuccess {
			_ = udp.Close()
			_ = up.Close()
			_ = writeReply(client, repGeneralFailure, net.IPv4zero)
			if err == nil {
				err = fmt.Errorf("the upstream refused the association: %d", rep)
			}
			return err
		}
		if relay == nil || relay.IP.IsUnspecified() || relay.Port == 0 {
			host, port, splitErr := net.SplitHostPort(upstream)
			if splitErr != nil {
				_ = udp.Close()
				_ = up.Close()
				return splitErr
			}
			parsed, parseErr := strconv.Atoi(port)
			if parseErr != nil {
				_ = udp.Close()
				_ = up.Close()
				return parseErr
			}
			relay = &net.UDPAddr{IP: net.ParseIP(host), Port: parsed}
		}
		if err := writeAssociateReply(client, udp.LocalAddr().(*net.UDPAddr)); err != nil {
			_ = udp.Close()
			_ = up.Close()
			return err
		}
		log.Debug("socks relay: association", "client", client.RemoteAddr(), "relay", udp.LocalAddr(), "upstream", relay)
		return pump(client, up, udp, relay)
	}
	_ = writeReply(client, repGeneralFailure, net.IPv4zero)
	return errors.New("no free UDP port for the association")
}

// pump forwards SOCKS5 datagrams between the client and the sidecar's relay until either control
// connection ends or nothing moves for udpIdleTimeout.
func pump(client, up net.Conn, udp *net.UDPConn, relay *net.UDPAddr) error {
	defer func() { _ = udp.Close() }()
	clientAddr, _ := client.RemoteAddr().(*net.TCPAddr)
	if clientAddr == nil {
		return errors.New("the client connection has no TCP address")
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(io.Discard, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(io.Discard, up); done <- struct{}{} }()
	go func() {
		<-done
		_ = udp.Close()
	}()

	var peer *net.UDPAddr
	buf := make([]byte, 65535)
	for {
		_ = udp.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, from, err := udp.ReadFromUDP(buf)
		if err != nil {
			return nil
		}
		switch {
		case from.IP.Equal(relay.IP) && from.Port == relay.Port:
			// A reply from the sidecar: hand it to the client that asked.
			if peer != nil {
				_, _ = udp.WriteToUDP(buf[:n], peer)
			}
		case from.IP.Equal(clientAddr.IP):
			peer = from
			if _, err := udp.WriteToUDP(buf[:n], relay); err != nil {
				return nil
			}
		}
	}
}

// dialUpstream opens a control connection to the sidecar's SOCKS5 listener and greets it.
func dialUpstream(ctx context.Context, upstream string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: handshakeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", upstream)
	if err != nil {
		return nil, fmt.Errorf("socks relay: reach the sidecar at %s: %w", upstream, err)
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := greetUpstream(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// greetUpstream is the client half of the SOCKS5 greeting.
func greetUpstream(conn net.Conn) error {
	if _, err := conn.Write([]byte{socksVersion, 0x01, 0x00}); err != nil {
		return fmt.Errorf("socks relay: greet the sidecar: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks relay: read the sidecar's greeting: %w", err)
	}
	if reply[0] != socksVersion || reply[1] != 0x00 {
		return fmt.Errorf("socks relay: the sidecar refused no-auth: %v", reply)
	}
	return nil
}
