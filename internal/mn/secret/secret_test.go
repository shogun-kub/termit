package secret_test

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/secret"
)

func TestHashAndVerify(t *testing.T) {
	t.Parallel()
	hash, err := secret.Hash("agent-key-value")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(hash, "argon2id$") {
		t.Errorf("hash %q is not argon2id", hash)
	}
	if strings.Contains(hash, "agent-key-value") {
		t.Error("the hash contains the value it hashes")
	}
	if err := secret.Verify(hash, "agent-key-value"); err != nil {
		t.Errorf("Verify with the right value: %v", err)
	}
	if err := secret.Verify(hash, "agent-key-wrong"); err == nil {
		t.Error("Verify accepted the wrong value")
	}

	// The same value hashed twice must not produce the same string: the salt is what makes a stolen
	// database useless for a rainbow table.
	second, err := secret.Hash("agent-key-value")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if hash == second {
		t.Error("two hashes of the same value are identical")
	}
}

func TestHashRefusesEmpty(t *testing.T) {
	t.Parallel()
	if _, err := secret.Hash(""); err == nil {
		t.Error("Hash accepted an empty value")
	}
}

func TestVerifyRejectsForeignInput(t *testing.T) {
	t.Parallel()
	for _, encoded := range []string{
		"",
		"plain-text",
		"argon2id$v=19$m=65536,t=1,p=4$only-four-parts",
		"bcrypt$whatever",
	} {
		if err := secret.Verify(encoded, "value"); err == nil {
			t.Errorf("Verify accepted %q as a hash", encoded)
		}
	}
}

func TestTokenAndCode(t *testing.T) {
	t.Parallel()
	token, err := secret.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if len(token) < 40 {
		t.Errorf("token %q is shorter than 40 characters", token)
	}
	if strings.ContainsAny(token, "+/=") {
		t.Errorf("token %q is not URL-safe", token)
	}

	code, err := secret.Code()
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if got := len(strings.ReplaceAll(code, "-", "")); got < 30 {
		t.Errorf("code %q carries only %d characters", code, got)
	}
	for _, group := range strings.Split(code, "-") {
		if len(group) != 4 {
			t.Errorf("code %q has a group of %d characters, want 4", code, len(group))
		}
		if group != strings.ToUpper(group) {
			t.Errorf("code %q is not upper case", code)
		}
	}

	other, err := secret.Code()
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if other == code {
		t.Error("two generated codes are identical")
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()
	if got := secret.Fingerprint("abcdefghijkl"); got != "abcdefgh" {
		t.Errorf("Fingerprint = %q, want the first eight characters", got)
	}
	if got := secret.Fingerprint("short"); got == "" || strings.Contains(got, "short") {
		t.Errorf("Fingerprint of a short value = %q, want a placeholder", got)
	}
}
