//go:build lab

package lab

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Environment variables the harness reads. They are the contract with the machine that runs the
// suite: nothing about the lab is compiled into the repository.
const (
	EnvMN         = "TERMIT_LAB_MN"
	EnvUV1        = "TERMIT_LAB_UV1"
	EnvUV2        = "TERMIT_LAB_UV2"
	EnvUV3        = "TERMIT_LAB_UV3"
	EnvSSHKey     = "TERMIT_LAB_SSH_KEY"
	EnvHostKeys   = "TERMIT_LAB_HOSTKEYS"
	EnvAdminToken = "TERMIT_LAB_MN_ADMIN_TOKEN"
	EnvPlink      = "TERMIT_LAB_PLINK"
	EnvPscp       = "TERMIT_LAB_PSCP"
)

// DefaultCommandTimeout bounds one remote command. A probe or a sync can be slow on a loaded box, but
// a command that hangs must fail the run instead of holding the suite forever.
const DefaultCommandTimeout = 3 * time.Minute

// Target is one machine the suite drives.
type Target struct {
	Name    string
	Host    string
	User    string
	HostKey string
}

// Lab is the wired set of machines plus the secrets and settings that belong to the whole run.
// Lab is one running lab. UV3 is optional: the mesh tests need a third node, and every other test
// works with the two the suite has always required.
type Lab struct {
	MN, UV1, UV2 *Machine
	// UV3 is nil when TERMIT_LAB_UV3 is unset; tests that need it skip instead of failing.
	UV3        *Machine
	AdminToken string
	DNSuffix   string
	ACME       string
}

// Machine is one lab host. Its methods are the only way the suite touches the lab.
type Machine struct {
	Name    string
	Host    string
	User    string
	HostKey string
	KeyPath string
	Plink   string
	Pscp    string

	token   string
	timeout time.Duration
}

// FromEnv builds the lab from the environment, skipping the test when it is incomplete. Skipping is
// deliberate: the acceptance suite is not part of the default `go test`, and a missing variable on a
// developer machine says nothing about the code.
func FromEnv(t *testing.T) *Lab {
	t.Helper()
	missing := make([]string, 0, 6)
	read := func(key string) string {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			missing = append(missing, key)
		}
		return value
	}
	mn := read(EnvMN)
	uv1 := read(EnvUV1)
	uv2 := read(EnvUV2)
	key := read(EnvSSHKey)
	hostKeys := read(EnvHostKeys)
	token := read(EnvAdminToken)
	// The third node is optional, so its absence is not "incomplete": the suite runs with two.
	uv3 := strings.TrimSpace(os.Getenv(EnvUV3))
	if len(missing) > 0 {
		t.Skipf("lab environment is incomplete (%s); see AGENTS.md §4.3", strings.Join(missing, ", "))
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("lab SSH key %q is not readable: %v", key, err)
	}

	pins, err := parseHostKeys(hostKeys)
	if err != nil {
		t.Fatalf("lab host keys: %v", err)
	}
	plink := envOr(EnvPlink, "plink")
	pscp := envOr(EnvPscp, "pscp")
	if _, err := exec.LookPath(plink); err != nil {
		t.Fatalf("lab transport %q is not installed: %v (set %s)", plink, err, EnvPlink)
	}
	if _, err := exec.LookPath(pscp); err != nil {
		t.Fatalf("lab transport %q is not installed: %v (set %s)", pscp, err, EnvPscp)
	}

	newMachine := func(name, spec, envName string) *Machine {
		user, host, err := splitTarget(spec)
		if err != nil {
			t.Fatalf("%s=%q: %v", envName, spec, err)
		}
		pin := pins[host]
		if pin == "" {
			t.Fatalf("no host-key pin for %q in %s", host, EnvHostKeys)
		}
		return &Machine{
			Name: name, Host: host, User: user, HostKey: pin,
			KeyPath: key, Plink: plink, Pscp: pscp,
			token: token, timeout: DefaultCommandTimeout,
		}
	}
	return &Lab{
		MN:         newMachine("mn", mn, EnvMN),
		UV1:        newMachine("uv1", uv1, EnvUV1),
		UV2:        newMachine("uv2", uv2, EnvUV2),
		UV3:        optionalMachine(t, newMachine, uv3),
		AdminToken: token,
		DNSuffix:   os.Getenv("TERMIT_LAB_DNS_SUFFIX"),
		ACME:       os.Getenv("TERMIT_LAB_ACME"),
	}
}

// optionalMachine builds a machine only when its target is configured. The third node is an
// enhancement, not a requirement: a lab with two UVs still runs the whole original suite.
func optionalMachine(t *testing.T, build func(name, spec, envName string) *Machine, spec string) *Machine {
	t.Helper()
	if spec == "" {
		return nil
	}
	return build("uv3", spec, EnvUV3)
}

