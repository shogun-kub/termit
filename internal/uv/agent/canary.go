package agent

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/xray"
)

// CanaryCheck proves the running core still carries traffic after a change. The interface lets a
// test decide the answer without starting a second Xray.
type CanaryCheck interface {
	Check(ctx context.Context) error
}

// Canary is one real VLESS+XHTTP connection through the node's own inbound, using the owner's
// credential, to the decoy on loopback. It never leaves the box, so it proves the client set works
// without contacting a host the agent is not allowed to reach.
type Canary struct {
	// Binary is the pinned core; it runs a second, short-lived process with a client config.
	Binary string
	// ProxyAddr is the core's loopback inbound. nginx normally fronts it; the canary speaks to it
	// directly because the peer set does not change nginx's configuration.
	ProxyAddr string
	// Target is the local HTTP endpoint the tunnel should reach, and Host is the name nginx answers.
	Target  string
	Host    string
	Path    string
	UUID    string
	WorkDir string
	Timeout time.Duration
	// Dial and Run are seams for tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Check runs the canary and reports the first thing that did not work.
func (c Canary) Check(ctx context.Context) error {
	if c.Binary == "" {
		return errors.New("agent: the canary has no core binary")
	}
	if c.UUID == "" {
		return errors.New("agent: the canary has no credential to test with")
	}
	if c.ProxyAddr == "" {
		c.ProxyAddr = net.JoinHostPort(xray.DefaultListen, strconv.Itoa(xray.DefaultPort))
	}
	if c.Target == "" {
		c.Target = "127.0.0.1:80"
	}
	if c.Host == "" {
		c.Host = "localhost"
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}

	socksPort, err := freeLoopbackPort()
	if err != nil {
		return err
	}
	config := canaryConfig(socksPort, c.ProxyAddr, c.Path, c.UUID)
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("agent: encode the canary config: %w", err)
	}
	path, cleanup, err := writeSecretFile(c.WorkDir, ".canary-*.json", data)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	dial := c.Dial
	if dial == nil {
		var dialer net.Dialer
		dial = dialer.DialContext
	}
	// A restart makes systemd report the unit started before the core is listening. The canary must
	// wait for the inbound, or it tests a closed port and blames the client set.
	if err := waitListener(ctx, dial, c.ProxyAddr, 5*time.Second); err != nil {
		return fmt.Errorf("agent: the core is not accepting connections on %s: %w", c.ProxyAddr, err)
	}

	if out, err := exec.CommandContext(ctx, c.Binary, "run", "-test", "-c", path).CombinedOutput(); err != nil {
		return fmt.Errorf("agent: the core rejected the canary config: %w (%s)", err, firstLine(out))
	}

	output := &strings.Builder{}
	cmd := exec.CommandContext(ctx, c.Binary, "run", "-c", path)
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agent: start the canary: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	socksAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))
	if err := waitListener(ctx, dial, socksAddr, 5*time.Second); err != nil {
		return fmt.Errorf("agent: the canary did not start: %w (log: %s)", err, lastLines(output.String(), 3))
	}

	host, port, err := splitHostPort(c.Target)
	if err != nil {
		return err
	}
	conn, err := socks5Connect(ctx, dial, socksAddr, host, port)
	if err != nil {
		return fmt.Errorf("agent: the tunnel refused the canary: %w (log: %s)", err, lastLines(output.String(), 3))
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(c.Timeout))

	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+c.Host+
		"\r\nUser-Agent: termit-canary/1\r\nConnection: close\r\n\r\n"); err != nil {
		return fmt.Errorf("agent: write the canary request: %w", err)
	}
	status, err := readStatus(conn)
	if err != nil {
		return fmt.Errorf("agent: read the canary answer: %w (log: %s)", err, lastLines(output.String(), 3))
	}
	if status < 200 || status >= 400 {
		return fmt.Errorf("agent: the canary reached the web tier but got HTTP %d", status)
	}
	return nil
}

