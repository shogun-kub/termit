package cert

import (
	"context"
	"log/slog"
	"time"
)

// defaultRenewCheck is how often a running server re-evaluates its certificate. Six hours is far
// inside a 30-day renewal window and cheap enough to ignore.
const defaultRenewCheck = 6 * time.Hour

// renewTimeout bounds one renewal attempt: an ACME order that hangs must not wedge the loop.
const renewTimeout = 10 * time.Minute

// KeepFresh re-evaluates the certificate on every tick and issues a new one when it is due.
//
// It is the piece that makes a management server able to outlive its first certificate. The server
// issues at startup and then keeps this loop: a renewal writes the files, the loader picks them up on
// the next handshake, and no restart is needed. A failed attempt is logged and retried on the next
// tick; the certificate already on disk keeps serving, because nothing is written until the CA
// returns a chain.
func KeepFresh(ctx context.Context, interval time.Duration, renew func(context.Context) (bool, error), logger *slog.Logger) {
	if interval <= 0 {
		interval = defaultRenewCheck
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attemptCtx, cancel := context.WithTimeout(ctx, renewTimeout)
			renewed, err := renew(attemptCtx)
			cancel()
			switch {
			case err != nil && ctx.Err() == nil:
				logger.Error("certificate renewal failed; the current certificate stays in service", "err", err)
			case renewed:
				logger.Info("certificate renewed and in service")
			}
		}
	}
}