// Run executes one command and returns its combined output. A non-zero remote exit is an error: the
// caller that expects one (a blocked node refusing a sync, for example) inspects the output and the
// error together.
func (m *Machine) Run(ctx context.Context, command string) (string, error) {
	timeout := m.timeout
	if timeout == 0 {
		timeout = DefaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-batch", "-i", m.KeyPath}
	if m.HostKey != "" {
		args = append(args, "-hostkey", m.HostKey)
	}
	args = append(args, m.User+"@"+m.Host, command)
	cmd := exec.CommandContext(ctx, m.Plink, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := m.sanitize(out.String())
	if ctx.Err() != nil {
		return text, fmt.Errorf("lab: %s: the command did not finish in %s: %w", m.Name, timeout, ctx.Err())
	}
	if err != nil {
		// The command itself can carry the admin token (MNAdmin passes it in the environment), so it
		// is redacted before it reaches a test log.
		return text, fmt.Errorf("lab: %s: %s: %w", m.Name, m.sanitize(command), err)
	}
	return text, nil
}

// MustRun fails the test with the machine's output when the command fails.
func (m *Machine) MustRun(t *testing.T, ctx context.Context, command string) string {
	t.Helper()
	out, err := m.Run(ctx, command)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// Upload copies one local file to the lab over pscp.
func (m *Machine) Upload(ctx context.Context, local, remote string) error {
	args := []string{"-batch", "-i", m.KeyPath}
	if m.HostKey != "" {
		args = append(args, "-hostkey", m.HostKey)
	}
	args = append(args, local, m.User+"@"+m.Host+":"+remote)
	cmd := exec.CommandContext(ctx, m.Pscp, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("lab: %s: upload %s: %w: %s", m.Name, local, err, m.sanitize(out.String()))
	}
	return nil
}

// WriteFile puts content at remote with the given mode. It is how a test installs a small script or a
// fixture without quoting it into a shell command.
func (m *Machine) WriteFile(ctx context.Context, content []byte, remote, mode string) error {
	dir, err := os.MkdirTemp("", "termit-lab-*")
	if err != nil {
		return fmt.Errorf("lab: %s: temp dir: %w", m.Name, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	local := filepath.Join(dir, "payload")
	if err := os.WriteFile(local, content, 0o600); err != nil {
		return fmt.Errorf("lab: %s: temp file: %w", m.Name, err)
	}
	if err := m.Upload(ctx, local, remote); err != nil {
		return err
	}
	if _, err := m.Run(ctx, "chmod "+mode+" "+remote); err != nil {
		return err
	}
	return nil
}

// MNAdmin runs a management CLI command with the admin token. The token is passed in the remote
// environment, never on a command line the harness logs, and every captured output is redacted.
func (l *Lab) MNAdmin(ctx context.Context, args ...string) (string, error) {
	command := "TERMIT_MN_ADMIN_TOKEN=" + shellQuote(l.AdminToken) + " termit-mn " + strings.Join(args, " ")
	return l.MN.Run(ctx, command)
}

// MustMNAdmin fails the test when the admin command fails.
func (l *Lab) MustMNAdmin(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := l.MNAdmin(ctx, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// Unit runs systemctl on the machine.
func (m *Machine) Unit(ctx context.Context, action string, units ...string) (string, error) {
	return m.Run(ctx, "systemctl "+action+" "+strings.Join(units, " "))
}

// WaitFor polls fn until it returns true or the timeout expires. The error of one attempt is returned
// as-is: a predicate that cannot answer (a missing binary) is a failure, not a reason to keep trying.
func WaitFor(ctx context.Context, timeout, every time.Duration, what string, fn func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := fn(ctx)
		if err != nil {
			return fmt.Errorf("lab: waiting for %s: %w", what, err)
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lab: %s did not happen within %s", what, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// sanitize removes the admin token from anything the harness may print.
func (m *Machine) sanitize(text string) string {
	if m.token != "" {
		text = strings.ReplaceAll(text, m.token, "<redacted>")
	}
	return text
}

// splitTarget parses "user@host".
func splitTarget(spec string) (user, host string, err error) {
	user, host, ok := strings.Cut(spec, "@")
	if !ok || user == "" || host == "" {
		return "", "", fmt.Errorf("want user@host, got %q", spec)
	}
	return user, host, nil
}

// parseHostKeys parses "host=SHA256:…,host=SHA256:…". A fingerprint is mandatory per host: an
// unpinned host would silently accept whichever machine answers.
func parseHostKeys(raw string) (map[string]string, error) {
	out := make(map[string]string)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		host, fingerprint, ok := strings.Cut(item, "=")
		if !ok || host == "" || fingerprint == "" {
			return nil, fmt.Errorf("want host=fingerprint, got %q", item)
		}
		out[host] = fingerprint
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no host-key pins")
	}
	return out, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// shellQuote single-quotes a value for the remote POSIX shell.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Tail returns the last n lines of text, for error messages.
func Tail(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
