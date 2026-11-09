package cred_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/cred"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func master(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// vectors are the shipped derivation: if a change makes these fail, every credential in the mesh has
// to be reissued, and that is a migration with a new epoch, not a refactor.
func TestVectors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b  string
		epoch int
		want  string
	}{
		{"uv_a", "uv_b", 1, "83fc1cf9-e38c-48a3-825b-8464bff64f6e"},
		{"uv_a", "uv_b", 2, "c391386b-ef27-4973-b616-f72dbda39a96"},
	} {
		got, err := cred.Pair(master(1), tc.a, tc.b, tc.epoch)
		if err != nil {
			t.Fatalf("Pair: %v", err)
		}
		if !uuidPattern.MatchString(got) {
			t.Errorf("Pair = %q, which is not a v4 UUID", got)
		}
		if tc.want != "" && got != tc.want {
			t.Errorf("Pair(%s,%s,%d) = %s, want %s", tc.a, tc.b, tc.epoch, got, tc.want)
		}
	}
}

func TestOrderDoesNotMatter(t *testing.T) {
	t.Parallel()
	first, err := cred.Pair(master(7), "uv_alpha", "uv_beta", 1)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	second, err := cred.Pair(master(7), "uv_beta", "uv_alpha", 1)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if first != second {
		t.Errorf("the pair credential depends on argument order: %s != %s", first, second)
	}
}

func TestEpochChangesTheCredential(t *testing.T) {
	t.Parallel()
	first, err := cred.Pair(master(3), "uv_a", "uv_b", 1)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	second, err := cred.Pair(master(3), "uv_a", "uv_b", 2)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if first == second {
		t.Error("a new epoch produced the same credential")
	}
}

func TestDifferentInputsDiffer(t *testing.T) {
	t.Parallel()
	base, err := cred.Pair(master(1), "uv_a", "uv_b", 1)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	for _, other := range []struct {
		name   string
		master []byte
		a, b   string
	}{
		{"another master", master(2), "uv_a", "uv_b"},
		{"another pair", master(1), "uv_a", "uv_c"},
		{"a shifted id", master(1), "uv_a", "uv_b "},
	} {
		got, err := cred.Pair(other.master, other.a, other.b, 1)
		if err != nil {
			t.Fatalf("Pair: %v", err)
		}
		if got == base {
			t.Errorf("%s produced the same credential", other.name)
		}
	}
	// Field boundaries matter: ("ab","c") must not hash like ("a","bc").
	joined, err := cred.Pair(master(1), "ab", "c", 1)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	split, err := cred.Pair(master(1), "a", "bc", 1)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if joined == split {
		t.Error("the derivation ignores the field boundary between the two ids")
	}
}

func TestRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		master []byte
		a, b   string
		epoch  int
	}{
		{"short master", []byte("too short"), "uv_a", "uv_b", 1},
		{"no master", nil, "uv_a", "uv_b", 1},
		{"empty id", master(1), "", "uv_b", 1},
		{"self pair", master(1), "uv_a", "uv_a", 1},
		{"epoch zero", master(1), "uv_a", "uv_b", 0},
		{"negative epoch", master(1), "uv_a", "uv_b", -1},
	} {
		if _, err := cred.Pair(tc.master, tc.a, tc.b, tc.epoch); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

func TestDeterministicForTheSameInput(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		got, err := cred.Pair(master(9), "uv_a", "uv_b", 1)
		if err != nil {
			t.Fatalf("Pair: %v", err)
		}
		if strings.Contains(got, "uv_a") {
			t.Errorf("the credential contains an id: %s", got)
		}
		seen[got] = true
	}
	if len(seen) != 1 {
		t.Errorf("the derivation is not deterministic: %d distinct results", len(seen))
	}
}
