package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// --- invites -----------------------------------------------------------------

const inviteColumns = `id, code_hash, created_at, created_by, expires_at, max_uses, used_count, note, revoked_at`

func scanInvite(row interface{ Scan(...any) error }) (Invite, error) {
	var (
		invite               Invite
		createdAt            string
		expiresAt, revokedAt sql.NullString
	)
	err := row.Scan(&invite.ID, &invite.CodeHash, &createdAt, &invite.CreatedBy, &expiresAt,
		&invite.MaxUses, &invite.UsedCount, &invite.Note, &revokedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invite{}, ErrNotFound
		}
		return Invite{}, err
	}
	if invite.CreatedAt, err = parseTime(createdAt); err != nil {
		return Invite{}, fmt.Errorf("store: parse invite created_at: %w", err)
	}
	if invite.ExpiresAt, err = scanNullTime(expiresAt); err != nil {
		return Invite{}, fmt.Errorf("store: parse invite expires_at: %w", err)
	}
	if invite.RevokedAt, err = scanNullTime(revokedAt); err != nil {
		return Invite{}, fmt.Errorf("store: parse invite revoked_at: %w", err)
	}
	return invite, nil
}

// CreateInvite stores a new invite. The caller generates the code and hands over its hash.
func (s *Store) CreateInvite(ctx context.Context, invite Invite) error {
	if invite.ID == "" || invite.CodeHash == "" {
		return errors.New("store: an invite needs an id and a code hash")
	}
	if invite.MaxUses <= 0 {
		invite.MaxUses = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invite (`+inviteColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		invite.ID, invite.CodeHash, formatTime(invite.CreatedAt), invite.CreatedBy,
		nullableTime(invite.ExpiresAt), invite.MaxUses, invite.UsedCount, invite.Note,
		nullableTime(invite.RevokedAt))
	return wrapDuplicate(err, "store: create invite "+invite.ID)
}

// InviteByCodeHash finds the invite a presented code belongs to, including revoked and exhausted ones:
// the caller decides what to tell the user.
func (s *Store) InviteByCodeHash(ctx context.Context, hash string) (Invite, error) {
	return scanInvite(s.db.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invite WHERE code_hash = ?`, hash))
}

// ListInvites returns every invite, newest last.
func (s *Store) ListInvites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+inviteColumns+` FROM invite ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list invites: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Invite
	for rows.Next() {
		invite, err := scanInvite(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan invite: %w", err)
		}
		out = append(out, invite)
	}
	return out, rows.Err()
}

// RedeemInvite consumes one use. The check and the increment happen in one statement so two agents
// cannot both take the last use of an invite.
func (s *Store) RedeemInvite(ctx context.Context, id string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE invite SET used_count = used_count + 1
		 WHERE id = ? AND revoked_at IS NULL
		   AND (expires_at IS NULL OR expires_at > ?)
		   AND (max_uses = 0 OR used_count < max_uses)`,
		id, formatTime(now))
	if err != nil {
		return fmt.Errorf("store: redeem invite: %w", err)
	}
	return expectOne(res, "invite "+id)
}

// RevokeInvite makes an invite unusable without deleting its history.
func (s *Store) RevokeInvite(ctx context.Context, id string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE invite SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(now), id)
	if err != nil {
		return fmt.Errorf("store: revoke invite: %w", err)
	}
	return expectOne(res, "invite "+id)
}

// --- admins ------------------------------------------------------------------

// CreateAdmin stores an operator token hash.
func (s *Store) CreateAdmin(ctx context.Context, admin Admin) error {
	if admin.ID == "" || admin.TokenHash == "" {
		return errors.New("store: an admin needs an id and a token hash")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admin (id, name, token_hash, created_at, last_used_at) VALUES (?, ?, ?, ?, ?)`,
		admin.ID, admin.Name, admin.TokenHash, formatTime(admin.CreatedAt), nullableTime(admin.LastUsedAt))
	return wrapDuplicate(err, "store: create admin "+admin.ID)
}

// AdminByTokenHash resolves the token an admin CLI call presented.
func (s *Store) AdminByTokenHash(ctx context.Context, hash string) (Admin, error) {
	var (
		admin      Admin
		createdAt  string
		lastUsedAt sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, created_at, last_used_at FROM admin WHERE token_hash = ?`, hash).
		Scan(&admin.ID, &admin.Name, &admin.TokenHash, &createdAt, &lastUsedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Admin{}, ErrNotFound
		}
		return Admin{}, fmt.Errorf("store: read admin: %w", err)
	}
	if admin.CreatedAt, err = parseTime(createdAt); err != nil {
		return Admin{}, fmt.Errorf("store: parse admin created_at: %w", err)
	}
	if admin.LastUsedAt, err = scanNullTime(lastUsedAt); err != nil {
		return Admin{}, fmt.Errorf("store: parse admin last_used_at: %w", err)
	}
	return admin, nil
}

