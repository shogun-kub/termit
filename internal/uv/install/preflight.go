package install

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/uv/config"
)

// CheckStatus is the outcome of one preflight check.
type CheckStatus string

const (
	// StatusOK means the check passed.
	StatusOK CheckStatus = "ok"
	// StatusWarn means something is off but the install can proceed.
	StatusWarn CheckStatus = "warn"
	// StatusFail means the install must not proceed.
	StatusFail CheckStatus = "fail"
	// StatusSkip means the check could not be evaluated on this host.
	StatusSkip CheckStatus = "skip"
)

// Check is one diagnostic statement about the host.
type Check struct {
	Name   string
	Status CheckStatus
	Detail string
	// Hint is what the operator should do about a warning or failure.
	Hint string
}

// PreflightReport is the result of a preflight run.
type PreflightReport struct {
	Checks []Check
}

// OK reports whether the install may proceed.
func (r PreflightReport) OK() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return false
		}
	}
	return true
}

// Failures returns the failed checks.
func (r PreflightReport) Failures() []Check { return r.filter(StatusFail) }

// Warnings returns the checks that passed with a warning.
func (r PreflightReport) Warnings() []Check { return r.filter(StatusWarn) }

func (r PreflightReport) filter(status CheckStatus) []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Status == status {
			out = append(out, c)
		}
	}
	return out
}

// PreflightOptions are the preflight checks' inputs and injection points. The zero value is valid:
// every probe falls back to the real host.
type PreflightOptions struct {
	// Config may be nil: checks that need a domain or a path then report skip.
	Config *config.Config
	Exec   Executor
	HTTP   *http.Client
	Arch   string
	Now    func() time.Time

	EUID       func() int
	ReadFile   func(string) ([]byte, error)
	Stat       func(string) (os.FileInfo, error)
	DiskFree   func(string) (uint64, error)
	LookupHost func(context.Context, string) ([]string, error)
}

// RunPreflight evaluates every check and returns them in a stable order.
func RunPreflight(ctx context.Context, opts PreflightOptions) PreflightReport {
	p := &preflight{opts: opts}
	p.defaults()

	checks := make([]Check, 0, 12)
	for _, fn := range []func(context.Context) Check{
		p.checkRoot,
		p.checkOS,
		p.checkSystemd,
		p.checkArch,
		p.checkClock,
		p.checkPorts,
		p.checkDNS,
		p.checkACME,
		p.checkTun,
		p.checkDisk,
		p.checkMemory,
		p.checkApt,
	} {
		checks = append(checks, fn(ctx))
	}
	return PreflightReport{Checks: checks}
}

type preflight struct {
	opts PreflightOptions
}

