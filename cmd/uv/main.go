// Command uv is the installer, agent and CLI for one Termit node.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/version"
)

// Exit statuses. A caller script can rely on these.
const (
	// exitOK means the command did what was asked.
	exitOK = 0
	// exitFailure means it did not, and the message says why.
	exitFailure = 1
	// exitUsage means the command line was wrong.
	exitUsage = 2
	// exitNotImplemented means the command exists in the interface but not in this build.
	exitNotImplemented = 3
)

// logLevel is shared by every command. The default keeps stdout readable; -v raises it.
var logLevel = new(slog.LevelVar)

// verboseFlag raises the log level as soon as it is parsed, so every command gets it without
// repeating itself.
type verboseFlag struct{}

func (verboseFlag) String() string   { return "false" }
func (verboseFlag) IsBoolFlag() bool { return true }
func (verboseFlag) Set(string) error {
	logLevel.Set(slog.LevelInfo)
	return nil
}

// pendingWork is what the installer does not do yet. It shrinks as M1.3–M1.5 land, and a successful
// init says so instead of pretending the node is complete.
var pendingWork = []string{"decoy site", "certificate", "proxy and nginx configuration", "systemd units"}

func main() {
	logLevel.Set(slog.LevelWarn)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
	app := &application{
		stdout: os.Stdout,
		stderr: os.Stderr,
		now:    time.Now,
		exec:   install.OSExecutor{},
		http:   &http.Client{Timeout: 15 * time.Minute},
	}
	os.Exit(app.run(os.Args[1:]))
}

// application is the CLI with its dependencies made explicit, so a test can run commands without a
// host and without a network.
type application struct {
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
	exec   install.Executor
	http   *http.Client
}

func (a *application) run(args []string) int {
	if len(args) == 0 {
		a.usage(a.stderr)
		return exitUsage
	}
	command, rest := args[0], args[1:]
	switch command {
	case "init":
		return a.runInit(rest)
	case "doctor":
		return a.runDoctor(rest)
	case "status":
		return a.runStatus(rest)
	case "uninstall":
		return a.runUninstall(rest)
	case "version":
		fmt.Fprintln(a.stdout, version.String("termit-uv"))
		return exitOK
	case "help", "-h", "--help":
		a.usage(a.stdout)
		return exitOK
	case "sync", "unblock", "migrate", "renew-site", "renew-cert", "warp":
		return a.notImplemented(command)
	default:
		fmt.Fprintf(a.stderr, "uv: unknown command %q\n\n", command)
		a.usage(a.stderr)
		return exitUsage
	}
}

func (a *application) notImplemented(command string) int {
	fmt.Fprintf(a.stderr, "uv %s: not implemented in this build; it arrives with the proxy lifecycle work\n", command)
	return exitNotImplemented
}

func (a *application) usage(w io.Writer) {
	fmt.Fprint(w, `termit-uv — Termit node installer, agent and CLI

Usage:
  uv <command> [flags]

Commands:
  init         install and configure this node (idempotent)
  doctor       run pre-flight and health diagnostics
  status       show node state, identity and last install
  uninstall    remove Termit from this node
  version      print build and protocol version
  help         show this help

Planned, not in this build:
  sync, unblock, migrate, renew-site, renew-cert, warp

Exit status: 0 success, 1 failure, 2 usage error, 3 command not implemented.

Configuration lives in /etc/termit/uv/config.yaml and state in /var/lib/termit/uv/;
both paths are overridable in the configuration file, and the configuration path with
--config or TERMIT_UV_CONFIG. See docs/DESCRIPTION.md §5 for service ownership.
`)
}

// newFlagSet builds a flag set that prints the command's own usage on -h and on a bad flag.
func (a *application) newFlagSet(name, summary string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Var(verboseFlag{}, "v", "log every step at info level")
	fs.Var(verboseFlag{}, "verbose", "log every step at info level")
	fs.Usage = func() {
		fmt.Fprintf(a.stderr, "Usage: uv %s [flags]\n\n%s\n\nFlags:\n", name, summary)
		fs.PrintDefaults()
	}
	return fs
}

// flagError maps a flag parse result to an exit status.
func (a *application) flagError(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

// configPathDefault is the path the agent uses when no flag overrides it.
func configPathDefault() string {
	if fromEnv := os.Getenv(config.PathEnv); fromEnv != "" {
		return fromEnv
	}
	return config.DefaultPath
}

// splitList parses a comma-separated flag value, ignoring blanks.
func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// loadConfig reads a configuration file that must exist and be valid.
func (a *application) loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// installOptions wires an Installer for this host.
func (a *application) installOptions(cfg *config.Config) install.Options {
	return install.Options{
		Config:  cfg,
		Exec:    a.exec,
		HTTP:    a.http,
		Logger:  slog.Default(),
		Now:     a.now,
		Version: version.Version,
	}
}

// printSteps renders a step table.
func (a *application) printSteps(report *install.Report) {
	width := 0
	for _, step := range report.Steps {
		if len(step.Name) > width {
			width = len(step.Name)
		}
	}
	for _, step := range report.Steps {
		detail := step.Detail
		if step.Err != nil {
			detail = step.Err.Error()
		}
		line := fmt.Sprintf("  %-*s  %-7s", width, step.Name, step.Status)
		if detail != "" {
			line += "  " + detail
		}
		fmt.Fprintln(a.stdout, line)
	}
}

// printChecks renders a preflight table.
func (a *application) printChecks(checks []install.Check) {
	width := 0
	for _, check := range checks {
		if len(check.Name) > width {
			width = len(check.Name)
		}
	}
	for _, check := range checks {
		fmt.Fprintf(a.stdout, "  %-*s  %-4s  %s\n", width, check.Name, check.Status, check.Detail)
		if check.Hint != "" && check.Status != install.StatusOK {
			fmt.Fprintf(a.stdout, "  %-*s  %-4s  %s\n", width, "", "", check.Hint)
		}
	}
}

// summarizeChecks returns a one-line tally.
func summarizeChecks(checks []install.Check) string {
	counts := map[install.CheckStatus]int{}
	for _, check := range checks {
		counts[check.Status]++
	}
	parts := make([]string, 0, 4)
	for _, status := range []install.CheckStatus{install.StatusOK, install.StatusWarn, install.StatusFail, install.StatusSkip} {
		if counts[status] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[status], status))
		}
	}
	return strings.Join(parts, ", ")
}

// summarizeReport returns a one-line tally of a step run.
func summarizeReport(report *install.Report) string {
	counts := report.Counts()
	parts := make([]string, 0, 4)
	for _, status := range []string{"ok", "present", "planned", "failed"} {
		if counts[status] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[status], status))
		}
	}
	return strings.Join(parts, ", ")
}

// context returns the command context: a signal-aware one is not needed yet, but every call site
// already takes a context so adding it later is local.
func (a *application) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Minute)
}
