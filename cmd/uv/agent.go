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
		// The server renders a link per kind, so it has to know which kinds this node actually serves.
		Transports: proto.NodeTransports(cfg.Hysteria != nil),
		Now:        a.now,
		Check: func(ctx context.Context, st *state.State) state.SelfCheckReport {
			return agent.RunSelfCheck(ctx, agent.SelfCheckOptions{
				Config: cfg,
				State:  st,
				Now:    a.now,
				Active: activeUnit,
				Dial:   dialLoopback,
				Warp:   a.warpProbe(cfg),
			})
		},
		Apply: func(ctx context.Context, st *state.State, answer proto.SyncResponse) (agent.Result, error) {
			applier, err := a.applier(cfg, st)
			if err != nil {
				return agent.Result{}, err
			}
			result, err := applier.Apply(ctx, answer, st.LocalUsers)
			if err != nil {
				return result, err
			}
			// The QUIC transport has no API: its users are one file, and a changed file means one
			// restart of that unit alone. It runs after the core, because its traffic enters the core.
			if cfg.Hysteria != nil {
				uuids := make([]string, 0, len(answer.Peers)+len(st.LocalUsers)+1)
				if answer.Self.ProbeUUID != "" {
					uuids = append(uuids, answer.Self.ProbeUUID)
				}
				for _, user := range st.LocalUsers {
					uuids = append(uuids, user.ID)
				}
				for _, peer := range agent.PeersFrom(answer) {
					uuids = append(uuids, peer.UUID)
				}
				transport := agent.Transport{
					ConfigPath: cfg.Paths.HysteriaConfig,
					Unit:       service.HysteriaUnitName,
					Owner:      "root:" + service.HysteriaUser,
					Runner:     execRunner{},
				}
				if _, err := transport.Apply(ctx, install.TransportSpec(cfg, st.Identity.SelfUUID, uuids)); err != nil {
					return result, err
				}
			}
			return result, nil
		},
		// The page lists the mesh, so a cycle that reconciled it must not leave yesterday's peers on
		// the page. A node whose owner never asked for the page has nothing to refresh.
		AfterSync: func(context.Context) error {
			return a.refreshOptions(cfg)
		},
	}
}

// warpProbe performs the end-to-end WARP check: a fetch that reports what Cloudflare sees. The two
// transports answer it differently — the sidecar is asked, the in-core tunnel is proved by fetching
// through it — and the self-check must not judge one of them by the other's rules.
func (a *application) warpProbe(cfg *config.Config) func(ctx context.Context) (warp.Status, error) {
	if cfg.Warp.UsesWireGuard() {
		return func(ctx context.Context) (warp.Status, error) {
			status, err := tunnelExit(ctx, cfg)
			if err != nil {
				return warp.Status{}, err
			}
			// The tunnel speaks its own words ("on"); the self-check judges the shared vocabulary the
			// sidecar also answers in, so one verdict covers both transports.
			if status.State == "on" {
				status.State = "connected"
			}
			return status, nil
		}
	}
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

// applier builds the core reconciler for one state snapshot. The WARP half comes from the install
// package so a sync writes the same document `init` does: a sync that built its own spec used to
// replace a wireguard tunnel with the proxy sidecar, and the node then exited directly.
func (a *application) applier(cfg *config.Config, st *state.State) (agent.Applier, error) {
	path := "/" + st.Identity.XHTTPPath + "/"
	binary := cfg.Paths.XrayBinary()
	warpSpec, err := install.WarpSpecFor(cfg)
	if err != nil {
		return agent.Applier{}, err
	}
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
		Warp:            warpSpec,
		BlockBitTorrent: cfg.Security.BitTorrentBlock,
		Hysteria:        install.HysteriaHopFor(cfg),
		Canary: agent.Canary{
			Binary:    binary,
			ProxyAddr: xray.DefaultListen + ":" + strconv.Itoa(xray.DefaultPort),
			Target:    "127.0.0.1:80",
			Host:      cfg.Domain,
			Path:      path,
			UUID:      st.Identity.SelfUUID,
			WorkDir:   cfg.Paths.StateDir,
		},
	}, nil
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
