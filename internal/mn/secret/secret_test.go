package secret_test

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/secret"
)

func TestDigest(t *testing.T) {
	t.Parallel()
	first := secret.Digest("invite-code")
	if !strings.HasPrefix(first, "sha256:") {
		t.Errorf("digest %q is not a sha256 digest", first)
	}
	if first != secret.Digest("invite-code") {
		t.Error("Digest is not deterministic; a stored credential could never be found again")
	}
	if first == secret.Digest("invite-code-other") {
		t.Error("two different values share a digest")
	}
	if strings.Contains(first, "invite-code") {
		t.Error("the digest contains the value it digests")
	}
	if got := len(first); got != len("sha256:")+64 {
		t.Errorf("digest has %d characters, want %d", got, len("sha256:")+64)
	}
}

func TestDigestIsCaseSensitive(t *testing.T) {
	t.Parallel()
	// Codes are folded to upper case by the caller; the digest itself must not silently fold, or two
	// different credentials would collide.
	if secret.Digest("ABC") == secret.Digest("abc") {
		t.Error("the digest folds case")
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
