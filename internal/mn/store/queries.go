package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// State is a node's lifecycle state on the server. The transitions live in the scheduler; the store
// only records what it is told.
type State string

const (
	StatePending     State = "pending"
	StateActive      State = "active"
	StateUnavailable State = "unavailable"
	StateBlocked     State = "blocked"
	StateRemoved     State = "removed"
)

// Valid reports whether s is one of the states this build knows.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateActive, StateUnavailable, StateBlocked, StateRemoved:
		return true
	}
	return false
}

// UV is one registered node.
type UV struct {
	ID       string
	Label    string
	Domain   string
	SelfUUID string
	// ProbeUUID is the credential the prober uses; it is never given to a client.
	ProbeUUID   string
	ProfileJSON string
	State       State
	StateReason string

	CreatedAt     time.Time
	ApprovedAt    *time.Time
	LastSeenAt    *time.Time
	LastRequestIP string
	AgentVersion  string

	AgentKeyHash string
	SubTokenHash string

	FailedCycles    int
	CredentialEpoch int
	InviteID        string
	Notes           string
}

// Invite is a registration code. Only its hash is stored.
type Invite struct {
	ID        string
	CodeHash  string
	CreatedAt time.Time
	CreatedBy string
	ExpiresAt *time.Time
	MaxUses   int
	UsedCount int
	Note      string
	RevokedAt *time.Time
}

// Usable reports whether the invite can still be redeemed at now.
func (i Invite) Usable(now time.Time) bool {
	switch {
	case i.RevokedAt != nil:
		return false
	case i.ExpiresAt != nil && !now.Before(*i.ExpiresAt):
		return false
	case i.MaxUses > 0 && i.UsedCount >= i.MaxUses:
		return false
	}
	return true
}

// Admin is an operator token. Only its hash is stored.
type Admin struct {
	ID         string
	Name       string
	TokenHash  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Check is one probe result.
type Check struct {
	ID         int64
	UVID       string
	At         time.Time
	Kind       string
	OK         bool
	LatencyMS  int
	ErrorClass string
	DetailJSON string
}

// Event is one state transition.
type Event struct {
	ID        int64
	UVID      string
	At        time.Time
	FromState State
	ToState   State
	Reason    string
	Actor     string
}

var (
	// ErrNotFound reports a missing row.
	ErrNotFound = errors.New("store: not found")
	// ErrDuplicate reports a uniqueness violation, which the API turns into a problem code.
	ErrDuplicate = errors.New("store: duplicate")
)

// timeLayout keeps timestamps readable in the file and sortable as text.
const timeLayout = time.RFC3339Nano

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(text string) (time.Time, error) { return time.Parse(timeLayout, text) }

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func scanNullTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// --- nodes -------------------------------------------------------------------

const uvColumns = `id, label, domain, self_uuid, probe_uuid, profile_json, state, state_reason,
	created_at, approved_at, last_seen_at, last_request_ip, agent_version,
	agent_key_hash, sub_token_hash, failed_cycles, credential_epoch, invite_id, notes`

func scanUV(row interface{ Scan(...any) error }) (UV, error) {
	var (
		uv                     UV
		createdAt              string
		approvedAt, lastSeenAt sql.NullString
	)
	err := row.Scan(&uv.ID, &uv.Label, &uv.Domain, &uv.SelfUUID, &uv.ProbeUUID, &uv.ProfileJSON,
		&uv.State, &uv.StateReason, &createdAt, &approvedAt, &lastSeenAt, &uv.LastRequestIP,
		&uv.AgentVersion, &uv.AgentKeyHash, &uv.SubTokenHash, &uv.FailedCycles,
		&uv.CredentialEpoch, &uv.InviteID, &uv.Notes)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UV{}, ErrNotFound
		}
		return UV{}, err
	}
	if uv.CreatedAt, err = parseTime(createdAt); err != nil {
		return UV{}, fmt.Errorf("store: parse created_at: %w", err)
	}
	if uv.ApprovedAt, err = scanNullTime(approvedAt); err != nil {
		return UV{}, fmt.Errorf("store: parse approved_at: %w", err)
	}
	if uv.LastSeenAt, err = scanNullTime(lastSeenAt); err != nil {
		return UV{}, fmt.Errorf("store: parse last_seen_at: %w", err)
	}
	return uv, nil
}

