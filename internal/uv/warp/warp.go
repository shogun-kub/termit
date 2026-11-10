// Package warp runs Cloudflare WARP in proxy mode, which is the node's default egress.
//
// The reason WARP exists here is probing, not privacy theatre. An application on a client's device
// routinely asks the internet "what address am I coming from, and is this a proxy?"; with direct
// egress the answer names the node, and a node with a name is a node that can be blocked. WARP puts a
// shared Cloudflare address in front of that answer.
//
// The traffic path is narrow on purpose. warp-cli runs in proxy mode and listens on a loopback
// SOCKS5 port; only Xray sends traffic to it. The host's own traffic — SSH, the decoy site, the ACME
// challenge — never enters WARP, and nothing in this package touches the host's routing table or
// firewall. That is the invariant from docs/DESCRIPTION.md §11, not an implementation detail.
//
// WARP needs /dev/net/tun. A container without it cannot run WARP at all, so the package has an
// explicit refusal path: the node says why it is not using WARP and keeps working without it, rather
// than reporting a healthy tunnel that does not exist.
package warp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	// SocksAddress is warp-cli's proxy-mode listener. Xray is the only client.
	SocksAddress = "127.0.0.1:40000"
	// TraceURL answers with the address, and whether WARP is on, as Cloudflare sees it.
	TraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	// DefaultBinary is where the Cloudflare package puts the client.
	DefaultBinary = "/usr/bin/warp-cli"
	// TunPath is the device WARP cannot work without.
	TunPath = "/dev/net/tun"
	// TraceTimeout bounds one end-to-end check through the sidecar.
	TraceTimeout = 15 * time.Second
	// ConnectAttempts and ConnectInterval bound the wait after `warp-cli connect`.
	ConnectAttempts = 30
	ConnectInterval = time.Second
)

// Unsupported is the refusal: this host cannot run WARP, and the reason is meant for the operator.
// It is a fact about the host, not a failure of the node.
type Unsupported struct {
	Reason string
}

func (e *Unsupported) Error() string { return "warp is not available here: " + e.Reason }

// Host is the platform facts availability depends on. The zero value means this machine, so tests
// can describe a host without pretending to be one.
type Host struct {
	GOOS    string
	GOARCH  string
	TunPath string
}

// Check reports nil when WARP can run, or an *Unsupported naming what is missing.
func (h Host) Check() error {
	if h.GOOS == "" {
		h.GOOS = runtime.GOOS
	}
	if h.GOARCH == "" {
		h.GOARCH = runtime.GOARCH
	}
	if h.TunPath == "" {
		h.TunPath = TunPath
	}
	if h.GOOS != "linux" {
		return &Unsupported{Reason: "the Cloudflare WARP client runs only on Linux"}
	}
	switch h.GOARCH {
	case "amd64", "arm64":
	default:
		return &Unsupported{Reason: fmt.Sprintf("Cloudflare WARP has no package for %s", h.GOARCH)}
	}
	if _, err := os.Stat(h.TunPath); err != nil {
		return &Unsupported{Reason: fmt.Sprintf(
			"%s is missing: a container needs --device %s (an unprivileged LXC cannot create it)",
			h.TunPath, h.TunPath)}
	}
	return nil
}

// Available checks this machine.
func Available() error { return Host{}.Check() }

// Runner runs one command and returns its output. The installer and the CLI adapt their own
// executors to it, so this package owns no process handling.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// Client is the warp-cli wrapper.
type Client struct {
	Binary   string
	Runner   Runner
	Socks    string
	ProbeURL string
	Sleep    func(ctx context.Context, d time.Duration) error
	// Attempts and Interval bound the wait for a freshly connected tunnel.
	Attempts int
	Interval time.Duration
	// Host is the machine availability is judged on; the zero value is this machine. Tests set it
	// instead of pretending the host has a tun device.
	Host Host
}

// New returns a client for this machine.
func New(runner Runner) Client {
	return Client{
		Binary:   DefaultBinary,
		Runner:   runner,
		Socks:    SocksAddress,
		ProbeURL: TraceURL,
		Sleep:    Sleep,
		Attempts: ConnectAttempts,
		Interval: ConnectInterval,
	}
}

// Sleep waits for d or until the context ends.
func Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Trace is the Cloudflare answer: the address WARP presents and whether WARP is on.
type Trace struct {
	IP     string
	Warp   string
	Colo   string
	Fields map[string]string
}

// ThroughWarp reports whether Cloudflare saw the request arrive through WARP.
func (t Trace) ThroughWarp() bool { return t.Warp == "on" }

// Status is one local check. It is deliberately two facts side by side: what warp-cli says about
// itself, and what an end-to-end fetch through it actually reports. "Connected" plus warp=off is a
// real state (WARP is on but Cloudflare did not attribute the request to it) and must not be
// flattened into "healthy".
type Status struct {
	Installed bool
	State     string // connected, disconnected, connecting, missing, unknown
	Raw       string
	Trace     *Trace
	Detail    string
}

// Healthy reports a connected client whose own probe says warp=on.
func (s Status) Healthy() bool {
	return s.Installed && s.State == "connected" && s.Trace != nil && s.Trace.ThroughWarp()
}

