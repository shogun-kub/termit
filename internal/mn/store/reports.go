package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A replica that meets a node whose home it is not serves the last desired state it holds and keeps the
// node's report for the home (docs/IMPLEMENTATION_PLAN.md §5.2.1 decision 3). The report is a complete
// statement of what the node serves, so the newest one replaces an older one rather than accumulating.

// QueueReport stores a node's self-report for its home to apply, replacing any report already waiting.
func (s *Store) QueueReport(ctx context.Context, uvID string, at time.Time, payload []byte) error {
	if uvID == "" {
		return errors.New("store: a report belongs to a node")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO mesh_report (uv_id, at, payload) VALUES (?, ?, ?)
		 ON CONFLICT(uv_id) DO UPDATE SET at = excluded.at, payload = excluded.payload`,
		uvID, formatTime(at), payload); err != nil {
		return fmt.Errorf("store: queue the report of %s: %w", uvID, err)
	}
	return nil
}

// PendingReport is one queued self-report and the server that has to apply it. Home is empty when the node
// row is gone: the caller drops such a report rather than delivering it nowhere.
type PendingReport struct {
	UV      string
	Home    string
	At      time.Time
	Payload []byte
}

// PendingReports returns the queued reports oldest first, each with the home of its node.
func (s *Store) PendingReports(ctx context.Context) ([]PendingReport, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT r.uv_id, COALESCE(u.home_mn, ''), r.at, r.payload
		 FROM mesh_report r LEFT JOIN uv u ON u.id = r.uv_id
		 ORDER BY r.at, r.uv_id`)
	if err != nil {
		return nil, fmt.Errorf("store: read the queued reports: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var pending []PendingReport
	for rows.Next() {
		var (
			report PendingReport
			at     string
		)
		if err := rows.Scan(&report.UV, &report.Home, &at, &report.Payload); err != nil {
			return nil, fmt.Errorf("store: scan a queued report: %w", err)
		}
		if report.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("store: parse a report time: %w", err)
		}
		pending = append(pending, report)
	}
	return pending, rows.Err()
}

// DropReport forgets a queued report: the home applied it, or the node it was about is gone.
func (s *Store) DropReport(ctx context.Context, uvID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mesh_report WHERE uv_id = ?`, uvID); err != nil {
		return fmt.Errorf("store: drop the report of %s: %w", uvID, err)
	}
	return nil
}
