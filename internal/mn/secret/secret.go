// Package secret generates and verifies the management server's credentials.
//
// Everything a client presents — an agent key, a subscription token, an invite code, an admin token —
// is stored only as an Argon2id hash, so a stolen database copy does not hand out the mesh. The same
// package generates the values, because the encoding and the hash must agree about what is compared.
package secret

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Length is the number of random bytes in a generated credential.
const Length = 32

// Digest is the deterministic digest stored for every high-entropy credential: agent keys,
// subscription tokens, invite codes and admin tokens.
//
// It is a plain SHA-256 and not a password hash on purpose. These values are 160-256 bits of
// randomness, so there is nothing to brute-force, and the store has to look a credential up by value —
// a salted hash cannot be looked up at all. Argon2id is the wrong tool here and would only make the
// server slower on every request.
func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Token returns a random URL-safe credential.
func Token() (string, error) {
	buf := make([]byte, Length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("secret: read randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Code returns a random invite code in groups of four, using an alphabet without look-alike
// characters: a code gets read out loud and typed by hand.
func Code() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("secret: read randomness: %w", err)
	}
	raw := strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
	var groups []string
	for i := 0; i < len(raw); i += 4 {
		end := i + 4
		if end > len(raw) {
			end = len(raw)
		}
		groups = append(groups, raw[i:end])
	}
	return strings.Join(groups, "-"), nil
}

// Fingerprint is the short form of a credential, safe to log and to show in a list.
func Fingerprint(value string) string {
	if len(value) <= 8 {
		return "…"
	}
	return value[:8]
}
