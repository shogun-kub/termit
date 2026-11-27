package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The mesh's journal: one append-only log per management server, a cursor per peer, and a snapshot for a
// peer that is new or too far behind to catch up event by event (docs/IMPLEMENTATION_PLAN.md §5.2.1).
//
// What travels is node rows, because a replica must answer a sync and render a subscription without
// asking the home. What does not travel: invites, admin tokens, the audit log and the check history —
// those belong to the server that made them, and a replica never writes one.

// Event kinds. The set is closed: an unknown kind is refused, never skipped, so an event from a newer
// peer cannot pass silently through an older build and leave it half-updated.
const (
	// EventNode carries one node and the transports it serves, as nodeEvent.
	EventNode = "node"
	// EventNodeRemoved carries nodeRemoved: the node left the mesh.
	EventNodeRemoved = "node_removed"
)

// Journal errors. The caller decides what to do with each: a gap means "take a snapshot", a conflict is
// a mesh that disagrees about who owns a node, and neither may be retried in a loop hoping to pass.
var (
	// ErrReplicationGap means an event arrived before the one it follows.
	ErrReplicationGap = errors.New("store: replication gap")
	// ErrUnknownEvent means this build does not understand the kind.
	ErrUnknownEvent = errors.New("store: unknown replication event")
	// ErrReplicationConflict means the peer's row is one this server homes, belongs to another peer, or
	// claims a domain already served. Two homes for one node id is what the mesh refuses to merge.
	ErrReplicationConflict = errors.New("store: replication conflict")
	// ErrSnapshotStale means the snapshot is older than what this server already applied.
	ErrSnapshotStale = errors.New("store: snapshot is older than the applied state")
)

// meshIDSetting is the settings key holding this server's identity in the journal.
const meshIDSetting = "mn_id"

// ownedHere refuses a write to a row another server homes. Two homes for one id is exactly the split brain
// the journal exists to prevent, and the losing side's write would be overwritten at the next pull anyway,
// so it is refused where it is attempted rather than discovered later.
func ownedHere(ctx context.Context, tx *sql.Tx, id, mnID string) error {
	var home string
	switch err := tx.QueryRowContext(ctx, `SELECT home_mn FROM uv WHERE id = ?`, id).Scan(&home); {
	case errors.Is(err, sql.ErrNoRows):
		// Not this guard's answer to give: the caller's own "not found" says more.
		return nil
	case err != nil:
		return fmt.Errorf("store: read the home of %s: %w", id, err)
	case home != "" && home != mnID:
		return fmt.Errorf("%w: %s is homed by %s, not by %s", ErrReplicationConflict, id, home, mnID)
	}
	return nil
}

// maxReplicationBatch bounds one read, so a peer with a long log cannot pull it in one response.
const maxReplicationBatch = 500

