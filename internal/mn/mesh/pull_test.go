package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "termit.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testPuller(t *testing.T, s *store.Store, client *http.Client) *Puller {
	t.Helper()
	return &Puller{
		Store:     s,
		Mesh:      config.Mesh{Peers: []config.Peer{{Name: "mn2", URL: "http://peer.invalid"}}},
		MasterKey: testKey,
		Logger:    slog.New(slog.DiscardHandler),
		Client:    client,
	}
}

func peerConfig(t *testing.T, url string) config.Peer {
	t.Helper()
	return config.Peer{Name: "mn2", URL: url}
}

func testNode(id, domain string) store.UV {
	return store.UV{ID: id, Label: "lab node", Domain: domain, State: store.StateActive,
		CreatedAt: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
}

// nodeBody is the payload of a node event: the whole row, as the journal carries it.
func nodeBody(t *testing.T, node store.UV) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"node": node, "transports": []string{"hysteria2"}})
	if err != nil {
		t.Fatalf("marshal the node: %v", err)
	}
	return body
}

func answer(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.WriteHeader(status)
	if body != nil {
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("write the answer: %v", err)
		}
	}
}

// The token is the whole authorization a peer sees: the endpoints are read-only, so holding it reads the
// registry and nothing else.
func TestThePullTokenTravelsWithEveryRequest(t *testing.T) {
	t.Parallel()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.URL.Path != "/peer/events" {
			t.Errorf("the request went to %q", r.URL.Path)
		}
		answer(t, w, http.StatusOK, Events{MNID: "mn_peer"})
	}))
	defer srv.Close()

	puller := testPuller(t, openStore(t), srv.Client())
	if _, err := puller.events(context.Background(), peerConfig(t, srv.URL), 3); err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("%d requests, want one", len(seen))
	}
	if want := "Bearer " + PullToken(testKey); seen[0] != want {
		t.Errorf("Authorization = %q, want %q", seen[0], want)
	}
}

// The URL in the configuration carries the peer's API prefix and may end in a slash; the request must still
// land on the endpoint rather than on a doubled slash.
func TestATrailingSlashInThePeerURLIsIgnored(t *testing.T) {
	t.Parallel()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		answer(t, w, http.StatusOK, Events{})
	}))
	defer srv.Close()

	puller := testPuller(t, openStore(t), srv.Client())
	if _, err := puller.events(context.Background(), peerConfig(t, srv.URL+"/"), 0); err != nil {
		t.Fatalf("events: %v", err)
	}
	if path != "/peer/events" {
		t.Errorf("path = %q, want /peer/events", path)
	}
}

// A peer that answers is saying something about the other side; a peer that does not answer is saying
// something else. The two mistakes the protocol can make — a different master key, a URL without the prefix —
// are named, and an operator should never have to read a stack trace to tell them apart.
func TestARefusalNamesTheMistake(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		want   string
		loses  bool
	}{
		{"a different key", http.StatusUnauthorized, "do not share a master key", false},
		{"a URL without the prefix", http.StatusNotFound, "must carry the peer's API prefix", false},
		{"a report it will not apply", http.StatusConflict, "answered 409", true},
		{"a bad request", http.StatusBadRequest, "answered 400", true},
		{"a peer that is broken", http.StatusInternalServerError, "answered 500", false},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte("detail from the peer"))
			}))
			defer srv.Close()

			puller := testPuller(t, openStore(t), srv.Client())
			_, err := puller.events(context.Background(), peerConfig(t, srv.URL), 0)
			if err == nil {
				t.Fatal("the refusal was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
			if got := errors.Is(err, errReportLoses); got != test.loses {
				t.Errorf("errReportLoses = %v, want %v", got, test.loses)
			}
		})
	}
}

func TestAPeerThatDoesNotAnswerIsUnreachable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	puller := testPuller(t, openStore(t), srv.Client())
	_, err := puller.events(context.Background(), peerConfig(t, srv.URL), 0)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want ErrUnreachable", err)
	}
}

