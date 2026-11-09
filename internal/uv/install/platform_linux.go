//go:build linux

package install

import (
	"os"
	"syscall"
)

// defaultEUID reports the effective user id.
func defaultEUID() int { return os.Geteuid() }

// diskFree returns the bytes available on the filesystem that holds dir.
func diskFree(dir string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	//nolint:unconvert // the field types differ between architectures.
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
