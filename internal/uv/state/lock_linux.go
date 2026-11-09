//go:build linux

package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Lock takes an exclusive advisory lock on filename without waiting. It returns ErrLocked when
// another process holds it, so a manual `uv sync` and the agent loop cannot rewrite the same core
// config at once. The returned function releases the lock; it is safe to call more than once.
func Lock(filename string) (func(), error) {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- a path from our own state dir.
	if err != nil {
		return nil, fmt.Errorf("state: open lock %s: %w", filename, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("state: %s is locked by another run: %w", filename, ErrLocked)
		}
		return nil, fmt.Errorf("state: lock %s: %w", filename, err)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