// The first pull has no cursor to start from, so it takes the snapshot, and it has to leave both the peer's
// identity and its position behind: the identity keys the journal here, the position is what status reads.
func TestTheFirstPullTakesASnapshot(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/peer/snapshot" {
			t.Errorf("the first pull asked for %q", r.URL.Path)
		}
		answer(t, w, http.StatusOK, store.Snapshot{
			SchemaVersion: store.SchemaVersion,
			MNID:          "mn_peer",
			Seq:           7,
			Nodes:         []store.UV{testNode("uv1", "a.example")},
		})
	}))
	defer srv.Close()

	s := openStore(t)
	puller := testPuller(t, s, srv.Client())
	applied, err := puller.Pull(context.Background(), peerConfig(t, srv.URL))
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied %d, want the one node of the snapshot", applied)
	}
	ctx := context.Background()
	if got, err := s.GetSetting(ctx, PeerIDKey("mn2")); err != nil || got != "mn_peer" {
		t.Errorf("peer id = %q (%v), want mn_peer", got, err)
	}
	if got, err := s.GetSetting(ctx, PeerSeqKey("mn2")); err != nil || got != "7" {
		t.Errorf("peer position = %q (%v), want 7", got, err)
	}
	node, err := s.UVByID(ctx, "uv1")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if node.HomeMN != "mn_peer" {
		t.Errorf("home = %q, want the origin of the snapshot", node.HomeMN)
	}
}

// A snapshot from a build that speaks another schema is refused by the store; the puller must pass that on
// rather than record a position for a registry it could not read.
func TestASnapshotOfAnotherSchemaIsRefused(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		answer(t, w, http.StatusOK, store.Snapshot{
			SchemaVersion: store.SchemaVersion + 1,
			MNID:          "mn_peer",
			Nodes:         []store.UV{testNode("uv1", "a.example")},
		})
	}))
	defer srv.Close()

	s := openStore(t)
	puller := testPuller(t, s, srv.Client())
	if _, err := puller.Pull(context.Background(), peerConfig(t, srv.URL)); err == nil {
		t.Fatal("a snapshot of another schema was applied")
	}
	if _, err := s.GetSetting(context.Background(), PeerIDKey("mn2")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the puller remembered a peer it could not read: %v", err)
	}
}

// Once the peer is known, the journal is read past the cursor; the page's LastSeq is the operator's answer to
// "how far behind am I".
func TestTheJournalIsReadPastTheCursor(t *testing.T) {
	t.Parallel()
	node := testNode("uv1", "a.example")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/peer/events" {
			t.Errorf("the pull asked for %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("after"); got != "0" {
			t.Errorf("after = %q, want the cursor 0", got)
		}
		answer(t, w, http.StatusOK, Events{
			MNID:    "mn_peer",
			LastSeq: 2,
			Events: []store.ReplicationEvent{{
				MNID: "mn_peer", Seq: 1, At: time.Now().UTC(), Kind: store.EventNode, Payload: nodeBody(t, node),
			}},
		})
	}))
	defer srv.Close()

	s := openStore(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, PeerIDKey("mn2"), "mn_peer"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	puller := testPuller(t, s, srv.Client())
	applied, err := puller.Pull(ctx, peerConfig(t, srv.URL))
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied %d, want 1", applied)
	}
	if cursor, err := s.Cursor(ctx, "mn_peer"); err != nil || cursor != 1 {
		t.Errorf("cursor = %d (%v), want 1", cursor, err)
	}
	if got, err := s.GetSetting(ctx, PeerSeqKey("mn2")); err != nil || got != "2" {
		t.Errorf("peer position = %q (%v), want the page's LastSeq 2", got, err)
	}
}

