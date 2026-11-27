package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/decoy"
	"github.com/shogun-kub/termit/internal/firewall"
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
	"github.com/shogun-kub/termit/internal/version"
)

// DiagnoseOptions configure the health half of `termit-uv doctor`.
type DiagnoseOptions struct {
	Config *config.Config
	Exec   Executor
	Now    func() time.Time
	// WarpHost overrides the platform facts WARP is judged on. Tests set it so the check does not
	// depend on whether the machine running the suite has a tun device.
	WarpHost *warp.Host
}

// Diagnose checks the installed node, not the host: the state, the services, the certificate, the
// generated decoy and the generated proxy config. It is what `termit-uv doctor` adds once a node exists —
// preflight answers "can this host be installed", this answers "is the node working".
//
// A check that cannot be performed reports a skip, never a failure: an unavailable systemctl says
// nothing about the node.
func Diagnose(ctx context.Context, opts DiagnoseOptions) []Check {
	if opts.Exec == nil {
		opts.Exec = OSExecutor{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cfg := opts.Config

	st, err := state.LoadInstalled(cfg.Paths.StatePath())
	if err != nil {
		detail := "not installed"
		if !errors.Is(err, state.ErrNotInstalled) {
			detail = err.Error()
		}
		return []Check{{Name: "state", Status: StatusSkip, Detail: detail,
			Hint: "run `termit-uv init` to install this node"}}
	}

	checks := []Check{{
		Name:   "state",
		Status: StatusOK,
		Detail: fmt.Sprintf("%s, node identity %s", cfg.Paths.StatePath(), state.Fingerprint(st.Identity.SelfUUID)),
	}}
	if check, ok := diagnoseSupport(version.Version, st); ok {
		checks = append(checks, check)
	}
	checks = append(checks, diagnoseServices(ctx, opts.Exec))
	checks = append(checks, diagnoseCertificate(cfg, st, opts.Now()))
	checks = append(checks, diagnoseDecoy(cfg, st))
	checks = append(checks, diagnoseProxy(ctx, opts.Exec, cfg))
	// Only when it is enabled: a check for a transport nobody asked for is noise on a healthy node.
	if cfg.Hysteria != nil {
		checks = append(checks, diagnoseTransport(ctx, opts.Exec, cfg))
	}
	if check, ok := diagnoseFirewall(cfg); ok {
		checks = append(checks, check)
	}
	warpClient := warp.New(warpRunner{exec: opts.Exec})
	if opts.WarpHost != nil {
		warpClient.Host = *opts.WarpHost
	}
	checks = append(checks, diagnoseWarp(ctx, cfg, st, warpClient))
	return checks
}

// diagnoseSupport compares this build against the floor the server declared in its sync answer. It warns
// and never fails: a node below the floor still serves, and taking it out of the mesh over a build number
// would break the thing the floor exists to protect (docs/DESCRIPTION.md §8). No floor, or a build that
// cannot be ordered against one, draws no check at all.
func diagnoseSupport(build string, st *state.State) (Check, bool) {
	if st.MinAgentVersion == "" {
		return Check{}, false
	}
	below, comparable := version.Below(build, st.MinAgentVersion)
	switch {
	case !comparable:
		return Check{Name: "support", Status: StatusSkip,
			Detail: fmt.Sprintf("this build %q cannot be ordered against the server's minimum %s",
				build, st.MinAgentVersion)}, true
	case below:
		return Check{Name: "support", Status: StatusWarn,
			Detail: fmt.Sprintf("this build %s is below the server's minimum %s", build, st.MinAgentVersion),
			Hint:   "run `termit-uv upgrade`"}, true
	}
	return Check{Name: "support", Status: StatusOK, Detail: "meets the server's minimum " + st.MinAgentVersion}, true
}

func diagnoseServices(ctx context.Context, exec Executor) Check {
	var down, unknown []string
	units := []string{service.XrayUnitName, nginxUnit, service.RenewTimerName, service.AgentUnitName}
	for _, unit := range units {
		res, err := exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", unit}})
		switch {
		case err == nil:
		case exitCode(err) == 3, exitCode(err) == 1:
			down = append(down, unit)
		default:
			_ = res
			unknown = append(unknown, unit)
		}
	}
	switch {
	case len(unknown) == len(units):
		return Check{Name: "services", Status: StatusSkip, Detail: "systemctl could not be asked"}
	case len(down) > 0:
		return Check{Name: "services", Status: StatusFail, Detail: "not running: " + strings.Join(down, ", "),
			Hint: "systemctl status <unit>; `termit-uv init` starts them again"}
	}
	return Check{Name: "services", Status: StatusOK, Detail: "core, web tier, renewal timer and agent are running"}
}

func diagnoseCertificate(cfg *config.Config, st *state.State, now time.Time) Check {
	notAfter, err := acme.CertNotAfter(filepath.Join(cfg.Paths.CertDir, certFile))
	switch {
	case os.IsNotExist(err):
		return Check{Name: "certificate", Status: StatusFail, Detail: "no certificate",
			Hint: "`termit-uv renew-cert`"}
	case err != nil:
		return Check{Name: "certificate", Status: StatusFail, Detail: err.Error()}
	}
	days := int(notAfter.Sub(now).Hours() / 24)
	switch {
	case days < 0:
		return Check{Name: "certificate", Status: StatusFail,
			Detail: fmt.Sprintf("expired %d days ago", -days), Hint: "`termit-uv renew-cert --force`"}
	case days < 14:
		return Check{Name: "certificate", Status: StatusWarn,
			Detail: fmt.Sprintf("expires in %d days", days), Hint: "check `termit-uv renew-cert` and the renewal timer"}
	default:
		return Check{Name: "certificate", Status: StatusOK, Detail: fmt.Sprintf("expires in %d days", days)}
	}
}

func diagnoseDecoy(cfg *config.Config, st *state.State) Check {
	if st.Decoy.Seed == "" {
		return Check{Name: "decoy", Status: StatusFail, Detail: "no decoy seed in the state"}
	}
	site, err := decoy.Generate(decoy.Spec{
		Domain:   cfg.Domain,
		Seed:     st.Decoy.Seed,
		Template: st.Decoy.Template,
	})
	if err != nil {
		return Check{Name: "decoy", Status: StatusFail, Detail: err.Error()}
	}
	if site.Digest != st.Decoy.Digest {
		return Check{Name: "decoy", Status: StatusFail,
			Detail: "the generated site no longer matches its recorded digest",
			Hint:   "`termit-uv renew-site`"}
	}
	// The digest proves the seed has not changed; comparing the files proves nobody edited them.
	for name, want := range site.Files {
		got, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, filepath.FromSlash(name))) // #nosec G304 -- a generated path.
		switch {
		case errors.Is(err, os.ErrNotExist):
			return Check{Name: "decoy", Status: StatusFail, Detail: name + " is missing from " + cfg.Paths.SiteDir,
				Hint: "`termit-uv renew-site`"}
		case err != nil:
			return Check{Name: "decoy", Status: StatusFail, Detail: err.Error()}
		case !bytes.Equal(got, want):
			return Check{Name: "decoy", Status: StatusFail,
				Detail: name + " differs from what the seed produces", Hint: "`termit-uv renew-site`"}
		}
	}
	return Check{Name: "decoy", Status: StatusOK, Detail: site.Template + " site, files match the seed"}
}

