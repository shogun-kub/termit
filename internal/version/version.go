// Package version reports build and protocol identity for both binaries.
package version

import (
	"fmt"
	"regexp"
	"strconv"
)

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

// Release is the shape of a published release and nothing else: a pre-release build (v0.7.0p4) and a
// development build (dev) are not releases. internal/release publishes under the same rule, so the
// binaries and the release tooling cannot disagree about what a version is.
var Release = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// buildShape splits a build identity into its release numbers and whatever was appended to them:
// v0.7.0p4 is release 0.7.0 with the suffix "p4", and a stamped development build carries a commit.
var buildShape = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(.*)$`)

// Below reports whether build predates floor, a plain release such as v0.7.0 — the answer `doctor` and
// `status` give to "is this node older than its server still supports?". A build carrying a suffix is
// below the release of the same numbers: v0.7.0p4 exists to be superseded by v0.7.0. comparable is false
// when either side cannot be ordered at all ("dev", "none", an empty or malformed floor), and a caller
// then reports nothing rather than a guess.
func Below(build, floor string) (below, comparable bool) {
	f, ok := parseBuild(floor)
	if !ok {
		return false, false
	}
	b, ok := parseBuild(build)
	if !ok {
		return false, false
	}
	for i := range b.nums {
		if b.nums[i] != f.nums[i] {
			return b.nums[i] < f.nums[i], true
		}
	}
	return b.suffix != "" && f.suffix == "", true
}

type buildNumber struct {
	nums   [3]int
	suffix string
}

func parseBuild(s string) (buildNumber, bool) {
	m := buildShape.FindStringSubmatch(s)
	if m == nil {
		return buildNumber{}, false
	}
	var out buildNumber
	for i := range out.nums {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return buildNumber{}, false
		}
		out.nums[i] = n
	}
	out.suffix = m[4]
	return out, true
}
