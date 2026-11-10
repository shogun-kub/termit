package store

import (
	"context"
	"fmt"
)

// Checks returns a node's check rows, newest first. It is what `termit-mn uv show` prints and what the
// scheduler's tests assert on.
func (s *Store) Checks(ctx context.Context, uvID string, limit int) ([]Check, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, uv_id, at, kind, ok, latency_ms, error_class, detail_json
		FROM "check" WHERE uv_id = ? ORDER BY at DESC, id DESC LIMIT ?`, uvID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read checks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var checks []Check
	for rows.Next() {
		var (
			check Check
			at    string
			ok    int
		)
		if err := rows.Scan(&check.ID, &check.UVID, &at, &check.Kind, &ok, &check.LatencyMS,
			&check.ErrorClass, &check.DetailJSON); err != nil {
			return nil, fmt.Errorf("store: scan check: %w", err)
		}
		if check.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("store: parse check time: %w", err)
		}
		check.OK = ok != 0
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read checks: %w", err)
	}
	return checks, nil
}
