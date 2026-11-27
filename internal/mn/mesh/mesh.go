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
func (p *Puller) postReport(ctx context.Context, peer config.Peer, payload []byte) (bool, error) {
	var answer struct {
		Applied bool `json:"applied"`
	}
	if err := p.post(ctx, peer, "/peer/report", payload, &answer); err != nil {
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

func (p *Puller) fetchSnapshot(ctx context.Context, peer config.Peer) (store.Snapshot, error) {
	var snap store.Snapshot
	if err := p.get(ctx, peer, "/peer/snapshot", nil, &snap); err != nil {
		return store.Snapshot{}, err
	}
	return snap, nil
}

func (p *Puller) events(ctx context.Context, peer config.Peer, after int64) (Events, error) {
	query := url.Values{
		"after": {strconv.FormatInt(after, 10)},
		"limit": {strconv.Itoa(batch)},
	}
	var page Events
	if err := p.get(ctx, peer, "/peer/events", query, &page); err != nil {
		return Events{}, err
	}
	return page, nil
}

// maxAnswer bounds one response body. A snapshot of a thousand nodes is a few hundred kilobytes; this is
// the point past which the answer is not this protocol's.
const maxAnswer = 32 << 20

func (p *Puller) get(ctx context.Context, peer config.Peer, path string, query url.Values, into any) error {
	client, err := p.clientFor(peer)
	if err != nil {
		return err
	}
	target := strings.TrimRight(peer.URL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", peer.Name, err)
	}
	request.Header.Set("Authorization", "Bearer "+PullToken(p.MasterKey))
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", peer.Name, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return p.refusal(peer, response)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAnswer)).Decode(into); err != nil {
		return fmt.Errorf("mesh: %s: read %s: %w", peer.Name, path, err)
	}
	return nil
}

// post sends one queued report. It is the only write this package makes, and the home fences it: a report
// for a row the receiver does not home is refused, not redirected.
func (p *Puller) post(ctx context.Context, peer config.Peer, path string, body []byte, into any) error {
	client, err := p.clientFor(peer)
	if err != nil {
		return err
	}
	target := strings.TrimRight(peer.URL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", peer.Name, err)
	}
	request.Header.Set("Authorization", "Bearer "+PullToken(p.MasterKey))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", peer.Name, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return p.refusal(peer, response)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAnswer)).Decode(into); err != nil {
		return fmt.Errorf("mesh: %s: read %s: %w", peer.Name, path, err)
	}
	return nil
}

func (p *Puller) clientFor(peer config.Peer) (*http.Client, error) {
	if p.Client != nil {
		return p.Client, nil
	}
	return ClientFor(peer, DefaultRequestTimeout)
}

// errReportLoses marks an answer that says the report will never be applied: the node is unknown there, or
// it is homed by somebody else. Retrying it would keep a dead entry in the queue for ever.
var errReportLoses = errors.New("mesh: the report will not be applied")

// refusal turns a peer's answer into something an operator can act on: the two mistakes this protocol has
// are a wrong key and a URL that does not carry the prefix, and both deserve to be named.
func (p *Puller) refusal(peer config.Peer, response *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("mesh: %s refused the mesh credential: the two servers do not share a master key", peer.Name)
	case http.StatusNotFound:
		return fmt.Errorf("mesh: %s has no replication endpoint: the url must carry the peer's API prefix", peer.Name)
	case http.StatusConflict, http.StatusBadRequest:
		return fmt.Errorf("%w: %s answered %s: %s", errReportLoses, peer.Name, response.Status,
			strings.TrimSpace(string(detail)))
	}
	return fmt.Errorf("mesh: %s answered %s: %s", peer.Name, response.Status, strings.TrimSpace(string(detail)))
}
