// Package install turns a fresh Debian host into a Termit node, one idempotent step at a time.
//
// Every step answers two questions: "is my effect already present?" (Check) and "produce it" (Run).
// Re-running `termit-uv init` therefore converges instead of regenerating: a step that finds its effect in
// place does nothing, and a step that was interrupted is simply run again. The journal records what
// happened, which step failed, and where a previous run stopped.
package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Cmd is one external command invocation.
type Cmd struct {
	Name  string
	Args  []string
	Env   []string // appended to the inherited environment
	Dir   string
	Stdin string
}

// String renders the command for logs and error messages.
func (c Cmd) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)
	parts = append(parts, c.Args...)
	return strings.Join(parts, " ")
}

// Result is a completed command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// outputTail is the part of a command's output kept for an error message. Full output can be
// megabytes; the last lines are what explains a failure.
func (r Result) outputTail() string {
	text := strings.TrimSpace(r.Stderr)
	if text == "" {
		text = strings.TrimSpace(r.Stdout)
	}
	if text == "" {
		return ""
	}
	const limit = 2000
	if len(text) > limit {
		text = "…" + text[len(text)-limit:]
	}
	return text
}

// ExitError reports a command that ran and exited non-zero. It is distinct from a command that
// could not be started at all.
type ExitError struct {
	Cmd    Cmd
	Result Result
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s exited %d", e.Cmd.String(), e.Result.ExitCode)
	if tail := e.Result.outputTail(); tail != "" {
		msg += ": " + tail
	}
	return msg
}

// Executor runs external commands. It exists so install steps can be tested without root, apt or a
// systemd.
type Executor interface {
	Run(ctx context.Context, cmd Cmd) (Result, error)
}

// OSExecutor runs commands on this host.
type OSExecutor struct{}

// Run implements Executor.
func (OSExecutor) Run(ctx context.Context, cmd Cmd) (Result, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...) // #nosec G204 -- every caller passes fixed arguments.
	c.Env = append(os.Environ(), cmd.Env...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, &ExitError{Cmd: cmd, Result: res}
	}
	// The command did not start: missing binary, permission denied, cancelled context.
	return res, fmt.Errorf("%s: %w", cmd.String(), err)
}

// exitCode reports the exit status of err, or -1 when the command never ran.
func exitCode(err error) int {
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Result.ExitCode
	}
	return -1
}

// notFound reports whether err means "the binary does not exist".
func notFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}
