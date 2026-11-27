package mesh

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
)

func TestThePullTokenFollowsTheMasterKey(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("fedcba9876543210fedcba9876543210")

	if PullToken(key) != PullToken(key) {
		t.Fatal("the same key must derive the same token")
	}
	if PullToken(key) == PullToken(other) {
		t.Fatal("two keys must not share a token")
	}
	if !CheckPullToken(key, PullToken(key)) {
		t.Error("the derived token must be accepted")
	}
	for _, presented := range []string{"", PullToken(other), strings.ToUpper(PullToken(key))} {
		if CheckPullToken(key, presented) {
			t.Errorf("CheckPullToken accepted %q", presented)
		}
	}
}

func TestThePinIsTheCertificate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)

	good, err := ClientFor(config.Peer{Name: "peer", URL: srv.URL, LeafSHA256: hex.EncodeToString(sum[:])},
		time.Second)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	response, err := good.Get(srv.URL)
	if err != nil {
		t.Fatalf("the pinned client was refused: %v", err)
	}
	_ = response.Body.Close()

	// A pin of the right shape for another certificate is refused: the pin, not the chain, is the check.
	other := sha256.Sum256([]byte("another certificate"))
	wrong, err := ClientFor(config.Peer{Name: "peer", URL: srv.URL, LeafSHA256: hex.EncodeToString(other[:])},
		time.Second)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if _, err := wrong.Get(srv.URL); err == nil {
		t.Fatal("a client pinned to another certificate reached the peer")
	}
}

func TestAPinThatIsNotAHashIsRefused(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"not-hex", "abcd", strings.Repeat("00", 31)} {
		if _, err := ClientFor(config.Peer{Name: "peer", LeafSHA256: raw}, time.Second); err == nil {
			t.Errorf("ClientFor accepted the pin %q", raw)
		}
	}
	// Colons and a sha256: prefix are what an operator copies from openssl, so they are accepted.
	digest := bytes.Repeat([]byte{0x1f}, sha256.Size)
	raw := "sha256:" + strings.Join(splitHex(hex.EncodeToString(digest)), ":")
	if _, err := ClientFor(config.Peer{Name: "peer", LeafSHA256: raw}, time.Second); err != nil {
		t.Errorf("ClientFor refused %q: %v", raw, err)
	}
}

// splitHex splits a hex string into byte-sized pieces, the way a fingerprint is printed.
func splitHex(s string) []string {
	var out []string
	for i := 0; i < len(s); i += 2 {
		out = append(out, s[i:i+2])
	}
	return out
}
