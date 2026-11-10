package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"os"
)

// CertFingerprint is the lowercase hex SHA-256 of the leaf certificate in path, ready for the
// server's probe pin. Empty means "could not read it"; a caller sends nothing rather than a zero
// fingerprint, and the server keeps the pin it has.
//
// The node reports its own leaf on every sync because the server pins the exact certificate. A
// renewal replaces that certificate; without the report every later check would close the tunnel on a
// pin mismatch, and the node would be blocked for having done the right thing.
func CertFingerprint(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a path from our own configuration.
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}
