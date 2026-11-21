package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestPinVerifierAcceptsOnlyThePinnedLeaf: the QUIC probe pins by the hash of the leaf, the same way the
// rest of the project does. Anything else must be refused, because it is the only thing standing between
// a private CA and a probe that trusts whoever answers.
func TestPinVerifierAcceptsOnlyThePinnedLeaf(t *testing.T) {
	t.Parallel()
	leaf := []byte("a certificate, in DER, as far as this function is concerned")
	sum := sha256.Sum256(leaf)
	pinned := hex.EncodeToString(sum[:])

	if err := pinVerifier(pinned)([][]byte{leaf}, nil); err != nil {
		t.Fatalf("the pinned leaf was refused: %v", err)
	}
	if err := pinVerifier(strings.Repeat("ab", 32))([][]byte{leaf}, nil); err == nil {
		t.Fatal("a certificate the server has not pinned was accepted")
	}
	if err := pinVerifier(pinned)(nil, nil); err == nil {
		t.Fatal("a node that presented no certificate was accepted")
	}
}
