// Package version reports build and protocol identity for both binaries.
package version

import "fmt"

// Build metadata, injected at link time:
//
//	-X github.com/shogun-kub/termit/internal/version.Version=v0.1.0
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

const (
	// APIVersion is the MN<->UV protocol version. A change here is a breaking
	// change for already-installed agents.
	APIVersion = "1"

	// ProfileVersion identifies the proxy profile (VLESS+XHTTP+TLS). MN refuses
	// nodes that report a profile it does not know.
	ProfileVersion = "v1"
)

// String renders a single line identifying one of the binaries.
func String(component string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s, api %s, profile %s)",
		component, Version, Commit, Date, APIVersion, ProfileVersion)
}