// Admins returns every operator token, so the admin service can compare a presented token against
// them. The registry holds a handful of tokens, not thousands: a scan is honest here and avoids an
// index that would leak which prefix matched.
func (s *Store) Admins(ctx context.Context) ([]Admin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, token_hash, created_at, last_used_at FROM admin ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list admins: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Admin
	for rows.Next() {
		var (
			admin      Admin
			createdAt  string
			lastUsedAt sql.NullString
		)
		if err := rows.Scan(&admin.ID, &admin.Name, &admin.TokenHash, &createdAt, &lastUsedAt); err != nil {
			return nil, fmt.Errorf("store: scan admin: %w", err)
		}
		if admin.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("store: parse admin created_at: %w", err)
		}
		if admin.LastUsedAt, err = scanNullTime(lastUsedAt); err != nil {
			return nil, fmt.Errorf("store: parse admin last_used_at: %w", err)
		}
		out = append(out, admin)
	}
	return out, rows.Err()
}

// AdminCount returns how many operator tokens exist.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM admin`).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return count, nil
}

// InviteCount returns how many invites exist, revoked ones included.
func (s *Store) InviteCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM invite`).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count invites: %w", err)
	}
	return count, nil
}

// TouchAdmin records that an operator token was used.
func (s *Store) TouchAdmin(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin SET last_used_at = ? WHERE id = ?`, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("store: touch admin: %w", err)
	}
	return nil
}

// --- settings ----------------------------------------------------------------

// Setting is one key of the mutable server configuration.
type Setting struct {
	Key   string
	Value string
}

// SetSetting writes one key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("store: a setting needs a key")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: set %s: %w", key, err)
	}
	return nil
}

// GetSetting reads one key; a missing key returns ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("store: get %s: %w", key, err)
	}
	return value, nil
}

// Settings returns every key, ordered by name.
func (s *Store) Settings(ctx context.Context) ([]Setting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("store: list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Setting
	for rows.Next() {
		var setting Setting
		if err := rows.Scan(&setting.Key, &setting.Value); err != nil {
			return nil, fmt.Errorf("store: scan setting: %w", err)
		}
		out = append(out, setting)
	}
	return out, rows.Err()
}

// --- audit and checks --------------------------------------------------------

// AuditRecord is one mutation by an operator or the server itself.
type AuditRecord struct {
	At         time.Time
	Actor      string
	Action     string
	Target     string
	DetailJSON string
}

// AppendAudit writes one audit record.
func (s *Store) AppendAudit(ctx context.Context, record AuditRecord) error {
	if record.Action == "" {
		return errors.New("store: an audit record needs an action")
	}
	if record.DetailJSON == "" {
		record.DetailJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (at, actor, action, target, detail_json) VALUES (?, ?, ?, ?, ?)`,
		formatTime(record.At), record.Actor, record.Action, record.Target, record.DetailJSON)
	if err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// AuditCount returns how many records match a target, which the tests use to prove that a mutation was
// recorded exactly once.
func (s *Store) AuditCount(ctx context.Context, target string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit WHERE target = ?`, target).Scan(&count); err != nil {
		return 0, fmt.Errorf("store: count audit: %w", err)
	}
	return count, nil
}

// AppendCheck stores one probe result.
func (s *Store) AppendCheck(ctx context.Context, check Check) error {
	if check.UVID == "" || check.Kind == "" {
		return errors.New("store: a check needs a node and a kind")
	}
	if check.DetailJSON == "" {
		check.DetailJSON = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO "check" (uv_id, at, kind, ok, latency_ms, error_class, detail_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		check.UVID, formatTime(check.At), check.Kind, check.OK, check.LatencyMS, check.ErrorClass,
		check.DetailJSON)
	if err != nil {
		return fmt.Errorf("store: append check: %w", err)
	}
	return nil
}

// LatestCheck returns the newest check of one node and kind; a node that was never probed returns
// ErrNotFound.
func (s *Store) LatestCheck(ctx context.Context, uvID, kind string) (Check, error) {
	var (
		check Check
		at    string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, uv_id, at, kind, ok, latency_ms, error_class, detail_json
		 FROM "check" WHERE uv_id = ? AND kind = ? ORDER BY at DESC, id DESC LIMIT 1`, uvID, kind).
		Scan(&check.ID, &check.UVID, &at, &check.Kind, &check.OK, &check.LatencyMS, &check.ErrorClass,
			&check.DetailJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Check{}, ErrNotFound
		}
		return Check{}, fmt.Errorf("store: read latest check: %w", err)
	}
	if check.At, err = parseTime(at); err != nil {
		return Check{}, fmt.Errorf("store: parse check time: %w", err)
	}
	return check, nil
}
