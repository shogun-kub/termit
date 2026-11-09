package install

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/state"
)

// CheckResult is a step's answer to "is my effect already present?".
type CheckResult struct {
	// Satisfied means nothing needs to be done; a re-run of the step would be a no-op.
	Satisfied bool
	// Detail explains what was found, for the operator and for the journal.
	Detail string
}

// Step is one idempotent unit of installation.
type Step interface {
	// Name identifies the step in the journal, in --only/--skip and in the summary.
	Name() string
	// Check reports whether the step's effect is already in place.
	Check(ctx context.Context) (CheckResult, error)
	// Run produces the effect. It must be safe to call after a partial failure.
	Run(ctx context.Context) error
}

// Options configure an Installer. Only Config is required.
type Options struct {
	Config *config.Config
	Exec   Executor
	HTTP   *http.Client
	Logger *slog.Logger
	Now    func() time.Time
	// Version is the agent build that wrote this journal entry.
	Version string
	// Arch is the architecture of the target host; the host's own by default.
	Arch string
}

// Installer runs the install steps in order and records them in the journal.
type Installer struct {
	cfg         *config.Config
	st          *state.State
	journal     *state.Journal
	journalPath string
	version     string
	env         *stepEnv
	steps       []Step
}

// New loads the node's state and journal and wires the steps. It never touches the host beyond
// reading its state file: everything that changes the host happens inside Run.
func New(opts Options) (*Installer, error) {
	if opts.Config == nil {
		return nil, errors.New("install: a configuration is required")
	}
	if err := opts.Config.Validate(); err != nil {
		return nil, err
	}
	cfg := opts.Config
	if opts.Exec == nil {
		opts.Exec = OSExecutor{}
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 15 * time.Minute}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Arch == "" {
		opts.Arch = runtime.GOARCH
	}

	st, err := state.LoadInstalled(cfg.Paths.StatePath())
	switch {
	case errors.Is(err, state.ErrNotInstalled):
		st = state.New(cfg.Domain, opts.Now())
	case err != nil:
		return nil, err
	}
	if st.Domain != "" && st.Domain != cfg.Domain {
		return nil, fmt.Errorf(
			"state at %s belongs to domain %q, configuration says %q; run `uv migrate` instead of reinstalling",
			cfg.Paths.StatePath(), st.Domain, cfg.Domain)
	}
	if st.Domain == "" {
		st.Domain = cfg.Domain
		st.Touch()
	}

	journal, err := state.LoadJournal(cfg.Paths.JournalPath())
	if err != nil {
		return nil, err
	}

	env := &stepEnv{
		cfg:       cfg,
		st:        st,
		statePath: cfg.Paths.StatePath(),
		exec:      opts.Exec,
		http:      opts.HTTP,
		log:       opts.Logger,
		now:       opts.Now,
		arch:      opts.Arch,
	}
	return &Installer{
		cfg:         cfg,
		st:          st,
		journal:     journal,
		journalPath: cfg.Paths.JournalPath(),
		version:     opts.Version,
		env:         env,
		steps: []Step{
			&preflightStep{env},
			&userStep{env},
			&dirsStep{env},
			&packagesStep{env},
			&xrayStep{env},
			&identityStep{env},
		},
	}, nil
}

// Steps returns the pipeline in execution order.
func (i *Installer) Steps() []Step { return i.steps }

// State is the node's state, which steps update in place.
func (i *Installer) State() *state.State { return i.st }

// Journal is the install history.
func (i *Installer) Journal() *state.Journal { return i.journal }

// RunOptions select and shape one run.
type RunOptions struct {
	// Command names the run in the journal, for example "init".
	Command string
	// Only restricts the run to the named steps; empty means all of them.
	Only []string
	// Skip excludes the named steps.
	Skip []string
	// DryRun reports what would happen without touching the host and without journaling.
	DryRun bool
	// KeepGoing continues after a failing step. The report carries every error; later steps that
	// depend on the failed one will usually fail too.
	KeepGoing bool
}

// StepReport is the outcome of one step in one run.
type StepReport struct {
	Name     string
	Status   string
	Detail   string
	Err      error
	Duration time.Duration
}

// Report is the outcome of one run.
type Report struct {
	Started  time.Time
	Finished time.Time
	Steps    []StepReport
	Run      *state.Run
}

// Counts returns how many steps ended in each status.
func (r *Report) Counts() map[string]int {
	counts := make(map[string]int, 5)
	for _, step := range r.Steps {
		counts[step.Status]++
	}
	return counts
}

// Failed returns the steps that failed.
func (r *Report) Failed() []StepReport {
	var failed []StepReport
	for _, step := range r.Steps {
		if step.Err != nil {
			failed = append(failed, step)
		}
	}
	return failed
}

