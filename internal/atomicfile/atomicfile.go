// Package atomicfile writes a file so that a reader never sees a half-written one.
//
// Both binaries write configuration that a running service reads: a truncated certificate or a
// half-written database key would take the service down at the worst moment. The write is a temp file
// in the same directory, fsynced, then renamed over the target.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces filename with data atomically. The parent directory is created with mode 0750 when
// it does not exist; perm applies to the file.
func Write(filename string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("atomicfile: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(filename)+".tmp*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, filename); err != nil {
		return fmt.Errorf("atomicfile: rename %s: %w", tmpName, err)
	}
	// Flush the directory entry too: without it a crash can lose the rename even though the file data
	// made it to disk. Directories cannot be synced everywhere, so the error is not fatal.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
