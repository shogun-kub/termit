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

// Agent renders the compact identity a node reports to the server: the component, the release version
// and a short commit. The protocol caps this field at 64 bytes, and the full banner does not fit — a
// release that could not enroll is worse than a version string nobody reads.
func Agent(component string) string {
	identity := component + " " + Version
	if Commit != "" && Commit != "none" {
		short := Commit
		if len(short) > 8 {
			short = short[:8]
		}
		identity += " (" + short + ")"
	}
	if len(identity) > maxAgentLen {
		identity = identity[:maxAgentLen]
	}
	return identity
}

// maxAgentLen is the protocol's limit for the reported agent version.
const maxAgentLen = 64

// String renders a single line identifying one of the binaries.
func String(component string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s, api %s, profile %s)",
		component, Version, Commit, Date, APIVersion, ProfileVersion)
}
