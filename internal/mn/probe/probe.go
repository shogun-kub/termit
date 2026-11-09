// Package probe checks that a node's proxy actually works, end to end.
//
// The prober runs one xray-core worker process holding a SOCKS inbound per node, each routed to that
// node's VLESS+XHTTP outbound, and performs a real HTTP request through each tunnel. Everything that
// could not be measured — a missing binary, a worker that will not start, a config that fails
// validation — is reported as ClassWorker, which callers must treat as "unknown", never as a failure
// of the node.
package probe

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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Class describes why a probe failed. The empty class means success.
type Class string

const (
	ClassNone     Class = ""
	ClassTimeout  Class = "timeout"
	ClassRejected Class = "tunnel_rejected"
	ClassClosed   Class = "tunnel_closed"
	ClassHTTP     Class = "http_error"
	// ClassWorker means the prober itself could not do its job. It is *unknown*, not a node failure.
	ClassWorker Class = "worker_unavailable"
)

// Target is one node to check.
type Target struct {
	ID         string
	Address    string
	Port       int
	ServerName string // SNI; usually the node's domain
	Path       string // XHTTP path, with the trailing slash the server expects
	Mode       string // XHTTP mode; "packet-up" is what works over an HTTP/1.1 hop
	UUID       string
	// PinnedCertSHA256 pins the node certificate (lowercase hex). Required in production; empty means
	// the worker verifies the chain against the system roots instead.
	PinnedCertSHA256 string
}

// Result is one node's outcome.
type Result struct {
	ID      string
	OK      bool
	Latency time.Duration
	Class   Class
	Err     error
}

// Unknown reports whether the prober failed rather than the node.
func (r Result) Unknown() bool { return r.Class == ClassWorker }

// Config configures a Worker.
type Config struct {
	// Binary is the xray-core executable.
	Binary string
	// WorkDir holds the generated config and the worker's log; created if missing.
	WorkDir string
	// ProbeURL is fetched through each tunnel. Its host and port are what the tunnel must reach.
	ProbeURL string
	// ExpectStatus is the HTTP status that counts as success. Defaults to 200.
	ExpectStatus int
	// Timeout bounds one node's probe. Defaults to 10s.
	Timeout time.Duration
	// BasePort is the first SOCKS port to try. Defaults to 19000.
	BasePort int
}

// Worker runs xray-core and probes targets through it.
type Worker struct {
	cfg Config
}

// NewWorker validates the configuration and that the xray binary is runnable.
func NewWorker(cfg Config) (*Worker, error) {
	if cfg.Binary == "" {
		return nil, errors.New("probe: xray binary is required")
	}
	if _, err := os.Stat(cfg.Binary); err != nil {
		return nil, fmt.Errorf("probe: xray binary %s: %w", cfg.Binary, err)
	}
	if cfg.ProbeURL == "" {
		return nil, errors.New("probe: probe URL is required")
	}
	if _, _, err := splitURL(cfg.ProbeURL); err != nil {
		return nil, err
	}
	if cfg.WorkDir == "" {
		return nil, errors.New("probe: work dir is required")
	}
	if cfg.ExpectStatus == 0 {
		cfg.ExpectStatus = 200
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.BasePort == 0 {
		cfg.BasePort = 19000
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o750); err != nil {
		return nil, fmt.Errorf("probe: create work dir: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cfg.Binary, "version").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("probe: %s version: %w (%s)", cfg.Binary, err, strings.TrimSpace(string(out)))
	}
	return &Worker{cfg: cfg}, nil
}

// Probe checks every target. A non-nil error means the prober could not run at all; otherwise the
// slice has one result per target, in order.
func (w *Worker) Probe(ctx context.Context, targets []Target) ([]Result, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	ports, err := freePorts(len(targets), w.cfg.BasePort)
	if err != nil {
		return nil, err
	}

	cfg, err := buildConfig(targets, ports)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(w.cfg.WorkDir, "probe.json")
	if err := writeJSON(cfgPath, cfg); err != nil {
		return nil, err
	}

	if out, err := exec.CommandContext(ctx, w.cfg.Binary, "run", "-test", "-c", cfgPath).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("probe: generated config rejected by xray: %w (%s)", err, firstLine(out))
	}

	logPath := filepath.Join(w.cfg.WorkDir, "probe.log")
	// #nosec G304 -- logPath is under Config.WorkDir.
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("probe: create log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.CommandContext(ctx, w.cfg.Binary, "run", "-c", cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("probe: start xray: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	if err := waitPorts(ctx, ports, 5*time.Second); err != nil {
		detail, _ := os.ReadFile(logPath)
		return nil, fmt.Errorf("probe: worker did not listen: %w (%s)", err, lastLines(string(detail), 3))
	}

	results := make([]Result, len(targets))
	for i, t := range targets {
		results[i] = w.probeOne(ctx, t, ports[i])
	}
	return results, nil
}

func (w *Worker) probeOne(ctx context.Context, t Target, socksPort int) Result {
	start := time.Now()
	res := Result{ID: t.ID}

	ctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer cancel()

	socksAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))
	host, port, err := splitURL(w.cfg.ProbeURL)
	if err != nil {
		res.Class, res.Err = ClassWorker, err
		return res
	}

	conn, err := socksConnect(ctx, socksAddr, host, port)
	if err != nil {
		res.Latency = time.Since(start)
		res.Class, res.Err = classify(err)
		return res
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(w.cfg.Timeout))

	status, err := httpStatusThroughTunnel(conn, host)
	res.Latency = time.Since(start)
	if err != nil {
		res.Class, res.Err = classify(err)
		return res
	}
	if status != w.cfg.ExpectStatus {
		res.Class = ClassHTTP
		res.Err = fmt.Errorf("probe: got HTTP %d, want %d", status, w.cfg.ExpectStatus)
		return res
	}
	res.OK = true
	return res
}

