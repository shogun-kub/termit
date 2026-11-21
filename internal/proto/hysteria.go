package proto

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// hysteriaDomain separates this derivation from every other use of the same UUID, so a value from one
// place cannot be mistaken for a value from another.
const hysteriaDomain = "termit/hysteria2/"

// HysteriaUser is the user name a pair presents, derived from the same UUID: both sides compute it, so
// nothing has to be passed around. The server lowercases user names before comparing them, and this one
// is lower case and colon-free to begin with.
func HysteriaUser(uuid string) string {
	compact := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(uuid)), "-", "")
	if len(compact) > 8 {
		compact = compact[:8]
	}
	return "u" + compact
}

// HysteriaPassword is the password a pair presents to a node's QUIC transport, with the pair's id as the
// user name. It is a one-way derivation of the pair's UUID, which both sides already hold: nothing new
// is stored, and the credential epoch does not move. The accepted trade is that whoever holds a
// subscription holds both access links to that node (IMPLEMENTATION_PLAN.md §10.3).
//
// The output is unpadded base64url: no colon, so it cannot break the "user:password" string the server
// splits on, and safe inside a URI.
func HysteriaPassword(uuid string) string {
	sum := sha256.Sum256([]byte(hysteriaDomain + strings.ToLower(strings.TrimSpace(uuid))))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
