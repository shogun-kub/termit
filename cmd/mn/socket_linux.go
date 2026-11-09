//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// listenAdmin binds the admin socket. It is a unix socket with 0600: the operator runs `mn` as root
// (or with sudo) on the same machine, and nothing else can reach the registry.
func listenAdmin(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("admin socket: create %s: %w", filepath.Dir(path), err)
	}
	// A stale socket from a previous run would make Listen fail; a socket in use fails below instead.
	if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(path)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("admin socket: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("admin socket: restrict %s: %w", path, err)
	}
	return listener, nil
}
