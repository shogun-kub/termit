// Package hardencmd is the command layer both binaries share for the security block: the host table,
// the ban list, the guard and the blocklist refresh. Everything a command needs to know about the
// component it hardens is a Target; everything it needs to know about the host is an Env. That way
// `termit-uv firewall on` and `termit-mn firewall on` are the same code with a different table name,
// which is the whole point: a difference in behaviour between them would be a bug nobody would notice.
package hardencmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/harden"
	"github.com/shogun-kub/termit/internal/uv/install"
)

// Exit codes, shared by both binaries so a script can treat them the same.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Paths are the state files the commands read and write.
type Paths struct {
	Firewall  string
	Bans      string
	Blocklist string
}

// Units are the systemd units a component has for this block. A component that is deployed from a
// package leaves its units to the operator; the names are still needed to start and stop them.
type Units struct {
	Guard          string
	Blocklist      string
	BlocklistTimer string
	Rollback       string
	RollbackTimer  string
}

// Target is what a command needs to know about the component it hardens.
type Target interface {
	// Name is the program name in messages: "termit-uv" or "termit-mn".
	Name() string
	// Table is the component's own nftables table and the ports it serves.
	Table() (name string, ports []int)
	// Units are the ones this component starts, stops and reports.
	Units() Units
	// Open loads the configuration at path and returns everything that depends on it.
	Open(path string) (*Component, error)
}

// Component is an opened configuration and the small amount of state a command derives from it.
type Component struct {
	Security *harden.Config
	Paths    Paths
	// ConfigPath is the file that was opened; a generated unit needs it in its ExecStart.
	ConfigPath string
	// UnitDir is where this component's units are written; empty for one whose units come from a
	// package or from the operator.
	UnitDir string
	// AccessLog is the web server's log for the 4xx signal; empty means the component keeps none and
	// the signal is off.
	AccessLog string
	// Never returns the networks a ban may not touch beyond the built-in ranges: the operator's
	// ssh_from and, on a node, the management server's addresses. A failed lookup returns the last
	// good answer, because an empty protected set is worse than a stale one.
	Never func(ctx context.Context) []netip.Prefix
}

// Rollback is the safety net under `firewall on`: something has to remove the table if the operator
// cannot confirm it. The node writes its own units; the server's come from its package, so it can only
// start and stop what is installed.
type Rollback interface {
	Arm(ctx context.Context) error
	Disarm(ctx context.Context) error
}

// Env is the host, the operator's terminal and the seams a test replaces.
type Env struct {
	Target Target
	// ConfigPath is the configuration a command reads when --config is not given.
	ConfigPath string
	// Rollback builds the component's confirm-or-remove timer from the configuration that was opened.
	Rollback func(*Component) Rollback
	Stdout   io.Writer
	Stderr   io.Writer
	// Exec runs systemctl and nft.
	Exec install.Executor
	Now  func() time.Time
	// Root answers "may this process touch the kernel".
	Root func() bool
	// Self is the path the generated units run; only a component that writes units needs it.
	Self func() (string, error)
	// NewFlags builds a command's flag set, so each binary keeps its own prelude (a verbosity flag,
	// its usage layout) without the commands knowing about either.
	NewFlags func(name, summary string) *flag.FlagSet
}

// table is the component's table name and ports.
func (e *Env) table() (string, []int) { return e.Target.Table() }

// applier wires the firewall package to this host's executor.
func (e *Env) applier() firewall.Applier { return firewall.Applier{Runner: nftRunner{exec: e.Exec}} }

// newFlags builds a flag set, or a bare one when the binary supplied no builder.
func (e *Env) newFlags(name, summary string) *flag.FlagSet {
	if e.NewFlags != nil {
		return e.NewFlags(name, summary)
	}
	fs := flag.NewFlagSet(e.Target.Name()+" "+name, flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(e.Stderr, "Usage: %s %s [flags]\n\n%s\n\nFlags:\n", e.Target.Name(), name, summary)
		fs.PrintDefaults()
	}
	return fs
}

// flagExit maps a flag parse failure to an exit code. Asking for help is a successful request.
func flagExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	return ExitUsage
}

// nftRunner adapts the installer's executor to the firewall package's smaller interface.
type nftRunner struct{ exec install.Executor }

func (r nftRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, error) {
	res, err := r.exec.Run(ctx, install.Cmd{Name: name, Args: args, Stdin: stdin})
	return res.Stdout, err
}

// AddressCache resolves the management server's addresses for the never-ban set. It keeps an answer for
// a while: a lookup every cycle would hammer the resolver for something that changes rarely.
type AddressCache struct {
	Now func() time.Time
	TTL time.Duration

	mu    sync.Mutex
	at    time.Time
	addrs []netip.Prefix
}

// Get returns the addresses behind rawURL, resolving at most once per TTL.
func (c *AddressCache) Get(ctx context.Context, rawURL string) []netip.Prefix {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if !c.at.IsZero() && now.Sub(c.at) < ttl {
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

// never is the protected set for a component: the built-in ranges plus what it adds.
func never(ctx context.Context, c *Component) firewall.NeverBan {
	if c.Never == nil {
		return firewall.NewNeverBan()
	}
	return firewall.NewNeverBan(c.Never(ctx)...)
}

// bannable refuses an address a ban would make self-inflicted. The answer names what protects it,
// because "that address is on the never-ban list" is not something an operator can act on.
func bannable(ctx context.Context, c *Component, raw string) (string, error) {
	prefix, err := firewall.ParsePrefix(raw)
	if err != nil {
		return "", err
	}
	if never(ctx, c).Covers(prefix.Addr()) {
		return "", fmt.Errorf("%s is loopback, a private or reserved range, the management server or your own "+
			"ssh_from: banning it would cut you off, not an attacker", prefix.Addr())
	}
	return prefix.Addr().String(), nil
}

// prefixStrings renders networks the way the configuration writes them.
func prefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, prefix.String())
	}
	return out
}

// denyWords and portsWords render the two facts people ask about in a status line.
func denyWords(deny bool) string {
	if deny {
		return "default deny"
	}
	return "other inbound allowed"
}

func portsWords(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, fmt.Sprintf("%d", port))
	}
	if len(parts) == 0 {
		return "none recorded"
	}
	return strings.Join(parts, ", ")
}

// humanSince renders an age the way a person reads it.
func humanSince(now, then time.Time) string {
	delta := now.Sub(then).Round(time.Second)
	switch {
	case delta < 0:
		return "in " + (-delta).String()
	case delta < time.Minute:
		return fmt.Sprintf("%s ago", delta)
	default:
		return fmt.Sprintf("%s ago", delta.Round(time.Minute))
	}
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

// setFor picks the set an address belongs to, the way the firewall package renders them.
func setFor(ip string) string {
	prefix, err := firewall.ParsePrefix(ip)
	if err == nil && prefix.Addr().Is6() && !prefix.Addr().Is4In6() {
		return "banned6"
	}
	return "banned4"
}

// DescribeBans is the one-line summary `status` prints.
func DescribeBans(bans *firewall.Bans, now time.Time) string {
	active := bans.Active(now)
	if len(active) == 0 {
		return "no bans"
	}
	permanent := 0
	for _, ban := range active {
		if ban.Permanent() {
			permanent++
		}
	}
	out := fmt.Sprintf("%d bans", len(active))
	if permanent > 0 {
		out += fmt.Sprintf(" (%d permanent)", permanent)
	}
	return out
}
