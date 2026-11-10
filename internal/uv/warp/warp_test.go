package warp_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/warp"
)

// fakeRunner records every command and answers from a table.
type fakeRunner struct {
	mu       sync.Mutex
	commands []string
	answers  map[string]answer
	afterSh  func()
}

type answer struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := name + " " + strings.Join(args, " ")
	f.commands = append(f.commands, key)
	if name == "sh" {
		// The install script is one shell invocation; its content is pinned by InstallScript.
		if f.afterSh != nil {
			f.afterSh()
		}
		return "", "", nil
	}
	reply, ok := f.answers[key]
	if !ok {
		return "", "", &warp.ExitError{Command: key, ExitCode: 127, Output: "no such command"}
	}
	return reply.stdout, reply.stderr, reply.err
}

func (f *fakeRunner) seen(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, command := range f.commands {
		if command == key {
			count++
		}
	}
	return count
}

func missingBinary(name string) error {
	return &os.PathError{Op: "fork/exec", Path: name, Err: os.ErrNotExist}
}

func TestHostCheck(t *testing.T) {
	t.Parallel()
	tun := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(tun, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tests := []struct {
		name     string
		host     warp.Host
		wantErr  bool
		wantWord string
	}{
		{"a linux host with a tun device", warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: tun}, false, ""},
		{"another system", warp.Host{GOOS: "windows", GOARCH: "amd64", TunPath: tun}, true, "only on Linux"},
		{"an architecture without a package", warp.Host{GOOS: "linux", GOARCH: "riscv64", TunPath: tun}, true, "no package"},
		{"a container without a tun device", warp.Host{GOOS: "linux", GOARCH: "arm64", TunPath: filepath.Join(t.TempDir(), "absent")}, true, "is missing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.host.Check()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Check() = %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				return
			}
			var unsupported *warp.Unsupported
			if !errors.As(err, &unsupported) {
				t.Fatalf("Check() = %T, want *warp.Unsupported", err)
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("Check() = %q, want it to mention %q", err, tc.wantWord)
			}
		})
	}
}

func TestParseState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		out  string
		want string
	}{
		{"Status update: Connected", "connected"},
		{"Status update: Disconnected", "disconnected"},
		{"Status update: Connecting", "connecting"},
		{"Registration missing", "disconnected"},
		{"", "unknown"},
		{"something new", "unknown"},
	}
	for _, tc := range tests {
		client := warp.New(nil)
		client.Runner = &fakeRunner{answers: map[string]answer{
			"/usr/bin/warp-cli --accept-tos status": {stdout: tc.out},
		}}
		status, err := client.Status(context.Background())
		if err != nil {
			t.Fatalf("Status(%q): %v", tc.out, err)
		}
		if status.State != tc.want {
			t.Errorf("Status(%q).State = %q, want %q", tc.out, status.State, tc.want)
		}
	}
}

func TestParseTrace(t *testing.T) {
	t.Parallel()
	trace := warp.ParseTrace("fl=1\nip=203.0.113.7\ncolo=AMS\nwarp=on\n")
	if trace.IP != "203.0.113.7" || trace.Colo != "AMS" || !trace.ThroughWarp() {
		t.Errorf("ParseTrace = %+v", trace)
	}
	if off := warp.ParseTrace("ip=198.51.100.9\nwarp=off\n"); off.ThroughWarp() {
		t.Error("warp=off was read as on")
	}
}

// fakeSocks serves one SOCKS5 handshake and then the given HTTP response. It records the requested
// host so a test can prove the name was forwarded rather than resolved locally.
func fakeSocks(t *testing.T, response string) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	requested := make(chan string, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				greeting := make([]byte, 3)
				if _, err := io.ReadFull(conn, greeting); err != nil {
					return
				}
				if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
					return
				}
				head := make([]byte, 4)
				if _, err := io.ReadFull(conn, head); err != nil {
					return
				}
				var host string
				switch head[3] {
				case 0x03:
					size := make([]byte, 1)
					if _, err := io.ReadFull(conn, size); err != nil {
						return
					}
					name := make([]byte, size[0])
					if _, err := io.ReadFull(conn, name); err != nil {
						return
					}
					host = string(name)
				case 0x01:
					raw := make([]byte, 4)
					if _, err := io.ReadFull(conn, raw); err != nil {
						return
					}
					host = net.IP(raw).String()
				default:
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(conn, port); err != nil {
					return
				}
				requested <- host
				if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 80}); err != nil {
					return
				}
				// Read the request before answering: a server that closes first turns a clean test
				// into a reset on Windows.
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
				}
				_, _ = io.WriteString(conn, response)
			}(conn)
		}
	}()
	return listener.Addr().String(), requested
}

