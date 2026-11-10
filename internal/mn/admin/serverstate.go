package admin

import (
	"context"
	"os"
	"time"
)

// ServerInfo is what the process knows about itself and the CLI cannot: the caller fills it at
// startup. Zero values mean "unknown" and are printed as such, never hidden.
type ServerInfo struct {
	Domain        string
	Version       string
	Listen        string
	Started       time.Time
	SchemaVersion int
	// ProberUp is false when no probe worker could be built: every check is then unknown.
	ProberUp bool
	// ProberDetail is where the worker came from, or why there is none.
	ProberDetail        string
	CertificateNotAfter *time.Time
}

// ServerState is the server half of the `mn status` dashboard: the process, the database and the
// scheduler's backlog. It carries no node identifier; the per-node view is `uv.list`/`uv.show`.
type ServerState struct {
	Domain        string    `json:"domain"`
	Listen        string    `json:"listen"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	SchemaVersion int       `json:"schema_version"`
	Database      struct {
		Path  string `json:"path"`
		Bytes int64  `json:"bytes"`
	} `json:"database"`
	Prober struct {
		Up     bool   `json:"up"`
		Detail string `json:"detail,omitempty"`
	} `json:"prober"`
	Certificate *struct {
		NotAfter  time.Time `json:"not_after"`
		DaysUntil int       `json:"days_until"`
	} `json:"certificate,omitempty"`
	Nodes map[string]int `json:"nodes"`
	Queue struct {
		Due              int        `json:"due"`
		OldestDueAt      *time.Time `json:"oldest_due_at,omitempty"`
		OldestDueSeconds int64      `json:"oldest_due_seconds"`
		NextDueAt        *time.Time `json:"next_due_at,omitempty"`
	} `json:"queue"`
}

// serverState answers with the dashboard's server block. Everything in it is read on demand, so a
// number that disagrees with itself would be more confusing than a slow one is annoying.
func (s *Service) serverState(ctx context.Context) (any, *Error) {
	now := s.Now().UTC()

	counts, err := s.Store.StateCounts(ctx)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	queue, err := s.Store.Queue(ctx, now)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}

	state := ServerState{
		Domain:        s.Server.Domain,
		Listen:        s.Server.Listen,
		Version:       s.Server.Version,
		StartedAt:     s.Server.Started,
		SchemaVersion: s.Server.SchemaVersion,
		Nodes:         counts,
	}
	if !s.Server.Started.IsZero() {
		state.UptimeSeconds = int64(now.Sub(s.Server.Started).Seconds())
		if state.UptimeSeconds < 0 {
			state.UptimeSeconds = 0
		}
	}
	state.Database.Path = s.Store.Path()
	if info, err := os.Stat(s.Store.Path()); err == nil {
		state.Database.Bytes = info.Size()
	}
	state.Prober.Up = s.Server.ProberUp
	state.Prober.Detail = s.Server.ProberDetail
	state.Queue.Due = queue.Due
	state.Queue.OldestDueAt = queue.OldestDue
	state.Queue.NextDueAt = queue.NextDue
	if queue.OldestDue != nil {
		state.Queue.OldestDueSeconds = int64(now.Sub(*queue.OldestDue).Seconds())
		if state.Queue.OldestDueSeconds < 0 {
			state.Queue.OldestDueSeconds = 0
		}
	}
	if s.Server.CertificateNotAfter != nil && !s.Server.CertificateNotAfter.IsZero() {
		state.Certificate = &struct {
			NotAfter  time.Time `json:"not_after"`
			DaysUntil int       `json:"days_until"`
		}{
			NotAfter:  *s.Server.CertificateNotAfter,
			DaysUntil: int(s.Server.CertificateNotAfter.Sub(now).Hours() / 24),
		}
	}
	return state, nil
}