// Run executes the selected steps in order. A step whose Check reports satisfied is recorded as
// "present" and does nothing; that is what makes a re-run converge.
func (i *Installer) Run(ctx context.Context, opts RunOptions) (*Report, error) {
	steps, err := i.selectSteps(opts)
	if err != nil {
		return nil, err
	}
	started := i.env.now()
	report := &Report{Started: started}

	if opts.DryRun {
		for _, step := range steps {
			stepStarted := i.env.now()
			res, checkErr := step.Check(ctx)
			entry := StepReport{Name: step.Name(), Duration: i.env.now().Sub(stepStarted)}
			switch {
			case checkErr != nil:
				entry.Status = state.StepFailed
				entry.Err = checkErr
			case res.Satisfied:
				entry.Status = state.StepPresent
				entry.Detail = res.Detail
			default:
				entry.Status = state.StepPlanned
				entry.Detail = res.Detail
			}
			report.Steps = append(report.Steps, entry)
		}
		report.Finished = i.env.now()
		return report, nil
	}

	run := i.journal.StartRun(started, i.version, opts.Command)
	report.Run = run
	if err := i.journal.Save(i.journalPath); err != nil {
		return nil, err
	}

	var failures []error
	for _, step := range steps {
		run.BeginStep(step.Name(), i.env.now())
		if err := i.journal.Save(i.journalPath); err != nil {
			return nil, err
		}
		stepStarted := i.env.now()

		res, checkErr := step.Check(ctx)
		if checkErr == nil && res.Satisfied {
			report.Steps = append(report.Steps, StepReport{
				Name: step.Name(), Status: state.StepPresent, Detail: res.Detail,
				Duration: i.env.now().Sub(stepStarted),
			})
			run.FinishStep(step.Name(), state.StepPresent, res.Detail, i.env.now())
			if err := i.journal.Save(i.journalPath); err != nil {
				return nil, err
			}
			continue
		}
		if checkErr == nil {
			i.env.log.Info("running install step", "step", step.Name(), "reason", res.Detail)
		}

		runErr := checkErr
		if runErr == nil {
			runErr = step.Run(ctx)
		}
		if runErr == nil {
			runErr = i.env.saveState()
		}
		if runErr != nil {
			report.Steps = append(report.Steps, StepReport{
				Name: step.Name(), Status: state.StepFailed, Err: runErr,
				Duration: i.env.now().Sub(stepStarted),
			})
			run.FinishStep(step.Name(), state.StepFailed, runErr.Error(), i.env.now())
			failures = append(failures, fmt.Errorf("%s: %w", step.Name(), runErr))
			if !opts.KeepGoing {
				break
			}
		} else {
			report.Steps = append(report.Steps, StepReport{
				Name: step.Name(), Status: state.StepOK, Duration: i.env.now().Sub(stepStarted),
			})
			run.FinishStep(step.Name(), state.StepOK, "", i.env.now())
		}
		if err := i.journal.Save(i.journalPath); err != nil {
			return nil, err
		}
	}

	report.Finished = i.env.now()
	if len(failures) > 0 {
		message := errors.Join(failures...)
		run.Finish(state.RunFailed, message.Error(), report.Finished)
		_ = i.journal.Save(i.journalPath)
		return report, message
	}
	run.Finish(state.RunOK, "", report.Finished)
	if err := i.journal.Save(i.journalPath); err != nil {
		return report, err
	}
	return report, nil
}

// selectSteps applies Only and Skip, rejecting names the pipeline does not know: a typo must not
// silently install something else.
func (i *Installer) selectSteps(opts RunOptions) ([]Step, error) {
	known := make(map[string]bool, len(i.steps))
	for _, step := range i.steps {
		known[step.Name()] = true
	}
	skip := make(map[string]bool, len(opts.Skip))
	for _, name := range opts.Skip {
		if !known[name] {
			return nil, fmt.Errorf("unknown step %q in --skip (steps: %s)", name, strings.Join(i.names(), ", "))
		}
		skip[name] = true
	}
	only := make(map[string]bool, len(opts.Only))
	for _, name := range opts.Only {
		if !known[name] {
			return nil, fmt.Errorf("unknown step %q in --only (steps: %s)", name, strings.Join(i.names(), ", "))
		}
		only[name] = true
	}

	var selected []Step
	for _, step := range i.steps {
		if skip[step.Name()] {
			continue
		}
		if len(only) > 0 && !only[step.Name()] {
			continue
		}
		selected = append(selected, step)
	}
	return selected, nil
}

func (i *Installer) names() []string {
	names := make([]string, 0, len(i.steps))
	for _, step := range i.steps {
		names = append(names, step.Name())
	}
	return names
}