func TestDialSocks5ForwardsTheName(t *testing.T) {
	t.Parallel()
	addr, requested := fakeSocks(t, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")
	conn, err := warp.DialSocks5(context.Background(), addr, "trace.invalid:80")
	if err != nil {
		t.Fatalf("DialSocks5: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: trace.invalid\r\n\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(reply), "hello") {
		t.Errorf("reply = %q", reply)
	}
	select {
	case host := <-requested:
		if host != "trace.invalid" {
			t.Errorf("the proxy was asked for %q, want the unresolved name", host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the proxy was never asked")
	}
}

func TestDialSocks5ReportsARefusal(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 3)
		_, _ = io.ReadFull(conn, buf)
		_, _ = conn.Write([]byte{0x05, 0x00})
		_, _ = io.ReadFull(conn, make([]byte, 4))
		size := make([]byte, 1)
		_, _ = io.ReadFull(conn, size)
		_, _ = io.ReadFull(conn, make([]byte, int(size[0])+2))
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	}()
	if _, err := warp.DialSocks5(context.Background(), listener.Addr().String(), "example.com:443"); err == nil {
		t.Fatal("DialSocks5 accepted a refused connection")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Errorf("error = %q, want the refusal", err)
	}
}

// Status is two facts: what warp-cli says, and what a real fetch through it reports.
func TestStatusUsesTheTrace(t *testing.T) {
	t.Parallel()
	body := "ip=203.0.113.7\ncolo=AMS\nwarp=on\n"
	addr, _ := fakeSocks(t, "HTTP/1.1 200 OK\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\nConnection: close\r\n\r\n"+body)
	client := warp.New(&fakeRunner{answers: map[string]answer{
		"/usr/bin/warp-cli --accept-tos status": {stdout: "Status update: Connected"},
	}})
	client.Socks = addr
	client.ProbeURL = "http://trace.invalid/trace"
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Healthy() {
		t.Fatalf("status is not healthy: %+v", status)
	}
	if !strings.Contains(status.Detail, "203.0.113.7") {
		t.Errorf("detail = %q, want the egress address", status.Detail)
	}

	// Connected but warp=off is a real, unhappy state.
	addr2, _ := fakeSocks(t, "HTTP/1.1 200 OK\r\nContent-Length: "+strconv.Itoa(len("ip=198.51.100.9\nwarp=off\n"))+"\r\nConnection: close\r\n\r\nip=198.51.100.9\nwarp=off\n")
	client.Socks = addr2
	status, err = client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Healthy() {
		t.Errorf("Connected with warp=off was reported healthy: %+v", status)
	}
	if !strings.Contains(status.Detail, "warp=off") {
		t.Errorf("detail = %q, want it to say the probe saw warp=off", status.Detail)
	}
}

func TestStatusSaysMissingWhenTheBinaryIsGone(t *testing.T) {
	t.Parallel()
	client := warp.New(&fakeRunner{answers: map[string]answer{
		"/usr/bin/warp-cli --accept-tos status": {err: missingBinary("/usr/bin/warp-cli")},
	}})
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != "missing" || status.Installed {
		t.Errorf("status = %+v, want missing", status)
	}
}

func TestEnsureRefusesAnUnsupportedHost(t *testing.T) {
	t.Parallel()
	client := warp.New(&fakeRunner{})
	client.Host = warp.Host{GOOS: "windows"}
	err := client.Ensure(context.Background())
	var unsupported *warp.Unsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Ensure = %v, want *warp.Unsupported", err)
	}
}

func TestEnsureConverges(t *testing.T) {
	t.Parallel()
	tun := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(tun, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runner := &fakeRunner{answers: map[string]answer{
		"/usr/bin/warp-cli --version":                      {err: missingBinary("/usr/bin/warp-cli")},
		"/usr/bin/warp-cli --accept-tos registration show": {err: &warp.ExitError{Command: "registration show", ExitCode: 1, Output: "Registration missing"}},
		"/usr/bin/warp-cli --accept-tos registration new":  {},
		"/usr/bin/warp-cli --accept-tos mode proxy":        {},
		"/usr/bin/warp-cli --accept-tos connect":           {},
		"/usr/bin/warp-cli --accept-tos status":            {stdout: "Status update: Connected"},
	}}
	runner.afterSh = func() { runner.answers["/usr/bin/warp-cli --version"] = answer{} }
	client := warp.New(runner)
	client.Host = warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: tun}
	if err := client.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, want := range []string{
		"/usr/bin/warp-cli --accept-tos registration new",
		"/usr/bin/warp-cli --accept-tos mode proxy",
		"/usr/bin/warp-cli --accept-tos connect",
	} {
		if runner.seen(want) != 1 {
			t.Errorf("%q ran %d times, want once", want, runner.seen(want))
		}
	}
	// The tun check came before any package work: a refused host installs nothing.
	if !strings.Contains(strings.Join(runner.commands, "\n"), "sh -c") {
		t.Error("Ensure did not install the package when warp-cli was missing")
	}
}

func TestEnsureWaitsForTheConnection(t *testing.T) {
	t.Parallel()
	tun := filepath.Join(t.TempDir(), "tun")
	if err := os.WriteFile(tun, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runner := &fakeRunner{answers: map[string]answer{
		"/usr/bin/warp-cli --version":                      {},
		"/usr/bin/warp-cli --accept-tos registration show": {},
		"/usr/bin/warp-cli --accept-tos mode proxy":        {},
		"/usr/bin/warp-cli --accept-tos connect":           {},
		"/usr/bin/warp-cli --accept-tos status":            {stdout: "Status update: Disconnected"},
	}}
	client := warp.New(runner)
	client.Host = warp.Host{GOOS: "linux", GOARCH: "amd64", TunPath: tun}
	client.Attempts = 3
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	err := client.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not connect") {
		t.Fatalf("Ensure = %v, want a timeout", err)
	}
	if runner.seen("/usr/bin/warp-cli --accept-tos status") != 3 {
		t.Errorf("status ran %d times, want 3", runner.seen("/usr/bin/warp-cli --accept-tos status"))
	}
}
