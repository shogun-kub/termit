package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/guard"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// guardSummary is the group's help page.
const guardSummary = `The ban engine: it reads the kernel's scan set and the web server's log, bans sources with the
ladder and keeps the decisions in bans.json. It only ever adds or removes elements inside the table
the operator applied; it never applies, changes or removes the table itself.

  termit-uv guard            the same as ` + "`guard status`" + `
  termit-uv guard run        the loop the unit runs; --once runs one pass and exits
  termit-uv guard status     whether the loop is running and what it has banned`

func (a *application) runGuard(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return a.runGuardRun(args[1:])
		case "status":
			return a.runGuardStatus(args[1:])
		}
	}
	return a.runGuardStatus(args)
}

// runGuardRun is the unit's command. It refuses to run when the configuration says the guard is off:
// a loop that bans without being asked is exactly what the design forbids.
func (a *application) runGuardRun(args []string) int {
	flags := a.newFlagSet("guard run", "Runs the ban engine until it is stopped. It reads security.guard from the configuration and\n"+
		"does nothing when security.guard.enabled is false.")
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	once := flags.Bool("once", false, "run one pass and exit; it is what a drill or a test wants")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv guard run: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv guard: %v\n", err)
		return exitFailure
	}
	if !cfg.Security.Guard.Enabled {
		// Exit 0, not an error: the unit may still be installed while the operator has turned the guard
		// off, and a non-zero exit would restart it every thirty seconds forever.
		fmt.Fprintln(a.stdout, "guard: security.guard.enabled is false, so the ban engine stays off")
		return exitOK
	}
	engine, err := guard.New(a.guardSettings(cfg), a.nftApplier())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv guard: %v\n", err)
		return exitFailure
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *once {
		if err := engine.Cycle(ctx); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv guard: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(a.stdout, "guard: one pass done")
		return exitOK
	}
	slog.Default().Info("guard started", "table", firewall.NodeTable, "bans", cfg.Paths.BansPath())
	if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(a.stderr, "termit-uv guard: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// runGuardStatus reports whether the loop is running and what it decided.
func (a *application) runGuardStatus(args []string) int {
	flags := a.newFlagSet("guard [status]", guardSummary)
	configFlag := flags.String("config", configPathDefault(), "path to config.yaml")
	jsonOut := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv guard: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	cfg, err := a.loadConfig(*configFlag)
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv guard: %v\n", err)
		return exitFailure
	}
	bans, err := firewall.LoadBans(cfg.Paths.BansPath())
	if err != nil {
		fmt.Fprintf(a.stderr, "termit-uv guard: %v\n", err)
		return exitFailure
	}
	now := a.now()
	active := bans.Active(now)
	offenders := bans.Offenders(now)
	state := "off"
	if cfg.Security.Guard.Enabled {
		state = "on"
	}
	if *jsonOut {
		report := map[string]any{
			"enabled":    cfg.Security.Guard.Enabled,
			"table":      firewall.NodeTable,
			"bans":       len(active),
			"offenders":  len(offenders),
			"access_log": cfg.Paths.AccessLog,
		}
		if err := writeJSON(a.stdout, report); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv guard: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	fmt.Fprintf(a.stdout, "guard:      %s\n", state)
	if !cfg.Security.Guard.Enabled {
		return exitOK
	}
	fmt.Fprintf(a.stdout, "table:      inet %s (the firewall command owns it)\n", firewall.NodeTable)
	fmt.Fprintf(a.stdout, "log:        %s\n", cfg.Paths.AccessLog)
	fmt.Fprintf(a.stdout, "bans:       %d active, %d offenders\n", len(active), len(offenders))
	return exitOK
}

// guardSettings wires the loop: the table, the record, the log and the two ladders it applies.
func (a *application) guardSettings(cfg *config.Config) guard.Settings {
	mn := &addressCache{now: a.now, ttl: 10 * time.Minute}
	ssh, _ := firewall.Prefixes(cfg.Security.Firewall.SSHFrom)
	return guard.Settings{
		Table:     firewall.NodeTable,
		BansPath:  cfg.Paths.BansPath(),
		AccessLog: cfg.Paths.AccessLog,
		Ladder:    cfg.Security.Ladder(),
		Failures: guard.Failures{
			Attempts: cfg.Security.Guard.Path404.Attempts,
			Window:   cfg.Security.Guard.Path404.Window.Std(),
		},
		Never: func(ctx context.Context) []netip.Prefix {
			extra := append([]netip.Prefix(nil), ssh...)
			return append(extra, mn.get(ctx, cfg.MN.URL)...)
		},
		Now: a.now,
		Log: slog.Default(),
	}
}

// addressCache resolves the management server's addresses for the never-ban set. It keeps an answer for
// a while: a lookup every cycle would hammer the resolver for something that changes rarely, and a
// failed lookup keeps the last good answer rather than emptying the protected set.
type addressCache struct {
	mu    sync.Mutex
	now   func() time.Time
	ttl   time.Duration
	at    time.Time
	addrs []netip.Prefix
}

// get returns the addresses behind rawURL, resolving at most once per ttl.
func (c *addressCache) get(ctx context.Context, rawURL string) []netip.Prefix {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.at.IsZero() && now.Sub(c.at) < c.ttl {
		return c.addrs
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return c.addrs
	}
	host := parsed.Hostname()
	if addr, err := netip.ParseAddr(host); err == nil {
		c.addrs, c.at = []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())}, now
		return c.addrs
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		// Keep the previous answer: an empty protected set is worse than a stale one.
		return c.addrs
	}
	resolved := make([]netip.Prefix, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		resolved = append(resolved, netip.PrefixFrom(address, address.BitLen()))
	}
	c.addrs, c.at = resolved, now
	return c.addrs
}

// writeJSON is the indented JSON report several commands print.
func writeJSON(w io.Writer, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(body))
	return err
}