// ReplicationEvent is one journal entry.
type ReplicationEvent struct {
	MNID    string          `json:"mn_id"`
	Seq     int64           `json:"seq"`
	At      time.Time       `json:"at"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// nodeEvent is the payload of EventNode: the whole node, and the transports it serves.
type nodeEvent struct {
	Node       UV       `json:"node"`
	Transports []string `json:"transports,omitempty"`
}

// nodeRemoved is the payload of EventNodeRemoved.
type nodeRemoved struct {
	ID string `json:"id"`
}

// Snapshot is everything a peer needs to start from nothing: the origin's nodes as of one journal
// position, and that position, so the events after it apply cleanly.
type Snapshot struct {
	SchemaVersion int                 `json:"schema_version"`
	MNID          string              `json:"mn_id"`
	Seq           int64               `json:"seq"`
	Nodes         []UV                `json:"nodes"`
	Transports    map[string][]string `json:"transports,omitempty"`
}

// MeshID is this server's identity in the journal: minted once, kept in settings. The domain cannot
// serve, because a server may move to another name without becoming another server.
func (s *Store) MeshID(ctx context.Context) (string, error) {
	id, err := s.GetSetting(ctx, meshIDSetting)
	switch {
	case err == nil && id != "":
		return id, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return "", fmt.Errorf("store: read the mesh id: %w", err)
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: mint the mesh id: %w", err)
	}
	id = "mn_" + hex.EncodeToString(buf)
	if err := s.SetSetting(ctx, meshIDSetting, id); err != nil {
		return "", err
	}
	return id, nil
}

// AppendNode records one node and the transports it serves. Enrollment, a state transition and the admin
// commands will call it beside their own write when the transport lands — through appendEventTx, so the
// row and the entry share a transaction.
func (s *Store) AppendNode(ctx context.Context, node UV, transports []string) (ReplicationEvent, error) {
	return s.AppendEvent(ctx, EventNode, nodeEvent{Node: node, Transports: transports})
}

// AppendNodeRemoved records that a node left the mesh.
func (s *Store) AppendNodeRemoved(ctx context.Context, id string) (ReplicationEvent, error) {
	return s.AppendEvent(ctx, EventNodeRemoved, nodeRemoved{ID: id})
}

// AppendEvent records one change in its own transaction and returns the entry with the sequence it got.
// The sequence is per origin and only ever grows, which is what lets a peer apply the log in order and
// skip what it saw. A store method that changes the registry itself appends through appendEventTx
// instead, inside its own transaction, so the row and its journal entry cannot disagree.
func (s *Store) AppendEvent(ctx context.Context, kind string, payload any) (ReplicationEvent, error) {
	if !knownKind(kind) {
		return ReplicationEvent{}, fmt.Errorf("%w: %q", ErrUnknownEvent, kind)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ReplicationEvent{}, fmt.Errorf("store: encode %s: %w", kind, err)
	}
	mnID, err := s.MeshID(ctx)
	if err != nil {
		return ReplicationEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReplicationEvent{}, fmt.Errorf("store: begin append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	event, err := appendEventTx(ctx, tx, s.now().UTC(), mnID, kind, body)
	if err != nil {
		return ReplicationEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReplicationEvent{}, fmt.Errorf("store: commit append: %w", err)
	}
	return event, nil
}

// appendEventTx writes one entry into the caller's transaction. It is the shape every producer uses once
// the transport lands: the registry change and the entry describing it commit together or not at all.
func appendEventTx(ctx context.Context, tx *sql.Tx, at time.Time, mnID, kind string, body []byte) (ReplicationEvent, error) {
	if mnID == "" {
		return ReplicationEvent{}, errors.New("store: an event needs an origin")
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM replication_event WHERE mn_id = ?`, mnID).Scan(&seq); err != nil {
		return ReplicationEvent{}, fmt.Errorf("store: read the journal tail: %w", err)
	}
	seq++
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO replication_event (mn_id, seq, at, kind, payload) VALUES (?, ?, ?, ?, ?)`,
		mnID, seq, formatTime(at), kind, body); err != nil {
		return ReplicationEvent{}, fmt.Errorf("store: append %s: %w", kind, err)
	}
	return ReplicationEvent{MNID: mnID, Seq: seq, At: at, Kind: kind, Payload: body}, nil
}

// EventsSince returns one server's journal after seq, oldest first.
func (s *Store) EventsSince(ctx context.Context, mnID string, after int64, limit int) ([]ReplicationEvent, error) {
	if mnID == "" {
		return nil, errors.New("store: the journal belongs to a server")
	}
	if limit <= 0 || limit > maxReplicationBatch {
		limit = maxReplicationBatch
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT mn_id, seq, at, kind, payload FROM replication_event
		 WHERE mn_id = ? AND seq > ? ORDER BY seq LIMIT ?`, mnID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read the journal: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []ReplicationEvent
	for rows.Next() {
		var (
			event ReplicationEvent
			at    string
		)
		if err := rows.Scan(&event.MNID, &event.Seq, &at, &event.Kind, &event.Payload); err != nil {
			return nil, fmt.Errorf("store: scan the journal: %w", err)
		}
		if event.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("store: parse an event time: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the journal: %w", err)
	}
	return events, nil
}

