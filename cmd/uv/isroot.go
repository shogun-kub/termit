package main

import (
	"fmt"
	"os"
)

// isRoot answers "may this process change the host's firewall".
func (a *application) isRoot() bool {
	euid := a.euid
	if euid == nil {
		euid = os.Geteuid
	}
	return euid() == 0
}

// executablePath is the binary the generated units run.
func (a *application) executablePath() (string, error) {
	if a.executable != "" {
		return a.executable, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	return path, nil
}