// A gap means the peer pruned its journal past the cursor, and a snapshot is the only way across a hole.
func TestAGapFallsBackToTheSnapshot(t *testing.T) {
	t.Parallel()
	node := testNode("uv1", "a.example")
	snapshots := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/peer/events":
			answer(t, w, http.StatusOK, Events{
				MNID:    "mn_peer",
				LastSeq: 9,
				Events: []store.ReplicationEvent{{
					MNID: "mn_peer", Seq: 9, At: time.Now().UTC(), Kind: store.EventNode, Payload: nodeBody(t, node),
				}},
			})
		case "/peer/snapshot":
			snapshots++
			answer(t, w, http.StatusOK, store.Snapshot{
				SchemaVersion: store.SchemaVersion, MNID: "mn_peer", Seq: 9, Nodes: []store.UV{node},
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	s := openStore(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, PeerIDKey("mn2"), "mn_peer"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	puller := testPuller(t, s, srv.Client())
	applied, err := puller.Pull(ctx, peerConfig(t, srv.URL))
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if snapshots != 1 {
		t.Errorf("%d snapshots, want one across the gap", snapshots)
	}
	if applied != 1 {
		t.Errorf("applied %d, want the snapshot's one node", applied)
	}
	if _, err := s.UVByID(ctx, node.ID); err != nil {
		t.Errorf("the node is not here after the snapshot: %v", err)
	}
}

// deliveringReports runs one delivery pass against a peer that answers with the given status, and returns
// how many requests the peer saw.
func deliveringReports(t *testing.T, status int, body any) (left int, requests int) {
	t.Helper()
	node := testNode("uv1", "a.example")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/peer/snapshot":
			answer(t, w, http.StatusOK, store.Snapshot{
				SchemaVersion: store.SchemaVersion, MNID: "mn_peer", Seq: 1, Nodes: []store.UV{node},
			})
		case "/peer/report":
			requests++
			answer(t, w, status, body)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	s := openStore(t)
	puller := testPuller(t, s, srv.Client())
	puller.Mesh.Peers = []config.Peer{peerConfig(t, srv.URL)}
	ctx := context.Background()
	if _, err := puller.Pull(ctx, peerConfig(t, srv.URL)); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	// The node's row now names the peer as its home, which is what a replica stores while it serves one.
	if err := s.QueueReport(ctx, node.ID, time.Now().UTC(), []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("QueueReport: %v", err)
	}
	puller.DeliverReports(ctx)
	reports, err := s.PendingReports(ctx)
	if err != nil {
		t.Fatalf("PendingReports: %v", err)
	}
	return len(reports), requests
}

func TestADeliveredReportLeavesTheQueue(t *testing.T) {
	t.Parallel()
	left, requests := deliveringReports(t, http.StatusOK, map[string]bool{"applied": true})
	if requests != 1 {
		t.Errorf("%d requests, want one", requests)
	}
	if left != 0 {
		t.Errorf("%d reports left, want the queue empty", left)
	}
}

// A report the home will never apply — the node is unknown there, or it is homed elsewhere — must leave the
// queue, or it is retried for ever.
func TestAReportTheHomeRefusesLeavesTheQueue(t *testing.T) {
	t.Parallel()
	left, _ := deliveringReports(t, http.StatusConflict, nil)
	if left != 0 {
		t.Errorf("%d reports left, want the queue empty", left)
	}
}

// A home that is merely broken is not a verdict about the report: it stays queued and is retried.
func TestAReportForABrokenHomeWaits(t *testing.T) {
	t.Parallel()
	left, _ := deliveringReports(t, http.StatusInternalServerError, nil)
	if left != 1 {
		t.Errorf("%d reports left, want it kept for the next pass", left)
	}
}

// A report for a node this server no longer knows has nowhere to go, and a report for a node this server
// homes now is a slower path to the same row: both leave the queue without a request.
func TestAReportWithNoHomeIsDropped(t *testing.T) {
	t.Parallel()
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		answer(t, w, http.StatusOK, map[string]bool{"applied": true})
	}))
	defer srv.Close()

	s := openStore(t)
	ctx := context.Background()
	if err := s.QueueReport(ctx, "gone", time.Now().UTC(), []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("QueueReport: %v", err)
	}
	puller := testPuller(t, s, srv.Client())
	puller.DeliverReports(ctx)
	if requests != 0 {
		t.Errorf("%d requests for a report with no home", requests)
	}
	reports, err := s.PendingReports(ctx)
	if err != nil {
		t.Fatalf("PendingReports: %v", err)
	}
	if len(reports) != 0 {
		t.Errorf("%d reports left, want the queue empty", len(reports))
	}
}

func TestARunEndsWithItsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	puller := testPuller(t, openStore(t), nil)
	puller.Mesh.Peers = nil
	done := make(chan struct{})
	go func() {
		puller.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context ended")
	}
}

func TestAPeerWithoutAPinTrustsTheSystemRoots(t *testing.T) {
	t.Parallel()
	client, err := ClientFor(config.Peer{Name: "mn2", URL: "https://mn2.example.com/prefix"}, time.Second)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", client.Transport)
	}
	// Cloning the default transport keeps its TLS settings, so the check is that no pin verification
	// was installed and verification was not switched off.
	if config := transport.TLSClientConfig; config != nil {
		if config.VerifyPeerCertificate != nil || config.InsecureSkipVerify {
			t.Errorf("the client verifies a pin it was not given: %+v", config)
		}
	}
}