func (p *preflight) defaults() {
	if p.opts.Arch == "" {
		p.opts.Arch = runtime.GOARCH
	}
	if p.opts.Now == nil {
		p.opts.Now = time.Now
	}
	if p.opts.EUID == nil {
		p.opts.EUID = defaultEUID
	}
	if p.opts.ReadFile == nil {
		p.opts.ReadFile = os.ReadFile
	}
	if p.opts.Stat == nil {
		p.opts.Stat = os.Stat
	}
	if p.opts.DiskFree == nil {
		p.opts.DiskFree = diskFree
	}
	if p.opts.LookupHost == nil {
		p.opts.LookupHost = net.DefaultResolver.LookupHost
	}
	if p.opts.Exec == nil {
		p.opts.Exec = OSExecutor{}
	}
	if p.opts.HTTP == nil {
		p.opts.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
}

func (p *preflight) checkRoot(context.Context) Check {
	if p.opts.EUID() == 0 {
		return Check{Name: "root", Status: StatusOK, Detail: "running as root"}
	}
	return Check{
		Name: "root", Status: StatusFail, Detail: "not running as root",
		Hint: "run as root (sudo termit-uv init …); the installer writes to /etc and /var/lib",
	}
}

func (p *preflight) checkOS(context.Context) Check {
	data, err := p.opts.ReadFile("/etc/os-release")
	if err != nil {
		return Check{
			Name: "os", Status: StatusFail, Detail: "cannot read /etc/os-release",
			Hint: "only Debian 13 (trixie) is supported",
		}
	}
	release := parseOSRelease(data)
	id := strings.ToLower(release["ID"])
	version := release["VERSION_ID"]
	if id != "debian" {
		return Check{
			Name: "os", Status: StatusFail,
			Detail: fmt.Sprintf("distribution %q is not supported", firstNonEmpty(id, "unknown")),
			Hint:   "only Debian 13 (trixie) is supported, on amd64 or arm64",
		}
	}
	major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	switch {
	case err != nil:
		return Check{Name: "os", Status: StatusWarn, Detail: "cannot parse Debian version " + quote(version),
			Hint: "expected Debian 13 (trixie)"}
	case major >= 13:
		return Check{Name: "os", Status: StatusOK, Detail: "Debian " + version}
	case major == 12:
		return Check{Name: "os", Status: StatusWarn, Detail: "Debian " + version + " is older than the supported release",
			Hint: "Debian 13 (trixie) is the target; 12 may work but is not tested"}
	default:
		return Check{Name: "os", Status: StatusFail, Detail: "Debian " + version + " is too old",
			Hint: "upgrade to Debian 13 (trixie)"}
	}
}

func (p *preflight) checkSystemd(context.Context) Check {
	if info, err := p.opts.Stat("/run/systemd/system"); err == nil && info.IsDir() {
		return Check{Name: "systemd", Status: StatusOK, Detail: "systemd is running"}
	}
	return Check{
		Name: "systemd", Status: StatusFail, Detail: "systemd is not running",
		Hint: "Termit supervises Xray and the agent with systemd units",
	}
}

func (p *preflight) checkArch(context.Context) Check {
	switch p.opts.Arch {
	case "amd64", "arm64":
		return Check{Name: "arch", Status: StatusOK, Detail: p.opts.Arch}
	default:
		return Check{Name: "arch", Status: StatusFail, Detail: "unsupported architecture " + p.opts.Arch,
			Hint: "builds exist for amd64 and arm64"}
	}
}

func (p *preflight) checkClock(ctx context.Context) Check {
	res, err := p.opts.Exec.Run(ctx, Cmd{Name: "timedatectl", Args: []string{"show", "--property=NTPSynchronized", "--value"}})
	if err != nil {
		if notFound(err) {
			return Check{Name: "clock", Status: StatusSkip, Detail: "timedatectl is not available"}
		}
		return Check{Name: "clock", Status: StatusWarn, Detail: "cannot query clock state: " + err.Error(),
			Hint: "ACME rejects requests from a host whose clock is far off"}
	}
	if strings.TrimSpace(res.Stdout) == "yes" {
		return Check{Name: "clock", Status: StatusOK, Detail: "clock is synchronised"}
	}
	return Check{Name: "clock", Status: StatusWarn, Detail: "clock is not synchronised",
		Hint: "enable NTP (timedatectl set-ntp true); ACME fails when the clock drifts"}
}

// listeners is one row of `ss -ltnp`.
type listener struct {
	Port    int
	Address string
	Process string
}

var ssProcessPattern = regexp.MustCompile(`users:\(\("([^"]+)"`)

func (p *preflight) checkPorts(ctx context.Context) Check {
	res, err := p.opts.Exec.Run(ctx, Cmd{Name: "ss", Args: []string{"-H", "-ltnp"}})
	if err != nil {
		if notFound(err) {
			return Check{Name: "ports", Status: StatusSkip, Detail: "ss is not available"}
		}
		return Check{Name: "ports", Status: StatusWarn, Detail: "cannot list listeners: " + err.Error()}
	}
	listeners := parseListeners(res.Stdout)

	termitVhost := false
	if p.opts.Config != nil {
		if _, err := p.opts.Stat(p.opts.Config.Paths.NginxSite); err == nil {
			termitVhost = true
		}
	}

	var busy, ours []string
	for _, port := range []int{80, 443} {
		// One port usually has two listeners, one for each address family. The report names the port
		// once: `:80 and :80 and :443 and :443` reads like a fault.
		served := false
		for _, l := range listeners {
			if l.Port != port {
				continue
			}
			switch {
			case l.Process == "nginx" && termitVhost:
				served = true
			case l.Process == "nginx":
				return Check{
					Name: "ports", Status: StatusWarn,
					Detail: fmt.Sprintf("nginx already listens on :%d", port),
					Hint:   "Termit adds its own vhost to the existing nginx; check that nothing else depends on that port",
				}
			case l.Process == "":
				return Check{
					Name: "ports", Status: StatusWarn,
					Detail: fmt.Sprintf(":%d is in use by a process this user cannot inspect", port),
					Hint:   "run termit-uv doctor as root to see which process holds the port",
				}
			default:
				busy = append(busy, fmt.Sprintf(":%d by %s", port, l.Process))
			}
		}
		if served {
			ours = append(ours, fmt.Sprintf(":%d", port))
		}
	}
	switch {
	case len(busy) > 0:
		return Check{
			Name: "ports", Status: StatusFail, Detail: "ports already in use: " + strings.Join(busy, ", "),
			Hint: "stop the service that owns :80/:443; Termit needs them for ACME and TLS",
		}
	case len(ours) > 0:
		return Check{Name: "ports", Status: StatusOK, Detail: "nginx serves " + strings.Join(ours, " and ") + " (Termit vhost)"}
	default:
		return Check{Name: "ports", Status: StatusOK, Detail: ":80 and :443 are free"}
	}
}

func (p *preflight) checkDNS(ctx context.Context) Check {
	if p.opts.Config == nil || p.opts.Config.Domain == "" {
		return Check{Name: "dns", Status: StatusSkip, Detail: "no domain configured"}
	}
	domain := p.opts.Config.Domain
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	addrs, err := p.opts.LookupHost(ctx, domain)
	if err != nil || len(addrs) == 0 {
		return Check{
			Name: "dns", Status: StatusWarn, Detail: fmt.Sprintf("%s does not resolve here", domain),
			Hint: "the ACME CA resolves the name from outside this host; verify the A record independently",
		}
	}
	state := StatusWarn
	hint := "this host cannot see its own public address; verify that the A record points at this server from outside"
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && !ip.IsLoopback() && isLocalAddress(ip) {
			state, hint = StatusOK, ""
			break
		}
	}
	return Check{Name: "dns", Status: state, Detail: domain + " → " + strings.Join(addrs, ", "), Hint: hint}
}

