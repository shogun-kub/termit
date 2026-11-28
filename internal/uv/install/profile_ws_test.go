package install

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// The second rendering is an escape hatch, not a default: a node that does not enable it has neither the
// inbound nor the nginx location, and one that does has both, on the path derived from the same secret.
func TestTheWebSocketRenderingFollowsTheConfiguration(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		name := "off by default"
		if enabled {
			name = "on when asked"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t)
			cfg.Profile.WebSocket = enabled
			env := &stepEnv{
				cfg:  cfg,
				exec: &fakeExecutor{},
				log:  slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				now:  func() time.Time { return engineNow },
				arch: "amd64",
				st:   state.New(cfg.Domain, engineNow),
			}
			env.st.Identity.XHTTPPath = "2ed18341babc42659bbd61de3bfce069"
			env.st.Identity.SelfUUID = "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5"

			core, err := (&xrayConfigStep{env}).spec()
			if err != nil {
				t.Fatalf("xray spec: %v", err)
			}
			site, err := (&webStep{env}).spec(context.Background())
			if err != nil {
				t.Fatalf("web spec: %v", err)
			}

			if !enabled {
				if core.WebSocketPath != "" || site.WebSocketPath != "" {
					t.Fatalf("an unasked rendering exists: core %q, vhost %q", core.WebSocketPath, site.WebSocketPath)
				}
				return
			}
			if core.WebSocketPath == "" || site.WebSocketPath == "" {
				t.Fatalf("the rendering is missing: core %q, vhost %q", core.WebSocketPath, site.WebSocketPath)
			}
			if want := proto.WebSocketPath(core.Path); core.WebSocketPath != want {
				t.Errorf("the path is %q, want the derived %q", core.WebSocketPath, want)
			}
			if core.WebSocketPath != site.WebSocketPath {
				t.Errorf("core and vhost disagree: %q vs %q", core.WebSocketPath, site.WebSocketPath)
			}
			if site.WebSocketAddr != xray.WebSocketAddress() {
				t.Errorf("the vhost forwards to %q, want %q", site.WebSocketAddr, xray.WebSocketAddress())
			}
			if !strings.HasPrefix(core.WebSocketPath, "/ws/") {
				t.Errorf("the path is %q", core.WebSocketPath)
			}
		})
	}
}
