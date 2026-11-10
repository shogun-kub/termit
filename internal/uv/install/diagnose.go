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
	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/decoy"
	"github.com/shogun-kub/termit/internal/uv/service"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// DiagnoseOptions configure the health half of `uv doctor`.
type DiagnoseOptions struct {
	Config *config.Config
	Exec   Executor
	Now    func() time.Time
	// WarpHost overrides the platform facts WARP is judged on. Tests set it so the check does not
	// depend on whether the machine running the suite has a tun device.
	WarpHost *warp.Host
}

// Diagnose checks the installed node, not the host: the state, the services, the certificate, the
// generated decoy and the generated proxy config. It is what `uv doctor` adds once a node exists —
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
			Hint: "run `uv init` to install this node"}}
	}

	checks := []Check{{
		Name:   "state",
		Status: StatusOK,
		Detail: fmt.Sprintf("%s, node identity %s", cfg.Paths.StatePath(), state.Fingerprint(st.Identity.SelfUUID)),
	}}
	checks = append(checks, diagnoseServices(ctx, opts.Exec))
	checks = append(checks, diagnoseCertificate(cfg, st, opts.Now()))
	checks = append(checks, diagnoseDecoy(cfg, st))
	checks = append(checks, diagnoseProxy(ctx, opts.Exec, cfg))
	warpClient := warp.New(warpRunner{exec: opts.Exec})
	if opts.WarpHost != nil {
		warpClient.Host = *opts.WarpHost
	}
	checks = append(checks, diagnoseWarp(ctx, cfg, warpClient))
	return checks
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
			Hint: "systemctl status <unit>; `uv init` starts them again"}
	}
	return Check{Name: "services", Status: StatusOK, Detail: "core, web tier, renewal timer and agent are running"}
}

func diagnoseCertificate(cfg *config.Config, st *state.State, now time.Time) Check {
	notAfter, err := acme.CertNotAfter(filepath.Join(cfg.Paths.CertDir, certFile))
	switch {
	case os.IsNotExist(err):
		return Check{Name: "certificate", Status: StatusFail, Detail: "no certificate",
			Hint: "`uv renew-cert`"}
	case err != nil:
		return Check{Name: "certificate", Status: StatusFail, Detail: err.Error()}
	}
	days := int(notAfter.Sub(now).Hours() / 24)
	switch {
	case days < 0:
		return Check{Name: "certificate", Status: StatusFail,
			Detail: fmt.Sprintf("expired %d days ago", -days), Hint: "`uv renew-cert --force`"}
	case days < 14:
		return Check{Name: "certificate", Status: StatusWarn,
			Detail: fmt.Sprintf("expires in %d days", days), Hint: "check `uv renew-cert` and the renewal timer"}
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
			Hint:   "`uv renew-site`"}
	}
	// The digest proves the seed has not changed; comparing the files proves nobody edited them.
	for name, want := range site.Files {
		got, err := os.ReadFile(filepath.Join(cfg.Paths.SiteDir, filepath.FromSlash(name))) // #nosec G304 -- a generated path.
		switch {
		case errors.Is(err, os.ErrNotExist):
			return Check{Name: "decoy", Status: StatusFail, Detail: name + " is missing from " + cfg.Paths.SiteDir,
				Hint: "`uv renew-site`"}
		case err != nil:
			return Check{Name: "decoy", Status: StatusFail, Detail: err.Error()}
		case !bytes.Equal(got, want):
			return Check{Name: "decoy", Status: StatusFail,
				Detail: name + " differs from what the seed produces", Hint: "`uv renew-site`"}
		}
	}
	return Check{Name: "decoy", Status: StatusOK, Detail: site.Template + " site, files match the seed"}
}

func diagnoseProxy(ctx context.Context, exec Executor, cfg *config.Config) Check {
	path := cfg.Paths.XrayConfig
	info, err := os.Stat(path)
	if err != nil {
		return Check{Name: "proxy config", Status: StatusFail, Detail: "missing: " + path, Hint: "`uv init`"}
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
