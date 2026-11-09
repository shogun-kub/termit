// Package secret generates and verifies the management server's credentials.
//
// Everything a client presents — an agent key, a subscription token, an invite code, an admin token —
// is stored only as an Argon2id hash, so a stolen database copy does not hand out the mesh. The same
// package generates the values, because the encoding and the hash must agree about what is compared.
package secret

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// Length is the number of random bytes in a generated credential.
	Length = 32

	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// ErrMismatch reports a value that does not match the stored hash.
var ErrMismatch = errors.New("secret: value does not match")

// Hash returns an encoded Argon2id hash of value, salt included. The format is
// "argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>" so a future parameter change can be detected.
func Hash(value string) (string, error) {
	if value == "" {
		return "", errors.New("secret: refusing to hash an empty value")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("secret: read salt: %w", err)
	}
	sum := argon2.IDKey([]byte(value), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// Verify reports whether value matches encoded.
func Verify(encoded, value string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return errors.New("secret: the stored hash is not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil || version != argon2.Version {
		return fmt.Errorf("secret: unsupported argon2 version %q", parts[1])
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return fmt.Errorf("secret: unreadable argon2 parameters %q", parts[2])
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return fmt.Errorf("secret: unreadable salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("secret: unreadable hash: %w", err)
	}
	got := argon2.IDKey([]byte(value), salt, time, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
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
