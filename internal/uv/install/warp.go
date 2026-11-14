package install

import (
	"context"

	"github.com/shogun-kub/termit/internal/uv/warp"
)

// warpStep converges the Cloudflare WARP sidecar.
//
// WARP is on by default, but it cannot run everywhere: an unprivileged container has no
// /dev/net/tun, and Cloudflare has no package for every architecture. In that case the step records
// the refusal and reports the converged state instead of failing the node — a node without WARP is
// still a working node, and its xray route fails open to direct. The operator sees the reason in the
// install summary and in `termit-uv doctor`; nothing is hidden.
type warpStep struct{ e *stepEnv }

func (*warpStep) Name() string { return "warp" }

func (s *warpStep) client() warp.Client {
	client := warp.New(warpRunner{s.e.exec})
	if s.e.warpHost != nil {
		client.Host = *s.e.warpHost
	}
	return client
}

// Check is offline on purpose: it runs on every `termit-uv init`, including the ones in tests and on a
// machine with no route to Cloudflare.
func (s *warpStep) Check(ctx context.Context) (CheckResult, error) {
	if !s.e.cfg.Warp.UsesWarp() {
		return CheckResult{Satisfied: true, Detail: "off"}, nil
	}
	client := s.client()
	if err := client.Host.Check(); err != nil {
		return CheckResult{Satisfied: true, Detail: err.Error()}, nil
	}
	if !client.Installed(ctx) {
		return CheckResult{Detail: warpPackage + " is not installed"}, nil
	}
	status, err := client.LocalStatus(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	if status.State == "connected" {
		return CheckResult{Satisfied: true, Detail: "connected"}, nil
	}
	return CheckResult{Detail: "warp-cli is " + status.State}, nil
}

func (s *warpStep) Run(ctx context.Context) error {
	return s.client().Ensure(ctx)
}

// warpRunner adapts the install executor to the warp package's small command interface.
type warpRunner struct{ exec Executor }

func (r warpRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	result, err := r.exec.Run(ctx, Cmd{Name: name, Args: args})
	return result.Stdout, result.Stderr, err
}