// CreateUV inserts a node. A duplicate domain or id is reported as ErrDuplicate.
func (s *Store) CreateUV(ctx context.Context, uv UV) error {
	if uv.ID == "" || uv.Domain == "" {
		return errors.New("store: a node needs an id and a domain")
	}
	if !uv.State.Valid() {
		return fmt.Errorf("store: %q is not a state", uv.State)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO uv (`+uvColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uv.ID, uv.Label, uv.Domain, uv.SelfUUID, uv.ProbeUUID, uv.ProfileJSON, string(uv.State),
		uv.StateReason, formatTime(uv.CreatedAt), nullableTime(uv.ApprovedAt), nullableTime(uv.LastSeenAt),
		uv.LastRequestIP, uv.AgentVersion, uv.AgentKeyHash, uv.SubTokenHash, uv.FailedCycles,
		uv.CredentialEpoch, uv.InviteID, uv.Notes)
	return wrapDuplicate(err, "store: create node "+uv.ID)
}

// CreateUVRedeemingInvite inserts a node and consumes one use of its invite in one transaction. A node
// must never exist without the invite that admitted it, and an invite must never be spent on a node
// that was not written.
func (s *Store) CreateUVRedeemingInvite(ctx context.Context, uv UV, invite Invite, now time.Time) error {
	if uv.ID == "" || uv.Domain == "" {
		return errors.New("store: a node needs an id and a domain")
	}
	if !uv.State.Valid() {
		return fmt.Errorf("store: %q is not a state", uv.State)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin enrollment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if invite.ID != "" {
		res, err := tx.ExecContext(ctx,
			`UPDATE invite SET used_count = used_count + 1
			 WHERE id = ? AND revoked_at IS NULL
			   AND (expires_at IS NULL OR expires_at > ?)
			   AND (max_uses = 0 OR used_count < max_uses)`,
			invite.ID, formatTime(now))
		if err != nil {
			return fmt.Errorf("store: redeem invite: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: rows affected: %w", err)
		}
		if affected == 0 {
			// The invite was used up between the validation and this moment.
			return fmt.Errorf("store: invite %s: %w", invite.ID, ErrNotFound)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO uv (`+uvColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uv.ID, uv.Label, uv.Domain, uv.SelfUUID, uv.ProbeUUID, uv.ProfileJSON, string(uv.State),
		uv.StateReason, formatTime(uv.CreatedAt), nullableTime(uv.ApprovedAt), nullableTime(uv.LastSeenAt),
		uv.LastRequestIP, uv.AgentVersion, uv.AgentKeyHash, uv.SubTokenHash, uv.FailedCycles,
		uv.CredentialEpoch, uv.InviteID, uv.Notes); err != nil {
		return wrapDuplicate(err, "store: create node "+uv.ID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO uv_event (uv_id, at, from_state, to_state, reason, actor)
		VALUES (?, ?, ?, ?, ?, ?)`,
		uv.ID, formatTime(uv.CreatedAt), "", string(uv.State), "enrolled", "agent"); err != nil {
		return fmt.Errorf("store: append enrollment event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit enrollment: %w", err)
	}
	return nil
}

// UVByID returns one node.
func (s *Store) UVByID(ctx context.Context, id string) (UV, error) {
	return scanUV(s.db.QueryRowContext(ctx, `SELECT `+uvColumns+` FROM uv WHERE id = ?`, id))
}

// UVByDomain returns one node by its domain; a domain is unique across the mesh.
func (s *Store) UVByDomain(ctx context.Context, domain string) (UV, error) {
	return scanUV(s.db.QueryRowContext(ctx, `SELECT `+uvColumns+` FROM uv WHERE domain = ?`, domain))
}

// UVByAgentKeyHash resolves the agent key an incoming request presented.
func (s *Store) UVByAgentKeyHash(ctx context.Context, hash string) (UV, error) {
	return scanUV(s.db.QueryRowContext(ctx, `SELECT `+uvColumns+` FROM uv WHERE agent_key_hash = ? AND agent_key_hash <> ''`, hash))
}

// UVBySubTokenHash resolves a subscription token.
func (s *Store) UVBySubTokenHash(ctx context.Context, hash string) (UV, error) {
	return scanUV(s.db.QueryRowContext(ctx, `SELECT `+uvColumns+` FROM uv WHERE sub_token_hash = ? AND sub_token_hash <> ''`, hash))
}

// UVFilter narrows ListUVs. The zero value lists every node that is not removed.
type UVFilter struct {
	States         []State
	IncludeRemoved bool
}

// ListUVs returns the registry, newest last.
func (s *Store) ListUVs(ctx context.Context, filter UVFilter) ([]UV, error) {
	query := `SELECT ` + uvColumns + ` FROM uv`
	var (
		conditions []string
		args       []any
	)
	if len(filter.States) > 0 {
		placeholders := ""
		for i, state := range filter.States {
			if i > 0 {
				placeholders += ", "
			}
			placeholders += "?"
			args = append(args, string(state))
		}
		conditions = append(conditions, "state IN ("+placeholders+")")
	} else if !filter.IncludeRemoved {
		conditions = append(conditions, "state <> ?")
		args = append(args, string(StateRemoved))
	}
	if len(conditions) > 0 {
		query += " WHERE " + conditions[0]
		for _, condition := range conditions[1:] {
			query += " AND " + condition
		}
	}
	query += " ORDER BY created_at ASC, id ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UV
	for rows.Next() {
		uv, err := scanUV(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan node: %w", err)
		}
		out = append(out, uv)
	}
	return out, rows.Err()
}

// SetUVState records a transition and appends the event in one transaction. from_state is read inside
// the transaction, so two writers cannot interleave and produce a state nobody wrote.
func (s *Store) SetUVState(ctx context.Context, id string, to State, reason, actor string, now time.Time) (Event, error) {
	if !to.Valid() {
		return Event{}, fmt.Errorf("store: %q is not a state", to)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("store: begin state change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var from State
	if err := tx.QueryRowContext(ctx, `SELECT state FROM uv WHERE id = ?`, id).Scan(&from); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Event{}, ErrNotFound
		}
		return Event{}, fmt.Errorf("store: read state: %w", err)
	}
	at := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE uv SET state = ?, state_reason = ? WHERE id = ?`, string(to), reason, id); err != nil {
		return Event{}, fmt.Errorf("store: update state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO uv_event (uv_id, at, from_state, to_state, reason, actor)
		VALUES (?, ?, ?, ?, ?, ?)`, id, at, string(from), string(to), reason, actor); err != nil {
		return Event{}, fmt.Errorf("store: append event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("store: commit state change: %w", err)
	}
	return Event{UVID: id, At: now, FromState: from, ToState: to, Reason: reason, Actor: actor}, nil
}

// SetSubTokenHash replaces a node's subscription token digest. The old token stops working at once,
// which is the point of rotating it.
func (s *Store) SetSubTokenHash(ctx context.Context, id, hash string) error {
	if hash == "" {
		return errors.New("store: refusing an empty subscription token digest")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE uv SET sub_token_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("store: set subscription token for %s: %w", id, err)
	}
	return expectOne(res, id)
}

// SetAgentKeyHash replaces a node's agent key digest. The old key stops working at once.
func (s *Store) SetAgentKeyHash(ctx context.Context, id, hash string) error {
	if hash == "" {
		return errors.New("store: refusing an empty agent key digest")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE uv SET agent_key_hash = ? WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("store: set agent key for %s: %w", id, err)
	}
	return expectOne(res, id)
}

// ApproveUV stamps the approval of a node that an operator let into the mesh.
func (s *Store) ApproveUV(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE uv SET approved_at = COALESCE(approved_at, ?) WHERE id = ?`,
		formatTime(now), id)
	if err != nil {
		return fmt.Errorf("store: approve node %s: %w", id, err)
	}
	return nil
}

// TouchUV records a successful agent request.
func (s *Store) TouchUV(ctx context.Context, id, ip, agentVersion string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE uv SET last_seen_at = ?, last_request_ip = ?, agent_version = ? WHERE id = ?`,
		formatTime(now), ip, agentVersion, id)
	if err != nil {
		return fmt.Errorf("store: touch node %s: %w", id, err)
	}
	return nil
}

// UpdateUV writes the mutable half of a node record: label, notes, profile and secrets hashes are
// changed by admin actions and credential issuance, never by an agent request.
func (s *Store) UpdateUV(ctx context.Context, uv UV) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE uv SET label = ?, profile_json = ?, probe_uuid = ?, agent_key_hash = ?, sub_token_hash = ?,
		 failed_cycles = ?, credential_epoch = ?, notes = ? WHERE id = ?`,
		uv.Label, uv.ProfileJSON, uv.ProbeUUID, uv.AgentKeyHash, uv.SubTokenHash,
		uv.FailedCycles, uv.CredentialEpoch, uv.Notes, uv.ID)
	if err != nil {
		return wrapDuplicate(err, "store: update node "+uv.ID)
	}
	return expectOne(res, uv.ID)
}

// UVEvents returns the transition log of one node, newest first.
func (s *Store) UVEvents(ctx context.Context, uvID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, uv_id, at, from_state, to_state, reason, actor FROM uv_event
		 WHERE uv_id = ? ORDER BY at DESC, id DESC LIMIT ?`, uvID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var (
			event              Event
			at                 string
			fromState, toState string
		)
		if err := rows.Scan(&event.ID, &event.UVID, &at, &fromState, &toState, &event.Reason, &event.Actor); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		if event.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("store: parse event time: %w", err)
		}
		event.FromState, event.ToState = State(fromState), State(toState)
		out = append(out, event)
	}
	return out, rows.Err()
}

func wrapDuplicate(err error, context string) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %s", ErrDuplicate, context)
	}
	return fmt.Errorf("%s: %w", context, err)
}

func expectOne(res sql.Result, id string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("store: %s: %w", id, ErrNotFound)
	}
	return nil
}
