//go:build !linux

package install

import (
	"errors"
	"os"
)

// defaultEUID reports the effective user id; it is -1 where the notion does not exist.
func defaultEUID() int { return os.Geteuid() }

// chmodIsReal is false here: the test host's filesystem does not keep the bits, so a mode assertion
// would fail for a reason that has nothing to do with the code.
const chmodIsReal = false

// linkSite copies the vhost instead of linking it: the test host cannot create symlinks without a
// privilege, and the installer only ever runs on Linux.
func linkSite(_, link string, content []byte) error { return os.WriteFile(link, content, 0o644) }

// diskFree is unavailable off Linux: Termit targets Debian, and a unit test must not need statfs.
func diskFree(string) (uint64, error) {
	return 0, errors.New("free space is only measured on Linux")
}
