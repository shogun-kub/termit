package version_test

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/version"
)

func TestAgentIdentityFitsTheProtocol(t *testing.T) {
	t.Parallel()
	// A release banner does not fit the 64-byte field the protocol allows; a release that could not
	// enroll because of its own version string is the kind of bug only a stamped build finds.
	short := version.Agent("termit-uv")
	if len(short) > 64 {
		t.Errorf("agent version %q is longer than the protocol allows", short)
	}
	if !strings.Contains(short, "termit-uv") || !strings.Contains(short, version.Version) {
		t.Errorf("agent version %q does not identify the build", short)
	}
}