// buildConfig renders the worker configuration: one SOCKS inbound per target, one VLESS outbound per
// target, and a routing rule joining them.
func buildConfig(targets []Target, ports []int) (*xrayConfig, error) {
	if len(targets) != len(ports) {
		return nil, errors.New("probe: targets and ports disagree")
	}
	cfg := &xrayConfig{
		Log:     &xrayLog{Loglevel: "warning"},
		Routing: &xrayRouter{},
	}

	for i, t := range targets {
		if t.ID == "" || t.Address == "" || t.Port == 0 || t.UUID == "" || t.ServerName == "" {
			return nil, fmt.Errorf("probe: target %d is incomplete", i)
		}
		inTag := "socks-" + t.ID
		outTag := "vless-" + t.ID

		cfg.Inbounds = append(cfg.Inbounds, xrayInbound{
			Tag:      inTag,
			Listen:   "127.0.0.1",
			Port:     ports[i],
			Protocol: "socks",
			Settings: xraySocksSettings{Auth: "noauth", UDP: false},
		})

		tls := &xrayTLS{ServerName: t.ServerName}
		if t.PinnedCertSHA256 != "" {
			tls.PinnedPeerCertSHA256 = t.PinnedCertSHA256
		}
		cfg.Outbounds = append(cfg.Outbounds, xrayOutbound{
			Tag:      outTag,
			Protocol: "vless",
			Settings: xrayVLESSOut{
				Vnext: []xrayVnext{{
					Address: t.Address,
					Port:    t.Port,
					Users:   []xrayUser{{ID: t.UUID, Encryption: "none"}},
				}},
			},
			StreamSettings: &xrayStream{
				Network:  "xhttp",
				Security: "tls",
				TLS:      tls,
				XHTTP:    &xrayXHTTP{Path: t.Path, Mode: t.Mode},
			},
		})
		cfg.Routing.Rules = append(cfg.Routing.Rules, xrayRule{
			Type:        "field",
			InboundTag:  []string{inTag},
			OutboundTag: outTag,
		})
	}
	// The direct outbound is the default for unmatched traffic and must come last.
	cfg.Outbounds = append(cfg.Outbounds, xrayOutbound{Tag: "direct", Protocol: "freedom"})
	cfg.Routing.Rules = append(cfg.Routing.Rules, xrayRule{Type: "field", Network: "tcp,udp", OutboundTag: "direct"})
	return cfg, nil
}

// ---- xray config model (only the fields the prober sets) ----

type xrayConfig struct {
	Log       *xrayLog       `json:"log,omitempty"`
	Inbounds  []xrayInbound  `json:"inbounds"`
	Outbounds []xrayOutbound `json:"outbounds"`
	Routing   *xrayRouter    `json:"routing,omitempty"`
}

// xrayRouter is the routing object; xray expects {"rules": [...]}, not a bare array.
type xrayRouter struct {
	Rules []xrayRule `json:"rules"`
}

type xrayLog struct {
	Loglevel string `json:"loglevel"`
}

type xrayInbound struct {
	Tag      string            `json:"tag"`
	Listen   string            `json:"listen"`
	Port     int               `json:"port"`
	Protocol string            `json:"protocol"`
	Settings xraySocksSettings `json:"settings"`
}

type xraySocksSettings struct {
	Auth string `json:"auth"`
	UDP  bool   `json:"udp"`
}

type xrayOutbound struct {
	Tag            string      `json:"tag"`
	Protocol       string      `json:"protocol"`
	Settings       any         `json:"settings,omitempty"`
	StreamSettings *xrayStream `json:"streamSettings,omitempty"`
}

