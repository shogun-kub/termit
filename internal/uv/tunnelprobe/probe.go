// Package tunnelprobe asks a node's own tunnel what the internet sees. It starts the node's core as a
// temporary client — a loopback SOCKS inbound and a VLESS outbound to the node's own listener — fetches
// one URL through it and stops the process again.
//
// It exists because "the tunnel is up" is not a fact an operator can act on: the address the request
// comes from is. The WARP egress is checked by fetching Cloudflare's trace through this path and reading
// the warp= line, which is the same evidence a client on a phone would produce.
package tunnelprobe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/shogun-kub/termit/internal/uv/warp"
)

// Options describe the temporary client and what to fetch through it.
type Options struct {
	// XrayBinary is the core to run; the node's own copy, so the probe tests the version in use.
	XrayBinary string
	// ProxyAddr is the node's loopback listener the core forwards to.
	ProxyAddr string
	// Path and UUID are the node's own secret path and device.
	Path string
	UUID string
	// TargetURL is what to fetch through the tunnel.
	TargetURL string
	// Timeout bounds the whole probe; zero means ten seconds.
	Timeout time.Duration
}

// Fetch returns the body of TargetURL as seen through the node's tunnel.
func Fetch(ctx context.Context, opts Options) (string, error) {
	if opts.XrayBinary == "" {
		return "", errors.New("tunnelprobe: no core binary")
	}
	if opts.ProxyAddr == "" {
		return "", errors.New("tunnelprobe: no proxy address")
	}
	if opts.Path == "" || opts.UUID == "" {
		return "", errors.New("tunnelprobe: the node has no identity")
	}
	if opts.TargetURL == "" {
		return "", errors.New("tunnelprobe: no target url")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	port, err := freeLoopbackPort()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "termit-probe-")
	if err != nil {
		return "", fmt.Errorf("tunnelprobe: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	configPath := filepath.Join(dir, "client.json")
	if err := os.WriteFile(configPath, config(port, opts), 0o600); err != nil {
		return "", fmt.Errorf("tunnelprobe: write the client config: %w", err)
	}

	cmd := exec.CommandContext(ctx, opts.XrayBinary, "run", "-config", configPath) // #nosec G204 -- the node's own core.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("tunnelprobe: start the core: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	socksAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if err := waitListener(ctx, socksAddr); err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, rawPort, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				port, err := strconv.Atoi(rawPort)
				if err != nil {
					return nil, err
				}
				return warp.DialSocks5(ctx, socksAddr, net.JoinHostPort(host, strconv.Itoa(port)))
			},
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.TargetURL, nil)
	if err != nil {
		return "", fmt.Errorf("tunnelprobe: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("tunnelprobe: fetch through the tunnel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tunnelprobe: %s answered %s", opts.TargetURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("tunnelprobe: read the answer: %w", err)
	}
	return string(body), nil
}

// config builds the temporary client: one SOCKS inbound on loopback, one VLESS outbound to the node's
// own listener through the same XHTTP transport a phone uses.
func config(socksPort int, opts Options) []byte {
	return []byte(fmt.Sprintf(`{
  "log": {"loglevel": "warning"},
  "inbounds": [{"listen": "127.0.0.1", "port": %d, "protocol": "socks", "settings": {"auth": "noauth"}}],
  "outbounds": [{
    "protocol": "vless",
    "settings": {"vnext": [{"address": "127.0.0.1", "port": %d, "users": [{"id": %q, "encryption": "none"}]}]},
    "streamSettings": {
      "network": "xhttp",
      "security": "none",
      "xhttpSettings": {"path": %q, "mode": "packet-up"}
    }
  }]
}`, socksPort, portOf(opts.ProxyAddr), opts.UUID, opts.Path))
}

func portOf(address string) int {
	_, raw, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return port
}

func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("tunnelprobe: pick a loopback port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// waitListener waits until the temporary client accepts connections. A core that exits instead of
// listening is reported as a failed probe, not as a timeout five seconds later.
func waitListener(ctx context.Context, address string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("tunnelprobe: the temporary core never listened on %s", address)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
