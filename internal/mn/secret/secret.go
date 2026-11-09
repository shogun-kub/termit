// Package secret generates and verifies the management server's credentials.
//
// Everything a client presents — an agent key, a subscription token, an invite code, an admin token —
// is stored only as an Argon2id hash, so a stolen database copy does not hand out the mesh. The same
// package generates the values, because the encoding and the hash must agree about what is compared.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

// Seal encrypts a value that has to be readable again — the subscription token, which MN must be able
// to put back into a URL — under a key derived from the master key.
//
// It is not a substitute for the digest: the digest is the index, the sealed copy is the value behind
// it. A stolen database without the master key yields neither.
func Seal(master []byte, plaintext string) (string, error) {
	key, err := deriveKey(master)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("secret: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secret: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret: nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return "aead:" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value written by Seal.
func Open(master []byte, sealed string) (string, error) {
	if !strings.HasPrefix(sealed, "aead:") {
		return "", errors.New("secret: the value is not sealed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, "aead:"))
	if err != nil {
		return "", fmt.Errorf("secret: decode the sealed value: %w", err)
	}
	key, err := deriveKey(master)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("secret: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("secret: gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("secret: the sealed value is truncated")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("secret: the sealed value does not open: %w", err)
	}
	return string(plain), nil
}

// deriveKey turns the master key into an AES-256 key for this purpose alone.
func deriveKey(master []byte) ([]byte, error) {
	if len(master) < 32 {
		return nil, errors.New("secret: the master key must be at least 32 bytes")
	}
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("termit/sealed-value/v1"))
	return mac.Sum(nil), nil
}
