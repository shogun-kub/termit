//go:build windows

package main

import "io/fs"

// checkTokenFilePermissions does nothing on Windows, where os.Chmod only toggles the read-only bit and
// a POSIX mode check would reject every file. The management server targets Linux; this build exists so
// the package compiles and can be vetted on a development host.
func checkTokenFilePermissions(string, fs.FileInfo) error { return nil }
