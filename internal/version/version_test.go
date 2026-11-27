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

func TestBelowOrdersBuildsAgainstARelease(t *testing.T) {
	t.Parallel()
	cases := []struct {
		build, floor string
		below        bool
		comparable   bool
	}{
		{"v0.6.0", "v0.7.0", true, true},
		{"v0.7.0", "v0.7.0", false, true},
		// A pre-release build predates the release of the same numbers: it exists to be superseded.
		{"v0.7.0p4", "v0.7.0", true, true},
		{"v0.8.0", "v0.7.0", false, true},
		{"v0.7.1", "v0.7.0", false, true},
		// Numbers, not strings: v10 is newer than v9.
		{"v10.0.0", "v9.9.9", false, true},
		// A development build cannot be ordered against a release, and neither can a malformed floor.
		{"dev", "v0.7.0", false, false},
		{"v0.7.0", "dev", false, false},
		{"v0.7.0", "", false, false},
		{"v0.7", "v0.7.0", false, false},
	}
	for _, tc := range cases {
		below, comparable := version.Below(tc.build, tc.floor)
		if below != tc.below || comparable != tc.comparable {
			t.Errorf("Below(%q, %q) = %v, %v; want %v, %v",
				tc.build, tc.floor, below, comparable, tc.below, tc.comparable)
		}
	}
}
