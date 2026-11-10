package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// QueueStats describes how far behind the scheduler is. `termit-mn status` prints it and /metrics exposes it
// as a gauge, because "the mesh looks fine but nothing has been checked for a week" is a failure mode
// that no per-node view shows.
type QueueStats struct {
	// Due is how many scheduled nodes should have been checked by the given time.
	Due int
	// OldestDue is the earliest due next_check_at, nil when no due node carries one (a node that was
	// never scheduled is due but has no timestamp).
	OldestDue *time.Time
	// NextDue is the earliest next_check_at still in the future, nil when nothing is scheduled ahead.
	NextDue *time.Time
}

// Queue reads the scheduler's backlog in one pass over the scheduled states.
func (s *Store) Queue(ctx context.Context, now time.Time) (QueueStats, error) {
	var (
		stats        QueueStats
		oldest, next sql.NullString
		formattedNow = formatTime(now)
	)
	err := s.db.QueryRowContext(ctx, `SELECT
			COUNT(CASE WHEN next_check_at IS NULL OR next_check_at <= ? THEN 1 END),
			MIN(CASE WHEN next_check_at IS NOT NULL AND next_check_at <= ? THEN next_check_at END),
			MIN(CASE WHEN next_check_at > ? THEN next_check_at END)
		FROM uv WHERE state IN (?, ?, ?, ?)`,
		formattedNow, formattedNow, formattedNow,
		scheduleStates[0], scheduleStates[1], scheduleStates[2], scheduleStates[3],
	).Scan(&stats.Due, &oldest, &next)
	if err != nil {
		return QueueStats{}, fmt.Errorf("store: read the check queue: %w", err)
	}
	if oldest.Valid && oldest.String != "" {
		at, err := parseTime(oldest.String)
		if err != nil {
			return QueueStats{}, fmt.Errorf("store: parse the oldest due time: %w", err)
		}
		stats.OldestDue = &at
	}
	if next.Valid && next.String != "" {
		at, err := parseTime(next.String)
		if err != nil {
			return QueueStats{}, fmt.Errorf("store: parse the next due time: %w", err)
		}
		stats.NextDue = &at
	}
	return stats, nil
}

// StateCounts returns how many nodes are in each state. A state with no nodes is present with a zero,
// so a caller can render a fixed set of gauges instead of guessing from the keys it got.
func (s *Store) StateCounts(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{
		string(StateActive):      0,
		string(StateUnavailable): 0,
		string(StateBlocked):     0,
		string(StatePending):     0,
		string(StateRemoved):     0,
	}
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM uv GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("store: count nodes by state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			state string
			count int
		)
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("store: scan a state count: %w", err)
		}
		counts[state] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read state counts: %w", err)
	}
	return counts, nil
}
