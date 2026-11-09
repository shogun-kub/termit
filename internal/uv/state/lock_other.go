//go:build !linux

package state

// Lock is a no-op away from Linux. The agent loop only runs there; a Windows test or a developer
// command must still be able to call the same code path instead of failing on a missing syscall.
func Lock(string) (func(), error) {
	return func() {}, nil
}
