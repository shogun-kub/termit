package install

import (
	"context"

	"github.com/shogun-kub/termit/internal/uv/config"
	"github.com/shogun-kub/termit/internal/uv/warp"
)

// diagnoseWarp is the one check that leaves the host: it fetches Cloudflare's trace through the
// tunnel, which is the only way to know whether the egress is really WARP and not merely a listener
// that accepts bytes.
//
// A host that cannot run WARP is a skip, not a failure: the node works, it just exits directly. A
// host that should run it and does not is a failure, because then the address the owner wanted to
// hide is the node's own.
func diagnoseWarp(ctx context.Context, cfg *config.Config, client warp.Client) Check {
	check := Check{Name: "warp"}
	if !cfg.Warp.UsesWarp() {
		check.Status = StatusSkip
		check.Detail = "off: proxied traffic exits directly"
		return check
	}
	if err := client.Host.Check(); err != nil {
		check.Status = StatusSkip
		check.Detail = err.Error()
		check.Hint = "the node exits directly; this is expected in a container without /dev/net/tun"
		return check
	}
	status, err := client.Status(ctx)
	switch {
	case err != nil:
		check.Status = StatusFail
		check.Detail = errorText(err)
	case status.State == "missing":
		check.Status = StatusFail
		check.Detail = "cloudflare-warp is not installed"
		check.Hint = "run `uv init --only warp` to install and register it"
	case status.Healthy():
		check.Status = StatusOK
		check.Detail = status.Detail
	default:
		check.Status = StatusFail
		check.Detail = status.Detail
		if cfg.Warp.FailOpenEnabled() {
			check.Hint = "fail-open is on, so proxied traffic exits directly until the tunnel is back"
		} else {
			check.Hint = "fail-open is off, so proxied traffic is failing until the tunnel is back"
		}
	}
	return check
}