// LocalStatus asks warp-cli only. It is what a check uses: a check runs on every install and must
// not depend on the network.
func (c Client) LocalStatus(ctx context.Context) (Status, error) {
	out, err := c.cli(ctx, "--accept-tos", "status")
	if err != nil {
		if isMissingBinary(err) {
			return Status{State: "missing", Detail: "warp-cli is not installed"}, nil
		}
		return Status{State: "unknown", Detail: err.Error()}, err
	}
	st := Status{Installed: true, State: parseState(out), Raw: strings.TrimSpace(out)}
	if st.State != "connected" {
		st.Detail = "warp-cli reports " + st.State
	}
	return st, nil
}

// Status asks warp-cli and then fetches the trace through the sidecar.
func (c Client) Status(ctx context.Context) (Status, error) {
	st, err := c.LocalStatus(ctx)
	if err != nil || !st.Installed || st.State != "connected" {
		return st, err
	}
	trace, err := c.Trace(ctx)
	if err != nil {
		st.Detail = "the sidecar is connected but the probe failed: " + err.Error()
		return st, nil
	}
	st.Trace = &trace
	switch {
	case trace.ThroughWarp():
		st.Detail = "via " + trace.IP
		if trace.Colo != "" {
			st.Detail += " (" + trace.Colo + ")"
		}
	default:
		st.Detail = "the probe says warp=off at " + trace.IP
	}
	return st, nil
}

// Trace fetches the Cloudflare trace through the loopback SOCKS5 proxy. It therefore tests the path
// the node's traffic actually takes, not the existence of a listener.
func (c Client) Trace(ctx context.Context) (Trace, error) {
	ctx, cancel := context.WithTimeout(ctx, TraceTimeout)
	defer cancel()
	socks := c.Socks
	if socks == "" {
		socks = SocksAddress
	}
	url := c.ProbeURL
	if url == "" {
		url = TraceURL
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return DialSocks5(ctx, socks, address)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Trace{}, err
	}
	response, err := (&http.Client{Transport: transport, Timeout: TraceTimeout}).Do(request)
	if err != nil {
		return Trace{}, err
	}
	defer response.Body.Close() //nolint:errcheck // read-only body.
	if response.StatusCode != http.StatusOK {
		return Trace{}, fmt.Errorf("the trace endpoint answered %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return Trace{}, err
	}
	trace := ParseTrace(string(body))
	if trace.IP == "" {
		return Trace{}, errors.New("the trace endpoint answered without an address")
	}
	return trace, nil
}

// ParseTrace reads the key=value document cdn-cgi/trace returns.
func ParseTrace(body string) Trace {
	trace := Trace{Fields: map[string]string{}}
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key == "" {
			continue
		}
		trace.Fields[key] = value
	}
	trace.IP = trace.Fields["ip"]
	trace.Warp = trace.Fields["warp"]
	trace.Colo = trace.Fields["colo"]
	return trace
}

// parseState turns warp-cli's free-form status line into one of a few words. Whole words only:
// "disconnected" contains "connected", and a substring search would report a tunnel that is down as
// up.
func parseState(out string) string {
	line := strings.ToLower(out)
	switch {
	case containsWord(line, "disconnected"):
		return "disconnected"
	case containsWord(line, "connecting"):
		return "connecting"
	case containsWord(line, "connected"):
		return "connected"
	case containsWord(line, "registration"), containsWord(line, "missing"):
		return "disconnected"
	case strings.TrimSpace(out) == "":
		return "unknown"
	default:
		return "unknown"
	}
}

// isWordRune reports whether r can appear inside a status word. parseState lowercases first, so the
// alphabet is lowercase letters and digits; everything else separates words.
func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

func containsWord(text, word string) bool {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return !isWordRune(r)
	}) {
		if field == word {
			return true
		}
	}
	return false
}

func (c Client) binary() string {
	if c.Binary == "" {
		return DefaultBinary
	}
	return c.Binary
}

// cli runs warp-cli and returns its combined output.
func (c Client) cli(ctx context.Context, args ...string) (string, error) {
	out, err := c.exec(ctx, c.binary(), args...)
	if err != nil {
		return out, fmt.Errorf("warp-cli %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func (c Client) exec(ctx context.Context, name string, args ...string) (string, error) {
	if c.Runner == nil {
		return "", errors.New("warp: no command runner")
	}
	stdout, stderr, err := c.Runner.Run(ctx, name, args...)
	return strings.TrimSpace(stdout + "\n" + stderr), err
}

// ExitError reports a command that ran and exited non-zero. A missing binary is a different
// condition and is never reported as an exit error.
type ExitError struct {
	Command  string
	ExitCode int
	Output   string
}

func (e *ExitError) Error() string {
	message := fmt.Sprintf("%s exited %d", e.Command, e.ExitCode)
	if e.Output != "" {
		message += ": " + e.Output
	}
	return message
}

// isMissingBinary reports whether err means the binary was never there.
//
// Both shapes are real: the installer's executor wraps whatever the process launch returned
// (*exec.Error from LookPath, which carries exec.ErrNotFound), and a caller that looked the path up
// itself produces *os.PathError. A host without the package is a normal state — "warp-cli is not
// installed" — not a failure of the node, so it must not surface as an error from `uv warp status`.
func isMissingBinary(err error) bool {
	var pathErr *os.PathError
	var execErr *exec.Error
	if errors.As(err, &pathErr) || errors.As(err, &execErr) {
		return true
	}
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}
