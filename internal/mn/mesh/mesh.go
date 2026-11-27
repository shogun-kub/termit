// Package mesh is one management server's side of replication with another: the credential peers present
// to each other, and the loop that pulls a peer's journal (docs/IMPLEMENTATION_PLAN.md §5.2.1). The store
// already knows how to export, apply and refuse a journal (internal/mn/store); this package moves it
// between two hosts and nothing else.
package mesh

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// pullTokenLabel gives the derived token a domain of its own, so it can never be mistaken for another use
// of the master key (the subscription-token sealing, the admin key) even by accident.
const pullTokenLabel = "termit/mn/mesh/pull/v1"

// PullToken is the credential one management server presents to another when it reads the journal. It is
// derived from the master key rather than stored: every server in a mesh holds that key by design
// (§5.2.1 decision 1), so a second secret would be one more copy to move and rotate without drawing a
// boundary the key does not already draw. The pull endpoints are read-only, so possessing the token
// grants reading the registry and nothing else.
func PullToken(masterKey []byte) string {
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte(pullTokenLabel))
	return hex.EncodeToString(mac.Sum(nil))
}

// CheckPullToken reports whether presented is this server's pull token. The comparison is constant time
// because the value is a credential a remote chooses.
func CheckPullToken(masterKey []byte, presented string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(PullToken(masterKey))) == 1
}

// ClientFor builds the HTTP client for one peer: pinned to its leaf certificate when the configuration
// names a pin, and otherwise trusting the system's roots.
func ClientFor(peer config.Peer, timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if peer.LeafSHA256 != "" {
		pin, err := config.ParsePin(peer.LeafSHA256)
		if err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		transport.TLSClientConfig = &tls.Config{
			// #nosec G402 -- the pin below is the check, and it is stricter than a name: it holds
			// whether or not the certificate chain validates, which is the point of a pin.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("mesh: the peer presented no certificate")
				}
				sum := sha256.Sum256(rawCerts[0])
				if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
					return errors.New("mesh: the peer's certificate is not the pinned one")
				}
				return nil
			},
		}
	}
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// DefaultRequestTimeout bounds one peer request: a snapshot of a small registry crosses a LAN in well
// under it, and a peer that stops answering must not hold the loop past its own interval.
const DefaultRequestTimeout = 30 * time.Second

// batch is how many journal entries one request asks for.
const batch = 200

// rounds bounds one peer's catch-up, so a peer that keeps answering cannot hold the loop forever. A
// backlog larger than batch*rounds is a snapshot's job, and the next tick will notice the gap.
const rounds = 100

// Events is one page of a peer's journal. LastSeq is where the peer's log ended when the page was read, so
// the puller learns how far behind it is without a second request, and status can show it later.
type Events struct {
	MNID    string                   `json:"mn_id"`
	LastSeq int64                    `json:"last_seq"`
	Events  []store.ReplicationEvent `json:"events"`
}

// PeerIDKey and PeerSeqKey are the settings keys the puller leaves behind for `termit-mn status`: the mesh
// id a peer taught this server, and the newest entry of that peer's journal as of the last pull.
func PeerIDKey(name string) string  { return "mesh.peer." + name }
func PeerSeqKey(name string) string { return "mesh.peer." + name + ".seq" }

// Puller brings this server up to date with the peers in its configuration.
type Puller struct {
	Store     *store.Store
	Mesh      config.Mesh
	MasterKey []byte
	Logger    *slog.Logger
	// Client overrides the client built from the peer's pin. A test points it at an httptest server; nil
	// means one client per peer, built by ClientFor.
	Client *http.Client
}

