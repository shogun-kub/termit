package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/agent"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/install"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// runAgent is the long-running half of the node: one cycle a day, at its own time, with a local
// self-check in the report and a reconciled core afterwards. systemd owns the clock across reboots;
// this loop owns the schedule between them.
func (a *application) runAgent(args []string) int {
	flags := a.newFlagSet("agent", "run the node agent: one cycle a day, with a self-check and a sync")
	configPath := flags.String("config", configPathDefault(), "path to config.yaml")
	once := flags.Bool("once", false, "run one cycle and exit; it is what a drill or a test wants")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv agent: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv agent: %v\n", err)
		return exitFailure
	}

	loop := a.newLoop(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *once {
		report, err := loop.Once(ctx)
		a.printCycle(cfg, report)
		if err != nil {
			fmt.Fprintf(a.stderr, "termit-uv agent: %v\n", err)
			return exitFailure
		}
		return exitOK
	}

	slog.Default().Info("agent started", "domain", cfg.Domain, "state", cfg.Paths.StatePath())
	if err := loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(a.stderr, "termit-uv agent: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// newLoop wires the loop the service and `termit-uv agent --once` share.
func (a *application) newLoop(cfg *config.Config) *agent.Loop {
	return &agent.Loop{
		Remote:    agent.Remote{BaseURL: cfg.MN.URL, CAFile: cfg.MN.CAFile},
		Logger:    slog.Default(),
		StatePath: cfg.Paths.StatePath(),
		CertPath:  install.LeafCertPath(cfg),
		Now:       a.now,
		Check: func(ctx context.Context, st *state.State) state.SelfCheckReport {
			return agent.RunSelfCheck(ctx, agent.SelfCheckOptions{
				Config: cfg,
				State:  st,
				Now:    a.now,
				Active: activeUnit,
				Dial:   dialLoopback,
				Warp:   a.warpProbe(),
			})
		},
		Apply: func(ctx context.Context, st *state.State, answer proto.SyncResponse) (agent.Result, error) {
			return a.applier(cfg, st).Apply(ctx, answer, st.LocalUsers)
		},
	}
}

// warpProbe performs the end-to-end WARP check: a fetch through the sidecar that reports what
// Cloudflare sees. No configuration is needed for it, which keeps the closure cheap.
func (a *application) warpProbe() func(ctx context.Context) (warp.Status, error) {
	return func(ctx context.Context) (warp.Status, error) {
		return a.warpClient().Status(ctx)
	}
}

// warpRunner adapts the shared command executor to the WARP package's command interface.
type warpRunner struct{ exec install.Executor }

func (r warpRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	result, err := r.exec.Run(ctx, install.Cmd{Name: name, Args: args})
	return result.Stdout, result.Stderr, err
}

// applier builds the core reconciler for one state snapshot.
func (a *application) applier(cfg *config.Config, st *state.State) agent.Applier {
	path := "/" + st.Identity.XHTTPPath + "/"
	binary := cfg.Paths.XrayBinary()
	return agent.Applier{
		Binary:     binary,
		ConfigPath: cfg.Paths.XrayConfig,
		Path:       path,
		SelfUUID:   st.Identity.SelfUUID,
		SelfEmail:  "owner",
		Runner:     execRunner{},
		Owner:      "root:termit",
		Unit:       service.XrayUnitName,
		Hot: agent.XrayAPI{
			Binary:  binary,
			Addr:    xray.DefaultListen + ":" + strconv.Itoa(xray.DefaultAPIPort),
			Path:    path,
			WorkDir: cfg.Paths.StateDir,
			Runner:  execRunner{},
		},
		Warp: xray.WarpSpec{
			Mode:     cfg.Warp.Mode,
			Selected: cfg.Warp.Selected,
			FailOpen: cfg.Warp.FailOpenEnabled(),
		},
		Canary: agent.Canary{
			Binary:    binary,
			ProxyAddr: xray.DefaultListen + ":" + strconv.Itoa(xray.DefaultPort),
			Target:    "127.0.0.1:80",
			Host:      cfg.Domain,
			Path:      path,
			UUID:      st.Identity.SelfUUID,
			WorkDir:   cfg.Paths.StateDir,
		},
	}
}

// printCycle renders one loop iteration for `termit-uv agent --once`.
func (a *application) printCycle(cfg *config.Config, report agent.CycleReport) {
	fmt.Fprintf(a.stdout, "cycle:    %s at %s\n", orDefault(report.Result, "unknown"), report.At.Format(time.RFC3339))
	for _, item := range report.Check.Items {
		status := "ok"
		switch {
		case item.Unknown:
			status = "unknown"
		case !item.OK:
			status = "fail"
		}
		fmt.Fprintf(a.stdout, "check:    %-11s %-7s %s\n", item.Name, status, item.Detail)
	}
	if report.Answer.Self.ID != "" {
		fmt.Fprintf(a.stdout, "self:     %s (%s) state=%s\n",
			report.Answer.Self.Domain, report.Answer.Self.ID, report.Answer.Self.State)
		fmt.Fprintf(a.stdout, "peers:    %d\n", len(report.Answer.Peers))
	}
	if report.Apply.Clients > 0 {
		fmt.Fprintf(a.stdout, "clients:  %d (%d added, %d removed, unchanged=%v hot=%v canary=%v)\n",
			report.Apply.Clients, len(report.Apply.Added), len(report.Apply.Removed),
			report.Apply.Unchanged, report.Apply.Hot, report.Apply.CanaryRan)
	}
	st, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err == nil {
		fmt.Fprintf(a.stdout, "next:     %s\n", agent.ScheduleNote(st.Schedule))
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// activeUnit asks systemd whether a unit is running. A missing systemctl is an error (unknown); a
// non-zero exit is the answer "not running", which is not an error.
func activeUnit(ctx context.Context, unit string) (bool, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false, nil
	}
	return false, err
}

// dialLoopback checks that something accepts TCP on addr, with a short deadline: a port that is not
// listening is the answer, and a hung one must not hold the whole self-check.
func dialLoopback(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}
