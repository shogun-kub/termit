package warp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// DialSocks5 opens a TCP connection to address through an unauthenticated SOCKS5 proxy.
//
// A domain name is sent as a domain, never resolved locally: the proxy decides what the name means.
// That is what makes the WARP probe honest — if the name were resolved on this host, the request
// could reach Cloudflare over the direct path while looking like it went through the tunnel.
//
// The proxy is on loopback, so there is no reason to accept anything but the no-auth method.
func DialSocks5(ctx context.Context, proxy, address string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("warp: dial %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("warp: dial %q: port %q is not a port", address, portText)
	}

	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, fmt.Errorf("warp: reach the sidecar at %s: %w", proxy, err)
	}
	// A context that ends mid-handshake must not leave a half-open socket behind.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close() //nolint:errcheck // nothing to do with the error.
			return nil, err
		}
	} else if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		conn.Close() //nolint:errcheck // nothing to do with the error.
		return nil, err
	}
	if err := socks5Handshake(conn, host, port); err != nil {
		conn.Close() //nolint:errcheck // nothing to do with the error.
		return nil, err
	}
	// The deadline was for the handshake only; the caller's own timeouts take over from here.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close() //nolint:errcheck // nothing to do with the error.
		return nil, err
	}
	return conn, nil
}

func socks5Handshake(conn net.Conn, host string, port int) error {
	// Greeting: version 5, one method, no authentication.
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fmt.Errorf("warp: socks greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("warp: socks greeting reply: %w", err)
	}
	if reply[0] != 0x05 {
		return fmt.Errorf("warp: the sidecar speaks SOCKS %d, not 5", reply[0])
	}
	if reply[1] != 0x00 {
		return errors.New("warp: the sidecar demands authentication, which warp-cli's proxy mode does not")
	}

	request := []byte{0x05, 0x01, 0x00} // CONNECT
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("warp: host name %q is too long for SOCKS5", host)
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port)) // #nosec G115 -- port is bounds-checked above.
	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("warp: socks connect request: %w", err)
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("warp: socks connect reply: %w", err)
	}
	if head[0] != 0x05 {
		return fmt.Errorf("warp: the sidecar answered SOCKS %d, not 5", head[0])
	}
	if head[1] != 0x00 {
		return fmt.Errorf("warp: the sidecar refused the connection: %s", socksReplyText(head[1]))
	}
	// Drain the bound address so the stream starts at the payload.
	var length int
	switch head[3] {
	case 0x01:
		length = 4
	case 0x04:
		length = 16
	case 0x03:
		size := make([]byte, 1)
		if _, err := io.ReadFull(conn, size); err != nil {
			return fmt.Errorf("warp: socks bound address: %w", err)
		}
		length = int(size[0])
	default:
		return fmt.Errorf("warp: socks reply has address type %d", head[3])
	}
	if _, err := io.ReadFull(conn, make([]byte, length+2)); err != nil {
		return fmt.Errorf("warp: socks bound address: %w", err)
	}
	return nil
}

func socksReplyText(code byte) string {
	switch code {
	case 0x01:
		return "general failure"
	case 0x02:
		return "connection not allowed"
	case 0x03:
		return "network unreachable"
	case 0x04:
		return "host unreachable"
	case 0x05:
		return "connection refused"
	case 0x06:
		return "TTL expired"
	case 0x07:
		return "command not supported"
	case 0x08:
		return "address type not supported"
	default:
		return "reply code " + strings.ToLower(strconv.Itoa(int(code)))
	}
}
