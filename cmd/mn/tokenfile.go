package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/shogun-kub/termit/internal/atomicfile"
)

// maxTokenFileSize bounds what the CLI will read as a credential. A token is 43 bytes; anything near
// this limit is a mistake, most likely a path that points at the wrong file.
const maxTokenFileSize = 4096

// readTokenFile reads an admin token from a file and refuses to use one it cannot vouch for.
//
// The permission check is the point of this function, not decoration. A token file that is readable by
// the group or the world is a leaked credential that looks like a tidy setup, and the operator finds
// out only after somebody else does. Refusing is the honest answer: it is fixable in one `chmod`, and
// the message says how.
func readTokenFile(path string) (string, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("admin token file %s does not exist", path)
	case err != nil:
		return "", fmt.Errorf("admin token file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("admin token file %s is not a regular file", path)
	}
	if err := checkTokenFilePermissions(path, info); err != nil {
		return "", err
	}
	if info.Size() > maxTokenFileSize {
		return "", fmt.Errorf("admin token file %s is %d bytes, which is too large for a token (limit %d)",
			path, info.Size(), maxTokenFileSize)
	}

	data, err := os.ReadFile(path) // #nosec G304 -- the operator chooses the path on purpose.
	if err != nil {
		return "", fmt.Errorf("admin token file %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("admin token file %s is empty", path)
	}
	return token, nil
}

// writeTokenFile stores a freshly created admin token where the operator asked for it. The file is
// written 0600 and atomically, so a partially written token cannot survive a crash.
func writeTokenFile(path, token string) error {
	if path == "" {
		return errors.New("admin token file path is empty")
	}
	if err := atomicfile.Write(path, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("write admin token to %s: %w", path, err)
	}
	return nil
}
