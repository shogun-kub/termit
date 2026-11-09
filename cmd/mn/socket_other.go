//go:build !linux

package main

import (
	"errors"
	"net"
)

// listenAdmin has no implementation outside Linux: the admin surface is a unix socket with 0600, and
// a platform without one gets a clear refusal instead of a second, weaker transport.
func listenAdmin(path string) (net.Listener, error) {
	return nil, errors.New("the admin socket is a Linux feature; run the server on the target host")
}