type xrayVLESSOut struct {
	Vnext []xrayVnext `json:"vnext"`
}

type xrayVnext struct {
	Address string     `json:"address"`
	Port    int        `json:"port"`
	Users   []xrayUser `json:"users"`
}

type xrayUser struct {
	ID         string `json:"id"`
	Encryption string `json:"encryption"`
}

type xrayStream struct {
	Network  string     `json:"network"`
	Security string     `json:"security"`
	TLS      *xrayTLS   `json:"tlsSettings,omitempty"`
	XHTTP    *xrayXHTTP `json:"xhttpSettings,omitempty"`
}

type xrayTLS struct {
	ServerName           string   `json:"serverName,omitempty"`
	PinnedPeerCertSHA256 string   `json:"pinnedPeerCertSha256,omitempty"`
	ALPN                 []string `json:"alpn,omitempty"`
}

type xrayXHTTP struct {
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"`
}

type xrayRule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag,omitempty"`
	Network     string   `json:"network,omitempty"`
	OutboundTag string   `json:"outboundTag"`
}

// ---- helpers ----

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("probe: marshal config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".probe*.json")
	if err != nil {
		return fmt.Errorf("probe: temp config: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("probe: write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("probe: close config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("probe: replace config: %w", err)
	}
	return nil
}

// freePorts returns n loopback ports, preferring to start at base.
func freePorts(n, base int) ([]int, error) {
	ports := make([]int, 0, n)
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for i := 0; i < n; i++ {
		var ln net.Listener
		var err error
		for attempt := 0; attempt < 20; attempt++ {
			ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(base+i+attempt)))
			if err == nil {
				ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
				listeners = append(listeners, ln)
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("probe: no free port near %d: %w", base+i, err)
		}
	}
	return ports, nil
}

func waitPorts(ctx context.Context, ports []int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, p := range ports {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), 200*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("probe: port %d not listening", p)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return nil
}

// splitURL extracts host and port from an http(s) URL without pulling in net/url's strictness about
// paths; the prober only ever needs a plain URL.
func splitURL(raw string) (host string, port int, err error) {
	rest := raw
	switch {
	case strings.HasPrefix(rest, "http://"):
		rest, port = strings.TrimPrefix(rest, "http://"), 80
	case strings.HasPrefix(rest, "https://"):
		rest, port = strings.TrimPrefix(rest, "https://"), 443
	default:
		return "", 0, fmt.Errorf("probe: probe URL %q must be http or https", raw)
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if h, p, splitErr := net.SplitHostPort(rest); splitErr == nil {
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return "", 0, fmt.Errorf("probe: bad port in %q", raw)
		}
		return h, n, nil
	}
	return rest, port, nil
}

// socksConnect performs a SOCKS5 CONNECT (no authentication) and returns the tunnelled connection.
func socksConnect(ctx context.Context, proxyAddr, host string, port int) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", proxyAddr)
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

	// Greeting: version 5, one method, no authentication.
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return nil, err
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return nil, err
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		return nil, fmt.Errorf("probe: socks proxy rejected authentication: %x", greeting)
	}

	addr, atyp, err := encodeSocksAddr(host)
	if err != nil {
		return nil, err
	}
	req := append([]byte{0x05, 0x01, 0x00, atyp}, addr...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return nil, err
	}
	if reply[0] != 0x05 {
		return nil, fmt.Errorf("probe: bad socks version in reply: %x", reply[0])
	}
	if reply[1] != 0x00 {
		return nil, fmt.Errorf("probe: socks connect failed with code %d", reply[1])
	}
	// Drain the bound address so the stream is positioned at the payload.
	var skip int
	switch reply[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return nil, err
		}
		skip = int(l[0])
	default:
		return nil, fmt.Errorf("probe: unsupported socks address type %d", reply[3])
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
		return nil, 0, fmt.Errorf("probe: host too long for socks: %q", host)
	}
	return append([]byte{byte(len(host))}, host...), 0x03, nil
}

// httpStatusThroughTunnel sends one minimal GET and returns the status code.
func httpStatusThroughTunnel(conn net.Conn, host string) (int, error) {
	req := "GET / HTTP/1.1\r\nHost: " + host + "\r\nUser-Agent: termit-probe/1\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return 0, err
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, fmt.Errorf("probe: malformed status line %q", line)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, fmt.Errorf("probe: malformed status %q", fields[1])
	}
	return status, nil
}

func classify(err error) (Class, error) {
	if err == nil {
		return ClassNone, nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return ClassTimeout, err
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ClassTimeout, err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return ClassClosed, err
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "socks connect failed"):
		return ClassRejected, err
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "broken pipe"):
		return ClassClosed, err
	}
	return ClassClosed, err
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
