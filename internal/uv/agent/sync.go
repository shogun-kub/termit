// Package agent is the node's side of the mesh: it asks the management server what the peer set is and
// makes the local proxy core match it.
//
// The server's answer is a complete desired set, never a delta, so applying it is a convergence rather
// than a patch: the core ends up with exactly the credentials the answer names, no matter what the
// previous run left behind.
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// Remote talks to one management server.
type Remote struct {
	BaseURL string
	// CAFile is trusted instead of the system roots; empty means the system store.
	CAFile string
	// Timeout bounds one call. Defaults to 30s.
	Timeout time.Duration
}

// Fetch asks for the desired state of node keyed by agentKey.
func (r Remote) Fetch(ctx context.Context, agentKey string) (proto.SyncResponse, error) {
	if r.BaseURL == "" {
		return proto.SyncResponse{}, errors.New("agent: no management server is configured (mn.url)")
	}
	if agentKey == "" {
		return proto.SyncResponse{}, errors.New("agent: no agent key; enroll this node first")
	}
	client, err := r.client()
	if err != nil {
		return proto.SyncResponse{}, err
	}

	body, err := json.Marshal(proto.SyncRequest{AgentVersion: proto.APIVersion})
	if err != nil {
		return proto.SyncResponse{}, fmt.Errorf("agent: encode the sync request: %w", err)
	}
	endpoint := strings.TrimRight(r.BaseURL, "/") + "/api/v1/agent/sync"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return proto.SyncResponse{}, fmt.Errorf("agent: build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentKey)

	resp, err := client.Do(req)
	if err != nil {
		return proto.SyncResponse{}, fmt.Errorf("agent: sync with %s: %w", r.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var problem proto.Problem
		if err := json.NewDecoder(resp.Body).Decode(&problem); err == nil && problem.Code != "" {
			return proto.SyncResponse{}, fmt.Errorf("agent: the server refused the sync: %s (%s)", problem.Code, problem.Detail)
		}
		return proto.SyncResponse{}, fmt.Errorf("agent: the server answered %s", resp.Status)
	}

	var answer proto.SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return proto.SyncResponse{}, fmt.Errorf("agent: decode the sync answer: %w", err)
	}
	return answer, nil
}

func (r Remote) client() (*http.Client, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if r.CAFile != "" {
		pem, err := os.ReadFile(r.CAFile) // #nosec G304 -- the operator configured this path.
		if err != nil {
			return nil, fmt.Errorf("agent: read %s: %w", r.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("agent: %s holds no readable certificate", r.CAFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// Runner runs one command and returns its combined output. It is an interface so a test can watch the
// core being checked and restarted without a real Xray on the test machine.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Applier writes the core configuration for a desired set and reloads the core.
type Applier struct {
	// Binary is the xray executable; ConfigPath is the file it reads.
	Binary     string
	ConfigPath string
	// Path is the node's secret XHTTP path.
	Path string
	// SelfUUID is the owner's own credential on this node.
	SelfUUID string
	// SelfEmail is how the owner's device appears in the core log.
	SelfEmail string
	Runner    Runner
	// Owner is the file owner the core needs to read its config, "root:termit" on a real node. An
	// empty value skips the chown, which is what tests want.
	Owner string
	// Unit is the systemd unit to restart; empty means the core is not managed here.
	Unit string
}

// Result is what applying a set changed.
type Result struct {
	Clients int
	Added   []string
	Removed []string
	// Unchanged is true when the file on disk already matched, so the core was not restarted.
	Unchanged bool
}

// Apply makes the core match the desired set and reports what changed.
func (a Applier) Apply(ctx context.Context, answer proto.SyncResponse, local []state.LocalUser) (Result, error) {
	if a.Binary == "" || a.ConfigPath == "" {
		return Result{}, errors.New("agent: the prober binary and config path are required")
	}

	spec := xray.DefaultSpec()
	spec.Path = a.Path
	desired := map[string]string{a.SelfUUID: a.SelfEmail}
	for _, user := range local {
		desired[user.ID] = localEmail(user)
	}
	// The probe credential is the server's own key to this node: it must keep working or the node
	// drops out of the mesh at its next check.
	if answer.Self.ProbeUUID != "" {
		desired[answer.Self.ProbeUUID] = "probe@termit"
	}
	for _, peer := range answer.Peers {
		desired[peer.UUID] = "peer-" + peer.ID + "@termit"
	}
	// Sorted, because the generated file must be identical for the same set: that is what makes
	// "already in sync" a comparison instead of a restart.
	ids := make([]string, 0, len(desired))
	for id := range desired {
		if id == "" {
			return Result{}, errors.New("agent: a client with an empty credential would make the core accept nobody")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		spec.Clients = append(spec.Clients, xray.Client{ID: id, Email: desired[id]})
	}

	config, err := xray.Generate(spec)
	if err != nil {
		return Result{}, err
	}
	previous, previousErr := os.ReadFile(a.ConfigPath) // #nosec G304 -- a path from our own config.
	result := Compare(previous, config, desired)

	if result.Unchanged {
		// The bytes are right, but the ownership may not be: a file the core cannot read is the kind
		// of breakage that only shows up at the next restart. Repairing it costs one exec.
		if a.Owner != "" && a.Runner != nil {
			if _, err := a.Runner.Run(ctx, "chown", a.Owner, a.ConfigPath); err != nil {
				return Result{}, fmt.Errorf("agent: give %s to %s: %w", a.ConfigPath, a.Owner, err)
			}
		}
		return result, nil
	}

	staged := a.ConfigPath + ".new.json"
	if err := os.WriteFile(staged, config, 0o640); err != nil {
		return Result{}, fmt.Errorf("agent: write %s: %w", staged, err)
	}
	defer func() { _ = os.Remove(staged) }()

	// The core runs as its own user and reads this file through the group. A config it cannot read is
	// a config that fails at the next restart, hours later, with nothing in the log to explain it.
	if a.Owner != "" && a.Runner != nil {
		if _, err := a.Runner.Run(ctx, "chown", a.Owner, staged); err != nil {
			return Result{}, fmt.Errorf("agent: give %s to %s: %w", staged, a.Owner, err)
		}
	}
	if a.Runner != nil {
		if _, err := a.Runner.Run(ctx, a.Binary, "run", "-test", "-config", staged); err != nil {
			return Result{}, fmt.Errorf("agent: the core rejected the new configuration: %w", err)
		}
	}
	if err := os.Rename(staged, a.ConfigPath); err != nil {
		return Result{}, fmt.Errorf("agent: install %s: %w", a.ConfigPath, err)
	}
	if a.Runner != nil && a.Unit != "" {
		if _, err := a.Runner.Run(ctx, "systemctl", "restart", a.Unit); err != nil {
			// The core refused the file at runtime. Put the old one back, restart it, and report both
			// failures: the node keeps serving instead of disappearing from the mesh.
			if previousErr == nil {
				if restoreErr := os.WriteFile(a.ConfigPath, previous, 0o640); restoreErr == nil {
					_, _ = a.Runner.Run(ctx, "systemctl", "restart", a.Unit)
					return Result{}, fmt.Errorf("agent: restart %s: %w (the previous configuration was restored)", a.Unit, err)
				}
			}
			return Result{}, fmt.Errorf("agent: restart %s: %w (the previous configuration could not be restored)", a.Unit, err)
		}
	}
	return result, nil
}

// Compare reports the difference between the file on disk and the generated configuration.
func Compare(previous, next []byte, desired map[string]string) Result {
	result := Result{Clients: len(desired)}
	if bytes.Equal(previous, next) {
		result.Unchanged = true
		return result
	}
	// A previous file that does not parse is not an error: the node is about to be corrected.
	var old struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct {
					ID string `json:"id"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	had := map[string]bool{}
	if err := json.Unmarshal(previous, &old); err == nil {
		for _, inbound := range old.Inbounds {
			for _, client := range inbound.Settings.Clients {
				had[strings.ToLower(client.ID)] = true
			}
		}
	}
	for id := range desired {
		if !had[strings.ToLower(id)] {
			result.Added = append(result.Added, id)
		}
	}
	for id := range had {
		if _, ok := desired[id]; !ok {
			if _, ok := desired[strings.ToUpper(id)]; !ok {
				result.Removed = append(result.Removed, id)
			}
		}
	}
	return result
}

func localEmail(user state.LocalUser) string {
	id := strings.ReplaceAll(user.ID, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return "local-" + id + "@termit"
}
