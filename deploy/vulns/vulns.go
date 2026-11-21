// Package vulns is the repository's list of advisories it has deliberately accepted, and the rules that
// stop the list from becoming permanent.
//
// govulncheck has no exclusion mechanism of its own. Without one, a build that carries an advisory it
// cannot fix yet has two options: fail forever, or paper over the gate. This file is the third: every
// accepted advisory names the release after which it must be gone, and both the vulnerability gate and
// the release tool refuse to move past that release while the entry is still there.
package vulns

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Exclusion is one advisory the build is allowed to carry, with the release that must not see it.
type Exclusion struct {
	// ID is the GO-YYYY-NNNN identifier govulncheck reports.
	ID string `json:"id"`
	// Module is the module the advisory belongs to, for the reader; it is not matched against.
	Module string `json:"module,omitempty"`
	// MustBeGoneBy is the first release that must not carry this advisory. A version equal to it, or
	// later, is refused — pre-release suffixes included.
	MustBeGoneBy string `json:"must_be_gone_by"`
	// Reason says why the build tolerates the advisory today.
	Reason string `json:"reason,omitempty"`
	// Action says what to do to remove the entry. It is printed by every refusal and every gate run,
	// so the reminder reaches the person who needs it without anyone reading this file.
	Action string `json:"action,omitempty"`
}

// File is the shape of deploy/vulnerabilities.json.
type File struct {
	Note       string      `json:"note"`
	Exclusions []Exclusion `json:"exclusions"`
}

// Load reads the exclusion list. A missing file means the build carries nothing it has accepted, which
// is a valid state: a repository that never needed an exclusion does not have to keep an empty file.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the caller names the path.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("read %s: %w", path, err)
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, exclusion := range file.Exclusions {
		if exclusion.ID == "" {
			return File{}, fmt.Errorf("%s: an exclusion has no id", path)
		}
		if _, ok := parseVersion(exclusion.MustBeGoneBy); !ok {
			return File{}, fmt.Errorf("%s: %s has no usable must_be_gone_by (%q)", path, exclusion.ID, exclusion.MustBeGoneBy)
		}
	}
	return file, nil
}

// Allowed reports the identifiers the build may carry.
func (f File) Allowed() []string {
	ids := make([]string, 0, len(f.Exclusions))
	for _, exclusion := range f.Exclusions {
		ids = append(ids, exclusion.ID)
	}
	return ids
}

// Blocked returns the exclusions whose deadline the given version has reached or passed: those are the
// ones that must be removed before this version can be cut.
func (f File) Blocked(version string) []Exclusion {
	want, ok := parseVersion(version)
	if !ok {
		return nil
	}
	var blocked []Exclusion
	for _, exclusion := range f.Exclusions {
		deadline, ok := parseVersion(exclusion.MustBeGoneBy)
		if !ok {
			continue
		}
		if compare(want, deadline) >= 0 {
			blocked = append(blocked, exclusion)
		}
	}
	return blocked
}

// parseVersion reads "0.6.0", "v0.6.0" and "v0.6.0p2" as the same number of numeric fields: a
// pre-release of a release is that release for the purpose of a deadline, never an excuse to slip past it.
func parseVersion(version string) ([]int, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if cut := strings.IndexByte(trimmed, 'p'); cut > 0 {
		trimmed = trimmed[:cut]
	}
	if trimmed == "" {
		return nil, false
	}
	parts := strings.Split(trimmed, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
		value := 0
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return nil, false
			}
			value = value*10 + int(digit-'0')
		}
		numbers = append(numbers, value)
	}
	return numbers, true
}

func compare(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var left, right int
		if i < len(a) {
			left = a[i]
		}
		if i < len(b) {
			right = b[i]
		}
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	return 0
}
