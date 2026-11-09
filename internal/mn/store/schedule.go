package store

import (
	"context"
	"fmt"
	"time"
)

// scheduleStates are the states worth probing. A removed node is not in the mesh; a blocked one is
// probed so that an unblock request has evidence to point at.
var scheduleStates = []string{"pending", "active", "unavailable", "blocked"}

// DueUVs returns the nodes whose next check is due, oldest first. A node that has never been scheduled
// counts as due: it was enrolled and nobody has looked at it yet.
func (s *Store) DueUVs(ctx context.Context, now time.Time, limit int) ([]UV, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+uvColumns+` FROM uv
		WHERE state IN (?, ?, ?, ?)
		  AND (next_check_at IS NULL OR next_check_at <= ?)
		ORDER BY COALESCE(next_check_at, ''), id
		LIMIT ?`,
		scheduleStates[0], scheduleStates[1], scheduleStates[2], scheduleStates[3], formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("store: due nodes: %w", err)
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read due nodes: %w", err)
	}
	return nodes, nil
}

// ScheduleCheck records when a node is next due and when it was last checked. The scheduler writes the
// next time before probing, so a crash mid-cycle cannot make a node due forever.
func (s *Store) ScheduleCheck(ctx context.Context, id string, next, last time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE uv SET next_check_at = ?, last_check_at = ? WHERE id = ?`,
		formatTime(next), formatTime(last), id)
	if err != nil {
		return fmt.Errorf("store: schedule node %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetFailedCycles stores the consecutive failed cycles, for display and for the two-strike rule's audit
// trail. The rule itself is in the transition table, not here.
func (s *Store) SetFailedCycles(ctx context.Context, id string, cycles int) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE uv SET failed_cycles = ? WHERE id = ?`, cycles, id); err != nil {
		return fmt.Errorf("store: set failed cycles for %s: %w", id, err)
	}
	return nil
}
