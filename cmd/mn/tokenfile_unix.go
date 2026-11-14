//go:build !windows

package main

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// checkTokenFilePermissions is the reason this feature is more than "read the file". A token file that
// the group or the world can read is a leaked credential that looks like a tidy setup, and one owned by
// somebody else is a file that user can rewrite to choose the credential this command presents. Both
// are refused with the exact fix, because a refusal with a `chmod` in it is a refusal somebody can act
// on.
func checkTokenFilePermissions(path string, info fs.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("admin token file %s is mode %04o: make it 0600 so nobody else can read the "+
			"credential (chmod 600 %s)", path, perm, path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := uint32(os.Geteuid()); stat.Uid != uid {
		return fmt.Errorf("admin token file %s is owned by uid %d, not by uid %d running this command: "+
			"whoever owns it can change the credential this command authenticates with", path, stat.Uid, uid)
	}
	return nil
}
