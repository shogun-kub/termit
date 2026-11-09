package install

import (
	"context"
	"sync"
	"testing"
)

// fakeExecutor answers commands from a handler and records what was asked. It is how the install
// steps are tested without root, apt or systemd.
type fakeExecutor struct {
	mu      sync.Mutex
	handler func(cmd Cmd) (Result, error)
	calls   []Cmd
}

func (f *fakeExecutor) Run(ctx context.Context, cmd Cmd) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	handler := f.handler
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if handler == nil {
		return Result{ExitCode: 127}, &ExitError{Cmd: cmd, Result: Result{ExitCode: 127}}
	}
	return handler(cmd)
}

// ran reports whether a command was executed.
func (f *fakeExecutor) ran(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cmd := range f.calls {
		if cmd.Name == name {
			return true
		}
	}
	return false
}

// okResult is a successful command.
func okResult(stdout string) (Result, error) { return Result{Stdout: stdout}, nil }

// failedResult is a command that ran and exited non-zero.
func failedResult(cmd Cmd, code int, stderr string) (Result, error) {
	res := Result{ExitCode: code, Stderr: stderr}
	return res, &ExitError{Cmd: cmd, Result: res}
}

var _ = testing.Short // keep the import set stable while the suite grows
