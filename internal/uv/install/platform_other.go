//go:build !linux

package install

import (
	"errors"
	"os"
)

// defaultEUID reports the effective user id; it is -1 where the notion does not exist.
func defaultEUID() int { return os.Geteuid() }

// diskFree is unavailable off Linux: Termit targets Debian, and a unit test must not need statfs.
func diskFree(string) (uint64, error) {
	return 0, errors.New("free space is only measured on Linux")
}