// diagnoseTransport answers "is the QUIC transport working". It is a second process, so three things can
// be wrong on their own: the binary is gone, the config is unreadable to its user, or the unit is down.
func diagnoseTransport(ctx context.Context, exec Executor, cfg *config.Config) Check {
	if _, err := os.Stat(cfg.Paths.HysteriaBinary()); err != nil {
		return Check{Name: "transport", Status: StatusFail, Detail: "the transport binary is missing: " + cfg.Paths.HysteriaBinary(),
			Hint: "`termit-uv init`"}
	}
	info, err := os.Stat(cfg.Paths.HysteriaConfig)
	switch {
	case err != nil:
		return Check{Name: "transport", Status: StatusFail, Detail: "the transport config is missing: " + cfg.Paths.HysteriaConfig,
			Hint: "`termit-uv init`"}
	case chmodIsReal && info.Mode().Perm() != 0o640:
		return Check{Name: "transport", Status: StatusFail,
			Detail: fmt.Sprintf("%s has mode %04o, want 0640", cfg.Paths.HysteriaConfig, info.Mode().Perm())}
	}
	if _, err := exec.Run(ctx, Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", service.HysteriaUnitName}}); err != nil {
		return Check{Name: "transport", Status: StatusFail, Detail: service.HysteriaUnitName + " is not running",
			Hint: "systemctl status " + service.HysteriaUnitName + "; `termit-uv init` starts it again"}
	}
	return Check{Name: "transport", Status: StatusOK, Detail: "hysteria " + hysteriaVersion + ", " + cfg.Paths.HysteriaConfig}
}

// diagnoseFirewall answers "does the table this node applied still describe its configuration". The boot
// unit restores the record an operator confirmed, so a UDP port or an SSH source added afterwards waits
// for the next `firewall on`; on a node whose QUIC transport is on, that wait is a transport nobody can
// reach, and its packets are counted as a scan. A node with no table applied is not a finding here —
// `firewall status` already says that the configuration asks for one.
func diagnoseFirewall(cfg *config.Config) (Check, bool) {
	if !cfg.Security.Firewall.Enabled {
		return Check{}, false
	}
	applied, err := firewall.LoadApplied(cfg.Paths.FirewallPath())
	if err != nil {
		return Check{Name: "firewall", Status: StatusFail, Detail: err.Error()}, true
	}
	if applied == nil || !applied.RemovedAt.IsZero() {
		return Check{}, false
	}
	want, err := cfg.Security.Table(firewall.NodePorts)
	if err != nil {
		return Check{Name: "firewall", Status: StatusFail, Detail: err.Error()}, true
	}
	if drift := firewall.Drift(applied, want); len(drift) > 0 {
		return Check{
			Name:   "firewall",
			Status: StatusWarn,
			Detail: "the applied table was rendered from an older configuration (" + strings.Join(drift, ", ") + " differ)",
			Hint:   "`termit-uv firewall on`, then `termit-uv firewall confirm`",
		}, true
	}
	return Check{Name: "firewall", Status: StatusOK, Detail: "the applied table matches the configuration"}, true
}

func diagnoseProxy(ctx context.Context, exec Executor, cfg *config.Config) Check {
	path := cfg.Paths.XrayConfig
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: "proxy config", Status: StatusFail, Detail: "missing: " + path, Hint: "`termit-uv init`"}
	}
	if chmodIsReal && info.Mode().Perm() != 0o640 {
		return Check{Name: "proxy config", Status: StatusFail,
			Detail: fmt.Sprintf("%s has mode %04o, want 0640", path, info.Mode().Perm())}
	}
	if _, err := exec.Run(ctx, Cmd{Name: cfg.Paths.XrayBinary(), Args: []string{"run", "-test", "-config", path}}); err != nil {
		return Check{Name: "proxy config", Status: StatusFail, Detail: "the core rejects it: " + err.Error()}
	}
	return Check{Name: "proxy config", Status: StatusOK, Detail: path}
}
