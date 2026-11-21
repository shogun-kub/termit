package vulns

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestBlockedIsTheReleaseAfterTheLastOneTolerated: the deadline is the *next* release, so the version
// that names it must already be clean. A pre-release of that version is that version: v0.6.0p1 must not
// slip past a 0.6.0 deadline.
func TestBlockedIsTheReleaseAfterTheLastOneTolerated(t *testing.T) {
	t.Parallel()
	file := File{Exclusions: []Exclusion{{ID: "GO-1", MustBeGoneBy: "0.6.0", Action: "upgrade"}}}
	tests := []struct {
		version string
		blocked bool
	}{
		{"v0.5.0", false},
		{"v0.5.0p3", false},
		{"v0.6.0", true},
		{"0.6.0", true},
		{"v0.6.0p1", true},
		{"v0.7.0", true},
	}
	for _, tc := range tests {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			if got := len(file.Blocked(tc.version)) > 0; got != tc.blocked {
				t.Errorf("Blocked(%q) = %v, want %v", tc.version, got, tc.blocked)
			}
		})
	}
}

func TestAllowedListsEveryIdentifier(t *testing.T) {
	t.Parallel()
	file := File{Exclusions: []Exclusion{{ID: "GO-1", MustBeGoneBy: "1.0.0"}, {ID: "GO-2", MustBeGoneBy: "1.0.0"}}}
	allowed := file.Allowed()
	if len(allowed) != 2 || allowed[0] != "GO-1" || allowed[1] != "GO-2" {
		t.Fatalf("Allowed() = %v", allowed)
	}
}

// TestLoadAcceptsAMissingFile: a repository that never needed an exclusion is not broken.
func TestLoadAcceptsAMissingFile(t *testing.T) {
	t.Parallel()
	file, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(file.Exclusions) != 0 || len(file.Blocked("1.0.0")) != 0 {
		t.Fatalf("a missing file produced %+v", file)
	}
}

func TestLoadRefusesAnEntryWithoutADeadline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "vulns.json")
	write(t, path, `{"exclusions": [{"id": "GO-1", "must_be_gone_by": "soon"}]}`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a deadline that is not a version")
	}
	write(t, path, `{"exclusions": [{"must_be_gone_by": "1.0.0"}]}`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted an exclusion with no id")
	}
}