// Cursor reports how far this server has applied another server's log. A peer never seen before is 0.
func (s *Store) Cursor(ctx context.Context, mnID string) (int64, error) {
	return cursorIn(ctx, s.db, mnID)
}

// LastSeq reports the newest entry in one server's log — this one's or a peer's — so a caller can tell how
// many entries it is behind. An empty log is 0.
func (s *Store) LastSeq(ctx context.Context, mnID string) (int64, error) {
	if mnID == "" {
		return 0, errors.New("store: the journal belongs to a server")
	}
	var seq int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM replication_event WHERE mn_id = ?`, mnID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("store: read the last entry of %s: %w", mnID, err)
	}
	return seq, nil
}

// Cursors reports how far this server has applied every log it follows. A handoff compares it with the home's
// own position: the target says "I am this far along", and the operator decides whether that is close
// enough to take a node.
func (s *Store) Cursors(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mn_id, seq FROM replication_cursor ORDER BY mn_id`)
	if err != nil {
		return nil, fmt.Errorf("store: read the cursors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	applied := map[string]int64{}
	for rows.Next() {
		var (
			mnID string
			seq  int64
		)
		if err := rows.Scan(&mnID, &seq); err != nil {
			return nil, fmt.Errorf("store: scan a cursor: %w", err)
		}
		applied[mnID] = seq
	}
	return applied, rows.Err()
}

// KeepJournal is how many of this server's own entries stay in the log after pruning. A peer that falls
// further behind than this takes a snapshot instead, which is the path that already exists for a new peer.
const KeepJournal = 1000

// PruneJournal drops this server's own entries beyond KeepJournal. The table only ever holds them: a peer's
// log is applied through the cursor and never copied, because a third server pulls the origin and not this
// copy — so the journal is one log, and pruning it is what keeps a long-running mesh from growing a file it
// never reads. Returns how many rows went.
//
// The newest entry is never dropped: the sequence is per origin and only grows, and a log whose maximum moved
// backwards would hand a peer a sequence it has already applied.
func (s *Store) PruneJournal(ctx context.Context, keep int64) (int64, error) {
	mnID, err := s.MeshID(ctx)
	if err != nil {
		return 0, err
	}
	if keep < 1 {
		keep = KeepJournal
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin the journal prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var before int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM replication_event`).Scan(&before); err != nil {
		return 0, fmt.Errorf("store: count the journal: %w", err)
	}
	var newest int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM replication_event WHERE mn_id = ?`, mnID).Scan(&newest); err != nil {
		return 0, fmt.Errorf("store: read the newest entry: %w", err)
	}
	if newest > keep {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM replication_event WHERE mn_id = ? AND seq <= ?`, mnID, newest-keep); err != nil {
			return 0, fmt.Errorf("store: prune this server's entries: %w", err)
		}
	}
	// Counted rather than read from RowsAffected: the answer is what the log holds, not what a driver says.
	var after int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM replication_event`).Scan(&after); err != nil {
		return 0, fmt.Errorf("store: count the journal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit the journal prune: %w", err)
	}
	return before - after, nil
}

// AdoptUV makes this server the home of a node whose home is gone for good, without that home's cooperation.
// The row keeps its agent key — the copy here was sealed with the master key this server also holds, so
// nothing has to be installed on the node — and only the home and the term move. A queued report for the old
// home is dropped: the node's next sync writes the same facts to the server that now homes it.
//
// This is the one write that takes a row nobody offered. It is the operator's decision (the admin socket,
// `home adopt`) and never a timeout's, and it refuses a row that is not the named server's.
func (s *Store) AdoptUV(ctx context.Context, id, from string, now time.Time) (UV, error) {
	mnID, err := s.MeshID(ctx)
	if err != nil {
		return UV{}, err
	}
	if from == "" || from == mnID {
		return UV{}, fmt.Errorf("store: %s cannot be adopted from %q", id, from)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UV{}, fmt.Errorf("store: begin the adoption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		home  string
		term  int64
		state State
	)
	switch err := tx.QueryRowContext(ctx, `SELECT home_mn, home_term, state FROM uv WHERE id = ?`, id).
		Scan(&home, &term, &state); {
	case errors.Is(err, sql.ErrNoRows):
		return UV{}, ErrNotFound
	case err != nil:
		return UV{}, fmt.Errorf("store: read %s for the adoption: %w", id, err)
	}
	if home != from {
		return UV{}, fmt.Errorf("%w: %s is homed by %q, not %q", ErrReplicationConflict, id, home, from)
	}
	if state == StateRemoved {
		return UV{}, fmt.Errorf("store: %s is removed and cannot be adopted", id)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE uv SET home_mn = ?, home_term = ? WHERE id = ?`, mnID, term+1, id); err != nil {
		return UV{}, fmt.Errorf("store: adopt %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mesh_report WHERE uv_id = ?`, id); err != nil {
		return UV{}, fmt.Errorf("store: drop the queued report of %s: %w", id, err)
	}
	if err := recordNode(ctx, tx, mnID, now, id); err != nil {
		return UV{}, err
	}
	node, err := scanUV(tx.QueryRowContext(ctx, `SELECT `+uvColumns+` FROM uv WHERE id = ?`, id))
	if err != nil {
		return UV{}, fmt.Errorf("store: read %s back: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return UV{}, fmt.Errorf("store: commit the adoption: %w", err)
	}
	return node, nil
}

// UVsHomedBy lists the rows one server homes, by domain: what `home adopt` takes over and what an operator
// sees before deciding to take it.
func (s *Store) UVsHomedBy(ctx context.Context, mnID string) ([]UV, error) {
	if mnID == "" {
		return nil, errors.New("store: rows are homed by a server or by this one, and that is not nothing")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+uvColumns+` FROM uv WHERE home_mn = ? AND state <> ? ORDER BY domain`, mnID, string(StateRemoved))
	if err != nil {
		return nil, fmt.Errorf("store: list the rows of %s: %w", mnID, err)
	}
	defer func() { _ = rows.Close() }()
	var nodes []UV
	for rows.Next() {
		node, err := scanUV(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

// AcceptHandoff makes this server the home of a node another server hands over: one transaction moves the
// home and the term, replaces the agent key, and journals the node as this server's own. A row that is not
// the caller's, or is removed, is refused.
func (s *Store) AcceptHandoff(ctx context.Context, id, from, keyHash, keySealed string, now time.Time) (UV, error) {
	mnID, err := s.MeshID(ctx)
	if err != nil {
		return UV{}, err
	}
	if from == "" || from == mnID {
		return UV{}, fmt.Errorf("store: %s cannot be handed over from %q", id, from)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UV{}, fmt.Errorf("store: begin the handoff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		home  string
		term  int64
		state State
	)
	switch err := tx.QueryRowContext(ctx, `SELECT home_mn, home_term, state FROM uv WHERE id = ?`, id).
		Scan(&home, &term, &state); {
	case errors.Is(err, sql.ErrNoRows):
		return UV{}, ErrNotFound
	case err != nil:
		return UV{}, fmt.Errorf("store: read %s for the handoff: %w", id, err)
	}
	if state == StateRemoved {
		return UV{}, fmt.Errorf("store: %s is removed and cannot be handed over", id)
	}
	if home != from {
		return UV{}, fmt.Errorf("%w: %s is homed by %q, not %q", ErrReplicationConflict, id, home, from)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE uv SET home_mn = ?, home_term = ?, agent_key_hash = ?, agent_key_sealed = ? WHERE id = ?`,
		mnID, term+1, keyHash, keySealed, id); err != nil {
		return UV{}, fmt.Errorf("store: take over %s: %w", id, err)
	}
	// The row is this server's now, so this is the entry a third server will pull to learn about the handoff.
	if err := recordNode(ctx, tx, mnID, now, id); err != nil {
		return UV{}, err
	}
	node, err := scanUV(tx.QueryRowContext(ctx, `SELECT `+uvColumns+` FROM uv WHERE id = ?`, id))
	if err != nil {
		return UV{}, fmt.Errorf("store: read %s back: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return UV{}, fmt.Errorf("store: commit the handoff: %w", err)
	}
	return node, nil
}

// HandoffOut records that this server gave a node away: the row is the new home's from now on, at the term
// that home answered with. It is not journaled — the row is not this server's any more, and an entry about it
// here would claim a decision this server no longer makes.
func (s *Store) HandoffOut(ctx context.Context, id, to string, term int64, keyHash, keySealed string) error {
	mnID, err := s.MeshID(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin the handoff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := ownedHere(ctx, tx, id, mnID); err != nil {
		return err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT home_term FROM uv WHERE id = ?`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: read the term of %s: %w", id, err)
	}
	if term <= current {
		return fmt.Errorf("store: %s is at term %d here and the new home answered %d", id, current, term)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE uv SET home_mn = ?, home_term = ?, agent_key_hash = ?, agent_key_sealed = ? WHERE id = ?`,
		to, term, keyHash, keySealed, id); err != nil {
		return fmt.Errorf("store: hand %s over: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit the handoff: %w", err)
	}
	return nil
}

// ApplyEvents applies one peer's events in order. An entry already applied is skipped, so a retry is
// free; a gap, an unknown kind or an unreadable payload stops the batch before anything is written, so
// the cursor never passes a change this server did not make.
func (s *Store) ApplyEvents(ctx context.Context, events []ReplicationEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	mnID := events[0].MNID
	if mnID == "" {
		return 0, errors.New("store: an event without an origin")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cursor, err := cursorIn(ctx, tx, mnID)
	if err != nil {
		return 0, err
	}
	applied := 0
	for _, event := range events {
		if event.MNID != mnID {
			return 0, fmt.Errorf("store: a batch from two origins, %s and %s", mnID, event.MNID)
		}
		if event.Seq <= cursor {
			continue
		}
		if event.Seq != cursor+1 {
			return 0, fmt.Errorf("%w: %s is applied to %d and the next event is %d",
				ErrReplicationGap, mnID, cursor, event.Seq)
		}
		if err := applyEvent(ctx, tx, event); err != nil {
			if errors.Is(err, errSuperseded) {
				// This entry lost to a handoff already applied here. It is not a failure — the journal
				// must move past it — and it is not a change, so it does not count as applied.
				cursor = event.Seq
				continue
			}
			return 0, fmt.Errorf("store: apply %s event %d: %w", mnID, event.Seq, err)
		}
		cursor = event.Seq
		applied++
	}
	if err := setCursor(ctx, tx, mnID, cursor); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit apply: %w", err)
	}
	return applied, nil
}

// Snapshot exports what this server homes, with the journal position it was taken at.
func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	mnID, err := s.MeshID(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	nodes, err := s.homedNodes(ctx, mnID)
	if err != nil {
		return Snapshot{}, err
	}
	served, err := s.UVTransports(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	transports := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		if kinds := served[node.ID]; len(kinds) > 0 {
			transports[node.ID] = kinds
		}
	}
	var seq int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM replication_event WHERE mn_id = ?`, mnID).Scan(&seq); err != nil {
		return Snapshot{}, fmt.Errorf("store: read the journal tail: %w", err)
	}
	return Snapshot{SchemaVersion: SchemaVersion, MNID: mnID, Seq: seq, Nodes: nodes, Transports: transports}, nil
}

// ApplySnapshot replaces what this server holds of the origin's nodes. Rows this server homes are never
// touched. A snapshot older than what was already applied is refused, because replaying it would roll the
// registry back to a state the mesh has moved past.
func (s *Store) ApplySnapshot(ctx context.Context, snap Snapshot) error {
	if snap.MNID == "" {
		return errors.New("store: a snapshot needs an origin")
	}
	if snap.SchemaVersion != SchemaVersion {
		return fmt.Errorf("store: snapshot schema %d, this build speaks %d", snap.SchemaVersion, SchemaVersion)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cursor, err := cursorIn(ctx, tx, snap.MNID)
	if err != nil {
		return err
	}
	if snap.Seq < cursor {
		return fmt.Errorf("%w: snapshot at %d, %s already applied to %d",
			ErrSnapshotStale, snap.Seq, snap.MNID, cursor)
	}
	// The snapshot is authoritative for what the origin homes: drop its rows and write the set back as
	// the origin has it. Children first, so a transport never outlives its node.
	if _, err := tx.ExecContext(ctx, `DELETE FROM uv_transport WHERE uv_id IN (SELECT id FROM uv WHERE home_mn = ?)`,
		snap.MNID); err != nil {
		return fmt.Errorf("store: clear the origin's transports: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM uv WHERE home_mn = ?`, snap.MNID); err != nil {
		return fmt.Errorf("store: clear the origin's nodes: %w", err)
	}
	for _, node := range snap.Nodes {
		if node.HomeMN == "" {
			node.HomeMN = snap.MNID
		}
		if node.HomeMN != snap.MNID {
			return fmt.Errorf("%w: %s has another home", ErrReplicationConflict, node.ID)
		}
		if err := putReplicatedNode(ctx, tx, node); err != nil {
			if errors.Is(err, errSuperseded) {
				// The snapshot is older than a handoff this server already applied: keep what is here.
				continue
			}
			return err
		}
		if err := putReplicatedTransports(ctx, tx, node.ID, snap.Transports[node.ID]); err != nil {
			return err
		}
	}
	if err := setCursor(ctx, tx, snap.MNID, snap.Seq); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit snapshot: %w", err)
	}
	return nil
}

// applyEvent writes one journal entry. Only the kinds this build knows are accepted.
func applyEvent(ctx context.Context, tx *sql.Tx, event ReplicationEvent) error {
	switch event.Kind {
	case EventNode:
		var body nodeEvent
		if err := json.Unmarshal(event.Payload, &body); err != nil {
			return fmt.Errorf("decode the node: %w", err)
		}
		node := body.Node
		if node.HomeMN == "" {
			node.HomeMN = event.MNID
		}
		if node.HomeMN != event.MNID {
			return fmt.Errorf("%w: %s claims %s as its home", ErrReplicationConflict, event.MNID, node.ID)
		}
		if err := putReplicatedNode(ctx, tx, node); err != nil {
			// A handoff this server already applied: the peer's row is older than what is here. The
			// caller skips it and lets the cursor move.
			return err
		}
		return putReplicatedTransports(ctx, tx, node.ID, body.Transports)
	case EventNodeRemoved:
		var body nodeRemoved
		if err := json.Unmarshal(event.Payload, &body); err != nil {
			return fmt.Errorf("decode the removal: %w", err)
		}
		return removeReplicatedNode(ctx, tx, body.ID, event.MNID)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownEvent, event.Kind)
	}
}

// putReplicatedNode writes a row the peer homes. The guard runs before the upsert, because the upsert
// cannot tell "update my copy of A's node" from "overwrite the node I home myself".
func putReplicatedNode(ctx context.Context, tx *sql.Tx, node UV) error {
	if node.ID == "" || node.Domain == "" {
		return errors.New("a replicated node needs an id and a domain")
	}
	if !node.State.Valid() {
		return fmt.Errorf("%q is not a state", node.State)
	}
	var (
		home string
		term int64
	)
	err := tx.QueryRowContext(ctx, `SELECT home_mn, home_term FROM uv WHERE id = ?`, node.ID).Scan(&home, &term)
	switch {
	case err == nil && node.HomeTerm < term:
		// The peer's copy is behind a handoff it never applied: its write loses, and the batch must still
		// move on, so the cursor keeps its place.
		return errSuperseded
	case err == nil && node.HomeTerm == term && home != node.HomeMN:
		return fmt.Errorf("%w: %s is homed by %q, not %q", ErrReplicationConflict, node.ID, home, node.HomeMN)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read the home of %s: %w", node.ID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO uv (`+uvColumns+`) VALUES (`+uvPlaceholders+`) ON CONFLICT(id) DO UPDATE SET `+
			replicatedUpdate, uvArgs(node)...); err != nil {
		return wrapDuplicate(err, "store: replicate node "+node.ID)
	}
	return nil
}

// errSuperseded means the incoming row claims an older home term than this server already knows: the node
// was handed over while the peer was away. The caller skips the write and lets the journal move on, because
// refusing would stall the cursor behind a change that must never be applied.
var errSuperseded = errors.New("store: the row is superseded by a newer home term")

// removeReplicatedNode deletes a row the peer homes. A row that is not the peer's, or is this server's,
// is a conflict: a removal must never take out somebody else's node.
func removeReplicatedNode(ctx context.Context, tx *sql.Tx, id, origin string) error {
	if id == "" {
		return errors.New("a removal needs an id")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM uv_transport WHERE uv_id = ?`, id); err != nil {
		return fmt.Errorf("store: remove the transports of %s: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM uv WHERE id = ? AND home_mn = ?`, id, origin)
	if err != nil {
		return fmt.Errorf("store: remove %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if affected > 0 {
		return nil
	}
	// Nothing was deleted: either the node was never here — a removal of something already gone, which is
	// not an error — or it has moved to another home, which is a handoff this removal predates. Neither is
	// a reason to stall the cursor, so both are skipped.
	var home string
	err = tx.QueryRowContext(ctx, `SELECT home_mn FROM uv WHERE id = ?`, id).Scan(&home)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("store: read the home of %s: %w", id, err)
	}
	return nil
}

// putReplicatedTransports replaces the transport list of a node with the peer's.
func putReplicatedTransports(ctx context.Context, tx *sql.Tx, uvID string, kinds []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM uv_transport WHERE uv_id = ?`, uvID); err != nil {
		return fmt.Errorf("store: clear the transports of %s: %w", uvID, err)
	}
	for _, kind := range kinds {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO uv_transport (uv_id, kind) VALUES (?, ?) ON CONFLICT DO NOTHING`, uvID, kind); err != nil {
			return fmt.Errorf("store: replicate a transport of %s: %w", uvID, err)
		}
	}
	return nil
}

// recordNode journals a node as it now stands, inside the caller's transaction. Every write that changes
// a replicated field ends with it: the row and the entry commit together or not at all, so a crash cannot
// leave a change the mesh will never hear about.
func recordNode(ctx context.Context, tx *sql.Tx, mnID string, at time.Time, id string) error {
	node, err := scanUV(tx.QueryRowContext(ctx, `SELECT `+uvColumns+` FROM uv WHERE id = ?`, id))
	if err != nil {
		return fmt.Errorf("store: read %s for the journal: %w", id, err)
	}
	// A row homed elsewhere is not this server's to announce. Its log is what a third server pulls, so an
	// entry there would claim a decision this one never made; the home's next event overwrites the copy
	// anyway. The write itself is refused once the read-only sync of a replica exists (§5.2.1, decision
	// 3), which is where a node may reach this path.
	if node.HomeMN != "" && node.HomeMN != mnID {
		return nil
	}
	kinds, err := transportsTx(ctx, tx, id)
	if err != nil {
		return err
	}
	body, err := json.Marshal(nodeEvent{Node: node, Transports: kinds})
	if err != nil {
		return fmt.Errorf("store: encode %s for the journal: %w", id, err)
	}
	_, err = appendEventTx(ctx, tx, at, mnID, EventNode, body)
	return err
}

// recordNodeRemoved journals the departure of a node, inside the caller's transaction.
func recordNodeRemoved(ctx context.Context, tx *sql.Tx, mnID string, at time.Time, id string) error {
	body, err := json.Marshal(nodeRemoved{ID: id})
	if err != nil {
		return fmt.Errorf("store: encode the removal of %s: %w", id, err)
	}
	_, err = appendEventTx(ctx, tx, at, mnID, EventNodeRemoved, body)
	return err
}

// transportsTx is what one node reported it serves, read inside the caller's transaction so the journal
// entry describes the same moment as the row it travels with.
func transportsTx(ctx context.Context, tx *sql.Tx, uvID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT kind FROM uv_transport WHERE uv_id = ? ORDER BY kind`, uvID)
	if err != nil {
		return nil, fmt.Errorf("store: read the transports of %s: %w", uvID, err)
	}
	defer func() { _ = rows.Close() }()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, fmt.Errorf("store: scan a transport of %s: %w", uvID, err)
		}
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the transports of %s: %w", uvID, err)
	}
	return kinds, nil
}

// homedNodes are the rows this server owns: its own (an empty home) and any written under its own id.
func (s *Store) homedNodes(ctx context.Context, mnID string) ([]UV, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+uvColumns+` FROM uv WHERE home_mn = '' OR home_mn = ? ORDER BY id`, mnID)
	if err != nil {
		return nil, fmt.Errorf("store: list the homed nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var nodes []UV
	for rows.Next() {
		node, err := scanUV(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan a homed node: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list the homed nodes: %w", err)
	}
	return nodes, nil
}

// queryer is what cursorIn needs: the pool for a read, the transaction for a read that decides a write.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func cursorIn(ctx context.Context, q queryer, mnID string) (int64, error) {
	if mnID == "" {
		return 0, errors.New("store: a cursor belongs to a server")
	}
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT seq FROM replication_cursor WHERE mn_id = ?`, mnID).Scan(&seq)
	if err == nil {
		return seq, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return 0, fmt.Errorf("store: read the cursor of %s: %w", mnID, err)
}

func setCursor(ctx context.Context, tx *sql.Tx, mnID string, seq int64) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO replication_cursor (mn_id, seq) VALUES (?, ?)
		 ON CONFLICT(mn_id) DO UPDATE SET seq = excluded.seq`, mnID, seq); err != nil {
		return fmt.Errorf("store: write the cursor of %s: %w", mnID, err)
	}
	return nil
}

func knownKind(kind string) bool {
	switch kind {
	case EventNode, EventNodeRemoved:
		return true
	}
	return false
}

// replicatedUpdate is the upsert half of writing a peer's node: every column but the id.
const replicatedUpdate = `label = excluded.label, domain = excluded.domain, self_uuid = excluded.self_uuid,
	probe_uuid = excluded.probe_uuid, profile_json = excluded.profile_json, state = excluded.state,
	state_reason = excluded.state_reason, created_at = excluded.created_at, approved_at = excluded.approved_at,
	last_seen_at = excluded.last_seen_at, last_request_ip = excluded.last_request_ip,
	agent_version = excluded.agent_version, agent_key_hash = excluded.agent_key_hash,
	agent_key_sealed = excluded.agent_key_sealed, sub_token_hash = excluded.sub_token_hash,
	sub_token_sealed = excluded.sub_token_sealed, failed_cycles = excluded.failed_cycles,
	credential_epoch = excluded.credential_epoch, invite_id = excluded.invite_id, notes = excluded.notes,
	next_check_at = excluded.next_check_at, last_check_at = excluded.last_check_at,
	cert_sha256 = excluded.cert_sha256, home_mn = excluded.home_mn, home_term = excluded.home_term`
