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
	"reflect"
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
	// CertPin is the certificate the node currently serves, sent with the request so the server can
	// pin its probes to it. It is per call: the node renews the certificate while the agent runs.
	CertPin string
	// Timeout bounds one call. Defaults to 30s.
	Timeout time.Duration
}

// Fetch asks for the desired state of node keyed by agentKey. The request carries the local
// self-check when the caller has one; the server records it next to its own probe so an operator can
// tell local breakage from a broken path.
func (r Remote) Fetch(ctx context.Context, agentKey string, request proto.SyncRequest) (proto.SyncResponse, error) {
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
	if request.AgentVersion == "" {
		request.AgentVersion = proto.APIVersion
	}
	body, err := json.Marshal(request)
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
	if r.CertPin != "" {
		req.Header.Set(proto.HeaderCertSHA256, r.CertPin)
	}

	resp, err := client.Do(req)
	if err != nil {
		return proto.SyncResponse{}, fmt.Errorf("agent: sync with %s: %w", r.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var problem proto.Problem
		if err := json.NewDecoder(resp.Body).Decode(&problem); err == nil && problem.Code != "" {
			return proto.SyncResponse{}, &Refusal{Status: resp.StatusCode, Problem: problem}
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
	// Hot, when set, applies the client diff to the running core without restarting it.
	Hot HotUsers
	// Canary, when set, proves the new client set carries traffic before Apply reports success.
	Canary CanaryCheck
}

// Result is what applying a set changed.
type Result struct {
	Clients int
	Added   []string
	Removed []string
	// Unchanged is true when the file on disk already matched, so the core was not restarted.
	Unchanged bool
	// Hot is true when the diff went through the core's API and no restart was needed.
	Hot bool
	// HotNote explains why the API path was not used, for the operator and the log. Empty when it was
	// used or not attempted.
	HotNote string
	// CanaryRan is true when the new set was proven with a live connection.
	CanaryRan bool
}

// desired is the client set the core must end up with: the owner, the local devices, the server's probe
// credential and one pair credential per peer.
func (a Applier) desired(answer proto.SyncResponse, local []state.LocalUser) map[string]string {
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
	return desired
}

// spec turns the desired set into a generator input. The clients are sorted because the generated file
// must be identical for the same set: that is what makes "already in sync" a comparison instead of a
// restart.
func (a Applier) spec(answer proto.SyncResponse, local []state.LocalUser) (xray.Spec, error) {
	spec := xray.DefaultSpec()
	spec.Path = a.Path
	desired := a.desired(answer, local)
	ids := make([]string, 0, len(desired))
	for id := range desired {
		if id == "" {
			return spec, errors.New("agent: a client with an empty credential would make the core accept nobody")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		spec.Clients = append(spec.Clients, xray.Client{ID: id, Email: desired[id]})
	}
	return spec, nil
}

// Preview reports what Apply would change, without touching anything: no restart, no write, the same
// comparison. It is what a monitoring check runs. When the core API is wired, a live set that differs
// from the file counts as drift too: the file can be right while the core serves something else.
func (a Applier) Preview(ctx context.Context, answer proto.SyncResponse, local []state.LocalUser) (Result, error) {
	spec, err := a.spec(answer, local)
	if err != nil {
		return Result{}, err
	}
	config, err := xray.Generate(spec)
	if err != nil {
		return Result{}, err
	}
	desired := a.desired(answer, local)
	previous, _ := os.ReadFile(a.ConfigPath) // #nosec G304 -- a path from our own config.
	result := Compare(previous, config, desired)
	if a.Hot == nil {
		return result, nil
	}
	live, err := a.Hot.Users(ctx)
	if err != nil {
		// The running set is unknown: the report is about the file, and an unreadable API is not drift.
		result.HotNote = "cannot read the running client set: " + err.Error()
		return result, nil
	}
	if matches(desired, live) {
		return result, nil
	}
	add, remove := diffUsers(desired, live)
	result.Unchanged = false
	result.Added = emailsOf(add)
	result.Removed = append([]string(nil), remove...)
	sort.Strings(result.Added)
	sort.Strings(result.Removed)
	return result, nil
}

// Apply makes the core match the desired set and reports what changed.
//
// The order is the safety contract: the generated config is staged, validated with `xray -test`, and
// installed; the running core is then changed through its API, or by one validated restart when the
// API cannot do the job. A canary connection proves the result, and any failure falls back to the
// previous file and a restart, so a bad peer set cannot leave the node serving nobody.
func (a Applier) Apply(ctx context.Context, answer proto.SyncResponse, local []state.LocalUser) (Result, error) {
	if a.Binary == "" || a.ConfigPath == "" {
		return Result{}, errors.New("agent: the prober binary and config path are required")
	}

	desired := a.desired(answer, local)
	spec, err := a.spec(answer, local)
	if err != nil {
		return Result{}, err
	}
	config, err := xray.Generate(spec)
	if err != nil {
		return Result{}, err
	}
	previous, previousErr := os.ReadFile(a.ConfigPath) // #nosec G304 -- a path from our own config.
	result := Compare(previous, config, desired)

	// What the running core reports, when the API is reachable. The file is not the truth on its own:
	// an update the API applied but the file missed, or a hand edit, leaves the core serving something
	// else, and only the core can say so.
	var (
		live        []LiveUser
		liveChecked bool
		liveMatches bool
	)
	if a.Hot != nil {
		if users, err := a.Hot.Users(ctx); err == nil {
			live, liveChecked = users, true
			liveMatches = matches(desired, users)
		} else {
			result.HotNote = "cannot read the running client set: " + err.Error()
		}
	}

	if result.Unchanged && (!liveChecked || liveMatches) {
		// Nothing to change. The bytes are right and so is the core, or the core cannot be asked: an
		// unknown answer is never a reason to restart it.
		if a.Owner != "" && a.Runner != nil {
			// The bytes are right, but the ownership may not be: a file the core cannot read is the
			// kind of breakage that only shows up at the next restart.
			if _, err := a.Runner.Run(ctx, "chown", a.Owner, a.ConfigPath); err != nil {
				return Result{}, fmt.Errorf("agent: give %s to %s: %w", a.ConfigPath, a.Owner, err)
			}
		}
		return result, nil
	}

	staged := a.ConfigPath + ".new.json"
	if !result.Unchanged {
		if err := os.WriteFile(staged, config, 0o640); err != nil {
			return Result{}, fmt.Errorf("agent: write %s: %w", staged, err)
		}
		defer func() { _ = os.Remove(staged) }()
		// The core runs as its own user and reads this file through the group. A config it cannot read
		// is a config that fails at the next restart, hours later, with nothing in the log to explain it.
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
	} else if a.Runner != nil {
		// The file already describes the set; it is about to be restarted, so check it again.
		if _, err := a.Runner.Run(ctx, a.Binary, "run", "-test", "-config", a.ConfigPath); err != nil {
			return Result{}, fmt.Errorf("agent: the core rejects its own configuration: %w", err)
		}
	}

	hot := false
	switch {
	case a.Hot == nil:
		result.HotNote = "no core API is configured"
	case !liveChecked:
		// The note from the failed read is already set; the fallback is a restart.
	case !result.Unchanged && (previousErr != nil || !onlyClientsChanged(previous, config)):
		// The API can add and remove users, not move a path or change a stream. Applying half of such
		// a change would leave the file describing a core that is not running.
		result.HotNote = "the change touches more than the client set"
	default:
		add, remove := diffUsers(desired, live)
		if len(add) > 0 || len(remove) > 0 {
			// The live diff is the change that actually happens; the file diff can be empty when the
			// core drifted from a file that is already correct.
			result.Added = emailsOf(add)
			result.Removed = append([]string(nil), remove...)
			sort.Strings(result.Added)
			sort.Strings(result.Removed)
		}
		if len(add) == 0 && len(remove) == 0 {
			hot = true
		} else if err := a.applyHot(ctx, desired, add, remove); err != nil {
			result.HotNote = err.Error()
		} else {
			hot = true
		}
	}

	if !result.Unchanged {
		if err := os.Rename(staged, a.ConfigPath); err != nil {
			if hot {
				// The core already runs the new set and the file does not describe it. A restart would
				// silently revert to the old set, so it is not attempted here; the next cycle retries.
				return Result{}, fmt.Errorf("agent: the API applied the new set but %s could not be replaced: %w",
					a.ConfigPath, err)
			}
			return Result{}, fmt.Errorf("agent: install %s: %w", a.ConfigPath, err)
		}
	}
	result.Hot = hot

	if !hot && a.Runner != nil && a.Unit != "" {
		if err := a.restart(ctx); err != nil {
			restored, restoreErr := a.restore(ctx, previous, previousErr)
			return Result{}, rolloutError("agent: restart "+a.Unit, err, restored, restoreErr)
		}
	}

	if a.Canary != nil {
		result.CanaryRan = true
		if err := a.Canary.Check(ctx); err != nil {
			// A hot apply can leave a handler in a state only a restart repairs. The file already
			// describes the intended set, so one restart is the cheap fix before a rollback.
			if hot && a.Runner != nil && a.Unit != "" {
				if restartErr := a.restart(ctx); restartErr == nil {
					if again := a.Canary.Check(ctx); again == nil {
						return result, nil
					}
				}
			}
			restored, restoreErr := a.restore(ctx, previous, previousErr)
			return Result{}, rolloutError("agent: the canary failed", err, restored, restoreErr)
		}
	}
	return result, nil
}

// applyHot reconciles the running core through its API and proves the result from the core's own
// answer. An error means the API could not do the job; the caller falls back to a restart.
func (a Applier) applyHot(ctx context.Context, desired map[string]string, add []xray.Client, remove []string) error {
	if err := a.Hot.Remove(ctx, remove); err != nil {
		return err
	}
	if err := a.Hot.Add(ctx, add); err != nil {
		return err
	}
	after, err := a.Hot.Users(ctx)
	if err != nil {
		return err
	}
	if !matches(desired, after) {
		return fmt.Errorf("agent: the core reports %d client(s), not the desired %d%s",
			len(after), len(desired), mismatch(desired, after))
	}
	return nil
}

// mismatch names the emails that are missing or unexpected, without ever printing a credential. It
// is what turns "the core reports a different set" into something an operator can act on.
func mismatch(desired map[string]string, live []LiveUser) string {
	wanted := make(map[string]bool, len(desired))
	for _, email := range desired {
		wanted[email] = true
	}
	seen := make(map[string]bool, len(live))
	var missing, extra []string
	for _, user := range live {
		seen[user.Email] = true
		if !wanted[user.Email] {
			extra = append(extra, user.Email)
		}
	}
	for email := range wanted {
		if !seen[email] {
			missing = append(missing, email)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return " (the ids differ)"
	}
	out := ""
	if len(missing) > 0 {
		out += "; missing: " + strings.Join(missing, ", ")
	}
	if len(extra) > 0 {
		out += "; unexpected: " + strings.Join(extra, ", ")
	}
	return " (" + strings.TrimPrefix(out, "; ") + ")"
}

func (a Applier) restart(ctx context.Context) error {
	if a.Runner == nil || a.Unit == "" {
		return nil
	}
	_, err := a.Runner.Run(ctx, "systemctl", "restart", a.Unit)
	return err
}

// restore puts the previous configuration back and restarts the core. It reports whether the file was
// restored even when the restart failed: "the old config is back but the core did not come up" and
// "there is no old config" are different things, and an operator needs to know which one happened.
func (a Applier) restore(ctx context.Context, previous []byte, previousErr error) (bool, error) {
	if previousErr != nil {
		return false, fmt.Errorf("there was no previous configuration to restore: %w", previousErr)
	}
	if err := os.WriteFile(a.ConfigPath, previous, 0o640); err != nil {
		return false, fmt.Errorf("write the previous configuration: %w", err)
	}
	if err := a.restart(ctx); err != nil {
		return true, fmt.Errorf("restart after the rollback: %w", err)
	}
	return true, nil
}

// rolloutError renders the outcome of a failed change: the cause, and whether the node is back on the
// configuration it had before. The cause stays wrapped so a caller can still inspect it.
func rolloutError(prefix string, cause error, restored bool, restoreErr error) error {
	switch {
	case !restored:
		return fmt.Errorf("%s: %w (the previous configuration could not be restored: %v)", prefix, cause, restoreErr)
	case restoreErr != nil:
		return fmt.Errorf("%s: %w (the previous configuration was restored, but the core did not restart: %v)",
			prefix, cause, restoreErr)
	default:
		return fmt.Errorf("%s: %w (the previous configuration was restored)", prefix, cause)
	}
}

// Compare reports the difference between the file on disk and the generated configuration. The
// elements are emails, not credentials: an operator reads this output, and a UUID is a secret.
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
					ID    string `json:"id"`
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	byID := map[string]string{}
	if err := json.Unmarshal(previous, &old); err == nil {
		for _, inbound := range old.Inbounds {
			for _, client := range inbound.Settings.Clients {
				byID[strings.ToLower(client.ID)] = client.Email
			}
		}
	}
	wanted := make(map[string]bool, len(desired))
	for id, email := range desired {
		wanted[strings.ToLower(id)] = true
		if _, ok := byID[strings.ToLower(id)]; !ok {
			result.Added = append(result.Added, email)
		}
	}
	for id, email := range byID {
		if !wanted[id] {
			result.Removed = append(result.Removed, email)
		}
	}
	sort.Strings(result.Added)
	sort.Strings(result.Removed)
	return result
}

// onlyClientsChanged reports whether the two generated documents differ only in the VLESS client
// list. The API can add and remove users, but it cannot change a path, a port or a stream setting, so
// anything else has to go through a validated restart.
func onlyClientsChanged(previous, next []byte) bool {
	var before, after any
	if err := json.Unmarshal(previous, &before); err != nil {
		return false
	}
	if err := json.Unmarshal(next, &after); err != nil {
		return false
	}
	stripClients(before)
	stripClients(after)
	return reflect.DeepEqual(before, after)
}

// stripClients removes the client arrays from a decoded config, leaving every other field in place.
func stripClients(document any) {
	root, ok := document.(map[string]any)
	if !ok {
		return
	}
	inbounds, ok := root["inbounds"].([]any)
	if !ok {
		return
	}
	for _, raw := range inbounds {
		inbound, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		settings, ok := inbound["settings"].(map[string]any)
		if !ok {
			continue
		}
		delete(settings, "clients")
	}
}

// RememberSubscription stores the subscription URL the server hands out. A node enrolled by an older
// build, or one whose token was rotated on the server, has no URL in its state; the next sync repairs
// it, and `uv status` can show it again. It reports whether the stored value changed.
func RememberSubscription(st *state.State, answer proto.SyncResponse) bool {
	if st.Credentials == nil || answer.SubscriptionURL == "" {
		return false
	}
	if st.Credentials.SubscriptionURL == answer.SubscriptionURL {
		return false
	}
	st.Credentials.SubscriptionURL = answer.SubscriptionURL
	return true
}

// emailsOf names the clients an add request carries. Emails are the core's own identifiers and are
// safe to show; the UUID beside each is not.
func emailsOf(clients []xray.Client) []string {
	out := make([]string, 0, len(clients))
	for _, client := range clients {
		out = append(out, client.Email)
	}
	return out
}

func localEmail(user state.LocalUser) string {
	id := strings.ReplaceAll(user.ID, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return "local-" + id + "@termit"
}