// canaryConfig is a client for the node's own inbound: one loopback SOCKS inbound, one VLESS
// outbound, and a routing rule joining them.
func canaryConfig(socksPort int, proxyAddr, path, uuid string) any {
	host, port, err := splitHostPort(proxyAddr)
	if err != nil {
		host, port = xray.DefaultListen, xray.DefaultPort
	}
	type socksSettings struct {
		Auth string `json:"auth"`
		UDP  bool   `json:"udp"`
	}
	type vlessUser struct {
		ID         string `json:"id"`
		Encryption string `json:"encryption"`
	}
	type vnext struct {
		Address string      `json:"address"`
		Port    int         `json:"port"`
		Users   []vlessUser `json:"users"`
	}
	type vlessSettings struct {
		Vnext []vnext `json:"vnext"`
	}
	type xhttp struct {
		Path string `json:"path"`
		Mode string `json:"mode,omitempty"`
	}
	type stream struct {
		Network  string `json:"network"`
		Security string `json:"security"`
		XHTTP    xhttp  `json:"xhttpSettings"`
	}
	type inbound struct {
		Tag      string        `json:"tag"`
		Listen   string        `json:"listen"`
		Port     int           `json:"port"`
		Protocol string        `json:"protocol"`
		Settings socksSettings `json:"settings"`
	}
	type outbound struct {
		Tag      string        `json:"tag"`
		Protocol string        `json:"protocol"`
		Settings vlessSettings `json:"settings"`
		Stream   stream        `json:"streamSettings"`
	}
	type rule struct {
		Type        string   `json:"type"`
		InboundTag  []string `json:"inboundTag"`
		OutboundTag string   `json:"outboundTag"`
	}
	type routing struct {
		Rules []rule `json:"rules"`
	}
	type config struct {
		Log       map[string]string `json:"log"`
		Inbounds  []inbound         `json:"inbounds"`
		Outbounds []outbound        `json:"outbounds"`
		Routing   routing           `json:"routing"`
	}
	return config{
		Log: map[string]string{"loglevel": "warning"},
		Inbounds: []inbound{{
			Tag: "socks", Listen: "127.0.0.1", Port: socksPort, Protocol: "socks",
			Settings: socksSettings{Auth: "noauth"},
		}},
		Outbounds: []outbound{{
			Tag: "node", Protocol: "vless",
			Settings: vlessSettings{Vnext: []vnext{{
				Address: host, Port: port,
				Users: []vlessUser{{ID: uuid, Encryption: "none"}},
			}}},
			Stream: stream{
				Network: "xhttp", Security: "none",
				XHTTP: xhttp{Path: path, Mode: "packet-up"},
			},
		}},
		Routing: routing{Rules: []rule{{
			Type: "field", InboundTag: []string{"socks"}, OutboundTag: "node",
		}}},
	}
}

// writeSecretFile writes data to a fresh 0600 file whose name carries no secret, and returns the
// path and a cleanup function.
func writeSecretFile(dir, pattern string, data []byte) (string, func(), error) {
	if dir == "" {
		dir = os.TempDir()
	}
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, fmt.Errorf("agent: create %s: %w", pattern, err)
	}
	name := file.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("agent: write %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("agent: close %s: %w", name, err)
	}
	return name, cleanup, nil
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("agent: find a loopback port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("agent: release the loopback port: %w", err)
	}
	return port, nil
}

func waitListener(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := dial(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agent: %s did not listen: %w", addr, err)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func splitHostPort(addr string) (string, int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("agent: %q is not host:port: %w", addr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("agent: %q has a bad port: %w", addr, err)
	}
	return host, port, nil
}

// socks5Connect performs a SOCKS5 CONNECT with no authentication and returns the tunnelled stream.
func socks5Connect(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), proxyAddr, host string, port int) (net.Conn, error) {
	conn, err := dial(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return nil, err
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return nil, err
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		return nil, fmt.Errorf("agent: socks refused no-auth: %x", greeting)
	}
	address, typeCode, err := encodeSocksAddr(host)
	if err != nil {
		return nil, err
	}
	request := append([]byte{0x05, 0x01, 0x00, typeCode}, address...)
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	if _, err := conn.Write(request); err != nil {
		return nil, err
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return nil, err
	}
	if reply[0] != 0x05 {
		return nil, fmt.Errorf("agent: bad socks version %x", reply[0])
	}
	if reply[1] != 0x00 {
		return nil, fmt.Errorf("agent: socks connect failed with code %d", reply[1])
	}
	var skip int
	switch reply[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return nil, err
		}
		skip = int(length[0])
	default:
		return nil, fmt.Errorf("agent: unsupported socks address type %d", reply[3])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(skip+2)); err != nil {
		return nil, err
	}
	ok = true
	return conn, nil
}

func encodeSocksAddr(host string) ([]byte, byte, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4, 0x01, nil
		}
		return ip.To16(), 0x04, nil
	}
	if len(host) > 255 {
		return nil, 0, fmt.Errorf("agent: host too long for socks: %q", host)
	}
	return append([]byte{byte(len(host))}, host...), 0x03, nil
}

func readStatus(conn net.Conn) (int, error) {
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, fmt.Errorf("agent: malformed status line %q", line)
	}
	return strconv.Atoi(fields[1])
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