// Run pulls every peer once and then on the configured interval until the context ends, handing back the
// reports a node left here while its home was away.
func (p *Puller) Run(ctx context.Context) {
	every := p.Mesh.PullEveryInterval()
	for {
		p.pullAll(ctx)
		p.DeliverReports(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// DeliverReports hands each queued self-report to the server that homes it, so a node that synced here
// while its home was down loses nothing. A report is dropped once the home applied it, kept while the home
// is unreachable, and dropped when the home says it will never be applied — the queue is one row per node,
// so it cannot grow while a home is away.
func (p *Puller) DeliverReports(ctx context.Context) {
	reports, err := p.Store.PendingReports(ctx)
	if err != nil {
		p.Logger.Warn("mesh: read the queued reports", "err", err)
		return
	}
	if len(reports) == 0 {
		return
	}
	byHome := make(map[string]config.Peer, len(p.Mesh.Peers))
	for _, peer := range p.Mesh.Peers {
		if id, ok, err := p.peerID(ctx, peer); err == nil && ok {
			byHome[id] = peer
		}
	}
	for _, report := range reports {
		if report.Home == "" {
			// The node row is gone: nothing to deliver, and nothing will ever own it again.
			p.dropReport(ctx, report.UV)
			continue
		}
		peer, ok := byHome[report.Home]
		if !ok {
			// Its home is not a configured peer (yet): keep the report and try on the next cycle.
			continue
		}
		applied, err := p.postReport(ctx, peer, report.Payload)
		switch {
		case errors.Is(err, errReportLoses):
			p.dropReport(ctx, report.UV)
		case err != nil:
			p.Logger.Warn("mesh: report not delivered", "uv", report.UV, "peer", peer.Name, "err", err)
		default:
			p.dropReport(ctx, report.UV)
			p.Logger.Info("mesh: report delivered", "uv", report.UV, "peer", peer.Name, "applied", applied)
		}
	}
}

func (p *Puller) dropReport(ctx context.Context, uvID string) {
	if err := p.Store.DropReport(ctx, uvID); err != nil {
		p.Logger.Warn("mesh: drop a queued report", "uv", uvID, "err", err)
	}
}

// postReport sends one queued report and reports whether the home applied it.
func (p *Puller) postReport(ctx context.Context, cfg config.Peer, payload []byte) (bool, error) {
	peer, err := p.talkTo(cfg)
	if err != nil {
		return false, err
	}
	var answer struct {
		Applied bool `json:"applied"`
	}
	if err := peer.post(ctx, "/peer/report", payload, &answer); err != nil {
		return false, err
	}
	return answer.Applied, nil
}

func (p *Puller) pullAll(ctx context.Context) {
	for _, peer := range p.Mesh.Peers {
		if ctx.Err() != nil {
			return
		}
		applied, err := p.Pull(ctx, peer)
		switch {
		case err != nil && ctx.Err() == nil:
			// A peer that is down must not stop the others, and it must not stop this server: the next
			// tick tries again, and status is where the operator sees that it has been a while.
			p.Logger.Warn("mesh: pull failed", "peer", peer.Name, "err", err)
		case applied > 0:
			p.Logger.Info("mesh: pulled", "peer", peer.Name, "applied", applied)
		}
	}
}

// Pull brings this server up to date with one peer and returns how many journal entries it applied. The
// first call takes a snapshot — there is no cursor, and the journal cannot say what happened before it
// existed — and every later call reads the journal past the cursor until the peer has nothing newer.
func (p *Puller) Pull(ctx context.Context, peer config.Peer) (int, error) {
	mnID, known, err := p.peerID(ctx, peer)
	if err != nil {
		return 0, err
	}
	if !known {
		return p.takeSnapshot(ctx, peer)
	}
	applied := 0
	for round := 0; round < rounds; round++ {
		cursor, err := p.Store.Cursor(ctx, mnID)
		if err != nil {
			return applied, fmt.Errorf("mesh: read the cursor for %s: %w", peer.Name, err)
		}
		page, err := p.events(ctx, peer, cursor)
		if err != nil {
			return applied, err
		}
		if err := p.Store.SetSetting(ctx, PeerSeqKey(peer.Name), strconv.FormatInt(page.LastSeq, 10)); err != nil {
			return applied, fmt.Errorf("mesh: record the position of %s: %w", peer.Name, err)
		}
		if len(page.Events) == 0 {
			return applied, nil
		}
		n, err := p.Store.ApplyEvents(ctx, page.Events)
		if err != nil {
			// A gap means the peer pruned the journal past the cursor. A snapshot is the only way across
			// a hole, and it is safe: the rows on both sides are the same rows.
			if errors.Is(err, store.ErrReplicationGap) {
				snapped, snapErr := p.takeSnapshot(ctx, peer)
				return applied + snapped, snapErr
			}
			return applied, fmt.Errorf("mesh: apply from %s: %w", peer.Name, err)
		}
		applied += n
		if len(page.Events) < batch {
			return applied, nil
		}
	}
	return applied, fmt.Errorf("mesh: %s is still behind after %d rounds", peer.Name, rounds)
}

// peerID reads the mesh id learned from this peer's last snapshot. It is what the journal is keyed by on
// our side, and it is why two peers cannot share a name.
func (p *Puller) peerID(ctx context.Context, peer config.Peer) (string, bool, error) {
	mnID, err := p.Store.GetSetting(ctx, PeerIDKey(peer.Name))
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("mesh: read the id of %s: %w", peer.Name, err)
	}
	if mnID == "" {
		return "", false, nil
	}
	return mnID, true, nil
}

// takeSnapshot replaces this server's copy of the peer's nodes with the peer's own.
func (p *Puller) takeSnapshot(ctx context.Context, peer config.Peer) (int, error) {
	snap, err := p.fetchSnapshot(ctx, peer)
	if err != nil {
		return 0, err
	}
	if err := p.Store.ApplySnapshot(ctx, snap); err != nil {
		return 0, fmt.Errorf("mesh: apply the snapshot from %s: %w", peer.Name, err)
	}
	if err := p.Store.SetSetting(ctx, PeerIDKey(peer.Name), snap.MNID); err != nil {
		return 0, fmt.Errorf("mesh: remember %s: %w", peer.Name, err)
	}
	return len(snap.Nodes), nil
}

func (p *Puller) fetchSnapshot(ctx context.Context, cfg config.Peer) (store.Snapshot, error) {
	peer, err := p.talkTo(cfg)
	if err != nil {
		return store.Snapshot{}, err
	}
	var snap store.Snapshot
	if err := peer.get(ctx, "/peer/snapshot", nil, &snap); err != nil {
		return store.Snapshot{}, err
	}
	return snap, nil
}

func (p *Puller) events(ctx context.Context, cfg config.Peer, after int64) (Events, error) {
	peer, err := p.talkTo(cfg)
	if err != nil {
		return Events{}, err
	}
	query := url.Values{
		"after": {strconv.FormatInt(after, 10)},
		"limit": {strconv.Itoa(batch)},
	}
	var page Events
	if err := peer.get(ctx, "/peer/events", query, &page); err != nil {
		return Events{}, err
	}
	return page, nil
}

// talkTo builds the peer handle for one call. A test supplies the client; production pins the leaf.
func (p *Puller) talkTo(cfg config.Peer) (Peer, error) {
	if p.Client != nil {
		return Peer{Config: cfg, MasterKey: p.MasterKey, HTTP: p.Client}, nil
	}
	return NewPeer(cfg, p.MasterKey, DefaultRequestTimeout)
}

// Peer is one configured server plus the credential and the client needed to talk to it. The puller builds a
// handle per peer per cycle; the admin socket builds one to hand a node over.
type Peer struct {
	Config    config.Peer
	MasterKey []byte
	HTTP      *http.Client
}

// NewPeer prepares a client for one peer: pinned to its leaf when the configuration names a pin.
func NewPeer(cfg config.Peer, masterKey []byte, timeout time.Duration) (Peer, error) {
	client, err := ClientFor(cfg, timeout)
	if err != nil {
		return Peer{}, err
	}
	return Peer{Config: cfg, MasterKey: masterKey, HTTP: client}, nil
}

// State is what a peer answers about itself: its identity and how far it applied every log it follows.
type State struct {
	MNID    string           `json:"mn_id"`
	Applied map[string]int64 `json:"applied"`
}

// Handoff is the answer to "take this node": the home term that now orders the row, and the agent key the
// node has to be given before it can talk to its new home.
type Handoff struct {
	Term     int64  `json:"term"`
	AgentKey string `json:"agent_key"`
}

// State reads the peer's progress, which is what a handoff checks its freshness against.
func (p Peer) State(ctx context.Context) (State, error) {
	var state State
	if err := p.get(ctx, "/peer/state", nil, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

// Handoff asks the peer to take a node this server homes, naming this server as the one handing it over.
func (p Peer) Handoff(ctx context.Context, uvID, from string) (Handoff, error) {
	body, err := json.Marshal(map[string]string{"uv": uvID, "from": from})
	if err != nil {
		return Handoff{}, fmt.Errorf("mesh: encode the handoff: %w", err)
	}
	var answer Handoff
	if err := p.post(ctx, "/peer/handoff", body, &answer); err != nil {
		return Handoff{}, err
	}
	return answer, nil
}

// maxAnswer bounds one response body. A snapshot of a thousand nodes is a few hundred kilobytes; this is
// the point past which the answer is not this protocol's.
const maxAnswer = 32 << 20

func (p Peer) get(ctx context.Context, path string, query url.Values, into any) error {
	target := strings.TrimRight(p.Config.URL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", p.Config.Name, err)
	}
	return p.send(request, path, into)
}

// post sends one request that writes: a queued report, a handoff. The receiver fences both by the row's
// home, so a wrong target is refused rather than redirected.
func (p Peer) post(ctx context.Context, path string, body []byte, into any) error {
	target := strings.TrimRight(p.Config.URL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", p.Config.Name, err)
	}
	request.Header.Set("Content-Type", "application/json")
	return p.send(request, path, into)
}

func (p Peer) send(request *http.Request, path string, into any) error {
	request.Header.Set("Authorization", "Bearer "+PullToken(p.MasterKey))
	response, err := p.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", p.Config.Name, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return p.refusal(response)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAnswer)).Decode(into); err != nil {
		return fmt.Errorf("mesh: %s: read %s: %w", p.Config.Name, path, err)
	}
	return nil
}

// errReportLoses marks an answer that says the report will never be applied: the node is unknown there, or
// it is homed by somebody else. Retrying it would keep a dead entry in the queue for ever.
var errReportLoses = errors.New("mesh: the report will not be applied")

// refusal turns a peer's answer into something an operator can act on: the two mistakes this protocol has
// are a wrong key and a URL that does not carry the prefix, and both deserve to be named.
func (p Peer) refusal(response *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	name := p.Config.Name
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("mesh: %s refused the mesh credential: the two servers do not share a master key", name)
	case http.StatusNotFound:
		return fmt.Errorf("mesh: %s has no replication endpoint: the url must carry the peer's API prefix", name)
	case http.StatusConflict, http.StatusBadRequest:
		return fmt.Errorf("%w: %s answered %s: %s", errReportLoses, name, response.Status,
			strings.TrimSpace(string(detail)))
	}
	return fmt.Errorf("mesh: %s answered %s: %s", name, response.Status, strings.TrimSpace(string(detail)))
}
