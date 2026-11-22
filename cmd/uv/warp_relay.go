package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/signal"
	"syscall"

	"github.com/shogun-kub/termit/internal/uv/socksrelay"
	"github.com/shogun-kub/termit/internal/uv/xray"
)

// runWarpRelay runs the loopback SOCKS5 relay the MASQUE transport needs. It is the address the core
// dials (`warp.transport: usque`): the sidecar keys each UDP association on the control connection's
// address, and the relay is what makes the datagrams arrive from that address.
func (a *application) runWarpRelay(args []string) int {
	flags := a.newFlagSet("warp-relay", `The loopback SOCKS5 relay in front of the MASQUE sidecar. Run by
termit-usque-relay.service; the sidecar refuses a UDP datagram that does not come from the port of the
connection that asked for the association, and the relay sends them from exactly that port.`)
	listen := flags.String("listen", xray.DefaultUsqueSocks, "loopback address the core's SOCKS hop connects to")
	upstream := flags.String("upstream", xray.DefaultUsqueSidecar, "loopback address of the MASQUE sidecar's SOCKS5 listener")
	if err := flags.Parse(args); err != nil {
		return a.flagError(err)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(a.stderr, "termit-uv warp-relay: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	for _, address := range []string{*listen, *upstream} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			fmt.Fprintf(a.stderr, "termit-uv warp-relay: %q is not an address: %v\n", address, err)
			return exitUsage
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err := socksrelay.Serve(ctx, *listen, *upstream, slog.Default())
	if err != nil && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
		fmt.Fprintf(a.stderr, "termit-uv warp-relay: %v\n", err)
		return exitFailure
	}
	return exitOK
}
