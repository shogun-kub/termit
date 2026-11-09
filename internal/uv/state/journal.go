package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// JournalSchemaVersion is the journal schema this build understands.
const JournalSchemaVersion = 1

// maxRuns caps the journal so a repeatedly failing install cannot grow it without bound. The last
// run is the one an operator needs; the rest are context.
const maxRuns = 20

// Step statuses, as written to the journal.
const (
	// StepRunning means the step started and has no result yet: an interrupted run.
	StepRunning = "running"
	// StepOK means the step ran and changed the host.
	StepOK = "ok"
	// StepPresent means the step's effect was already in place and nothing was done.
	StepPresent = "present"
	// StepFailed means the step ran and failed.
	StepFailed = "failed"
	// StepPlanned means a dry run reported the step without running it.
	StepPlanned = "planned"
)

// Run results.
const (
	// RunRunning means the run did not finish: the process was interrupted or crashed.
	RunRunning = "running"
	// RunOK means every step either ran or was already satisfied.
	RunOK = "ok"
	// RunFailed means a step failed and the run stopped there.
	RunFailed = "failed"
)

// Journal is the record of install runs, kept so a failed install can be diagnosed and resumed.
//
// Resuming does not replay from the journal alone: every step reports whether its effect is already
// present, and the engine re-checks that on each run. The journal is the history and the human
// answer to "what did the installer do last time, and where did it stop".
type Journal struct {
	SchemaVersion int   `json:"schema_version"`
	Runs          []Run `json:"runs"`
}

// Run is one `uv init` or `uv uninstall` execution.
type Run struct {
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Version    string       `json:"version,omitempty"`
	Command    string       `json:"command,omitempty"`
	Result     string       `json:"result"`
	Detail     string       `json:"detail,omitempty"`
	Steps      []StepRecord `json:"steps,omitempty"`
}

// StepRecord is the outcome of one step inside a run.
type StepRecord struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// LoadJournal reads the journal. A missing file is an empty journal, not an error: a fresh host has
// never run the installer.
func LoadJournal(filename string) (*Journal, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- the journal path comes from configuration.
	if errors.Is(err, fs.ErrNotExist) {
		return &Journal{SchemaVersion: JournalSchemaVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("journal: read %s: %w", filename, err)
	}
	j := &Journal{}
	if err := json.Unmarshal(data, j); err != nil {
		return nil, fmt.Errorf("journal: parse %s: %w", filename, err)
	}
	if j.SchemaVersion > JournalSchemaVersion {
		return nil, fmt.Errorf("journal: %s has schema_version %d, newer than this build supports (%d)",
			filename, j.SchemaVersion, JournalSchemaVersion)
	}
	j.SchemaVersion = JournalSchemaVersion
	return j, nil
}

// Save writes the journal atomically with 0600 permissions.
func (j *Journal) Save(filename string) error {
	j.SchemaVersion = JournalSchemaVersion
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("journal: encode: %w", err)
	}
	data = append(data, '\n')
	return WriteFileAtomic(filename, data, 0o600)
}

// StartRun appends a run in the running state and returns it.
func (j *Journal) StartRun(now time.Time, version, command string) *Run {
	j.Runs = append(j.Runs, Run{
		StartedAt: now.UTC(),
		Version:   version,
		Command:   command,
		Result:    RunRunning,
	})
	if len(j.Runs) > maxRuns {
		j.Runs = j.Runs[len(j.Runs)-maxRuns:]
	}
	return &j.Runs[len(j.Runs)-1]
}

// BeginStep records that a step started.
func (r *Run) BeginStep(name string, now time.Time) {
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			r.Steps[i].Status = StepRunning
			return
		}
	}
	r.Steps = append(r.Steps, StepRecord{Name: name, Status: StepRunning, StartedAt: now.UTC()})
}

// FinishStep records a step's outcome.
func (r *Run) FinishStep(name, status, detail string, now time.Time) {
	finished := now.UTC()
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			r.Steps[i].Status = status
			r.Steps[i].Detail = detail
			r.Steps[i].FinishedAt = &finished
			return
		}
	}
	r.Steps = append(r.Steps, StepRecord{
		Name: name, Status: status, Detail: detail, StartedAt: finished, FinishedAt: &finished,
	})
}

// Finish records the run's overall result.
func (r *Run) Finish(result, detail string, now time.Time) {
	finished := now.UTC()
	r.Result = result
	r.Detail = detail
	r.FinishedAt = &finished
}

// Last returns the most recent run, or nil if the journal is empty.
func (j *Journal) Last() *Run {
	if len(j.Runs) == 0 {
		return nil
	}
	return &j.Runs[len(j.Runs)-1]
}

// Interrupted reports whether the last run started and never finished. That is what a crashed
// install looks like, and it is worth saying so instead of pretending the host is fresh.
func (j *Journal) Interrupted() bool {
	last := j.Last()
	return last != nil && last.Result == RunRunning
}