func (p *preflight) checkACME(ctx context.Context) Check {
	if p.opts.Config == nil {
		return Check{Name: "acme", Status: StatusSkip, Detail: "no ACME directory configured"}
	}
	directory := p.opts.Config.ACME.Directory
	client := *p.opts.HTTP
	if p.opts.Config.ACME.SkipTLSVerify {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// #nosec G402 -- lab-only escape hatch from Config.ACME.SkipTLSVerify.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client.Transport = transport
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, directory, nil)
	if err != nil {
		return Check{Name: "acme", Status: StatusFail, Detail: "bad directory URL: " + err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Check{
			Name: "acme", Status: StatusWarn, Detail: errorText(err),
			Hint: "certificate issuance runs later; without the CA it cannot succeed",
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Check{
			Name: "acme", Status: StatusWarn, Detail: fmt.Sprintf("%s answered %s", directory, resp.Status),
			Hint: "check acme.directory in the configuration",
		}
	}
	return Check{Name: "acme", Status: StatusOK, Detail: directory}
}

func (p *preflight) checkTun(context.Context) Check {
	_, err := p.opts.Stat("/dev/net/tun")
	warpWanted := p.opts.Config != nil && p.opts.Config.Warp.UsesWarp()
	switch {
	case err == nil:
		return Check{Name: "tun", Status: StatusOK, Detail: "/dev/net/tun is available"}
	case warpWanted:
		// A host without TUN is still a node. WARP is on by default, so this is the common case for
		// a container; the install step reports the refusal, the route fails open to direct, and the
		// operator sees the warning here rather than a failed install.
		return Check{
			Name: "tun", Status: StatusWarn, Detail: "/dev/net/tun is missing, so WARP cannot run on this host",
			Hint: "traffic exits directly and termit-uv status says so; set warp.mode: off to silence this warning",
		}
	default:
		return Check{Name: "tun", Status: StatusSkip,
			Detail: "/dev/net/tun is missing (WARP is unavailable here and is off)"}
	}
}

func (p *preflight) checkDisk(context.Context) Check {
	if p.opts.Config == nil {
		return Check{Name: "disk", Status: StatusSkip, Detail: "no state directory configured"}
	}
	dir := p.opts.Config.Paths.StateDir
	free, err := p.opts.DiskFree(dir)
	if err != nil {
		// Before the first install the state directory does not exist yet; the filesystem that will
		// hold it is the nearest existing ancestor.
		for parent := path.Dir(filepath.ToSlash(dir)); parent != "/" && parent != "."; parent = path.Dir(parent) {
			if free, err = p.opts.DiskFree(parent); err == nil {
				dir = parent
				break
			}
		}
	}
	if err != nil {
		return Check{Name: "disk", Status: StatusSkip, Detail: "cannot measure free space: " + err.Error()}
	}
	detail := fmt.Sprintf("%s free on the filesystem holding %s", humanBytes(free), dir)
	switch {
	case free < 256<<20:
		return Check{Name: "disk", Status: StatusFail, Detail: detail,
			Hint: "Termit needs room for the Xray binary, certificates, the decoy site and logs"}
	case free < 1<<30:
		return Check{Name: "disk", Status: StatusWarn, Detail: detail,
			Hint: "less than 1 GiB free; log rotation is mandatory at this size"}
	default:
		return Check{Name: "disk", Status: StatusOK, Detail: detail}
	}
}

func (p *preflight) checkMemory(context.Context) Check {
	data, err := p.opts.ReadFile("/proc/meminfo")
	if err != nil {
		return Check{Name: "memory", Status: StatusSkip, Detail: "cannot read /proc/meminfo"}
	}
	total, ok := parseMemTotal(data)
	if !ok {
		return Check{Name: "memory", Status: StatusSkip, Detail: "cannot parse /proc/meminfo"}
	}
	detail := humanBytes(total) + " RAM"
	switch {
	case total < 384<<20:
		return Check{Name: "memory", Status: StatusFail, Detail: detail,
			Hint: "at least 512 MiB is needed for nginx, Xray and the agent"}
	case total < 768<<20:
		return Check{Name: "memory", Status: StatusWarn, Detail: detail,
			Hint: "the steady-state budget is 150–250 MiB; keep swap enabled at this size"}
	default:
		return Check{Name: "memory", Status: StatusOK, Detail: detail}
	}
}

func (p *preflight) checkApt(ctx context.Context) Check {
	res, err := p.opts.Exec.Run(ctx, Cmd{Name: "apt-get", Args: []string{"--version"}})
	if err != nil {
		return Check{
			Name: "apt", Status: StatusFail, Detail: "apt-get is not usable",
			Hint: "packages are installed with apt; this must be a Debian host",
		}
	}
	line, _, _ := strings.Cut(strings.TrimSpace(res.Stdout), "\n")
	return Check{Name: "apt", Status: StatusOK, Detail: firstNonEmpty(line, "apt-get is available")}
}

// parseOSRelease parses the shell-style KEY=VALUE file, ignoring comments and stripping quotes.
func parseOSRelease(data []byte) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		values[strings.TrimSpace(key)] = value
	}
	return values
}

// parseListeners extracts the local port and owning process from `ss -ltnp` output.
func parseListeners(output string) []listener {
	var out []listener
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		address := fields[3]
		idx := strings.LastIndex(address, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(address[idx+1:])
		if err != nil {
			continue
		}
		process := ""
		if m := ssProcessPattern.FindStringSubmatch(line); m != nil {
			process = m[1]
		}
		out = append(out, listener{Port: port, Address: address, Process: process})
	}
	return out
}

// parseMemTotal returns MemTotal in bytes.
func parseMemTotal(data []byte) (uint64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// isLocalAddress reports whether ip is configured on one of this host's interfaces.
func isLocalAddress(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if network, ok := addr.(*net.IPNet); ok && network.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func humanBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func quote(s string) string { return strconv.Quote(s) }

// errorText renders an error for a check detail without repeating the URL it refers to.
func errorText(err error) string {
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timed out reaching the ACME directory"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out reaching the ACME directory"
	default:
		return strings.TrimSpace(compact(err.Error()))
	}
}

// compact collapses whitespace so multi-line transport errors stay on one line.
func compact(s string) string {
	var b bytes.Buffer
	space := false
	for _, r := range s {
		if r == '\n' || r == '\t' || r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
