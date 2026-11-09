//go:build linux

package install

import (
	"os"
	"syscall"
)

// defaultEUID reports the effective user id.
func defaultEUID() int { return os.Geteuid() }

// chmodIsReal reports whether the OS keeps the permission bits we set. It does on Linux, which is the
// only platform the installer runs on.
const chmodIsReal = true

// linkSite enables a vhost the way Debian's nginx packaging expects: a symlink from sites-available
// into sites-enabled, so disabling one is removing a link and not a file.
func linkSite(target, link string, _ []byte) error { return os.Symlink(target, link) }

// diskFree returns the bytes available on the filesystem that holds dir.
func diskFree(dir string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	//nolint:unconvert // the field types differ between architectures.
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
