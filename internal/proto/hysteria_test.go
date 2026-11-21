package proto_test

import (
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
)

// TestHysteriaPassword pins the derivation: the same UUID must produce the same password on a node and on
// the server that rendered the participant's link, and a change here breaks every existing link.
func TestHysteriaPassword(t *testing.T) {
	t.Parallel()
	const uuid = "11111111-2222-4333-8444-555555555555"
	const want = "yes0AelsVfhI-gVeY1WVm66sg1S4jskaN65K01cZezQ"
	if got := proto.HysteriaPassword(uuid); got != want {
		t.Fatalf("HysteriaPassword(%q) = %q, want %q", uuid, got, want)
	}
	// The server lowercases user names; a UUID that arrived in upper case must not produce a different
	// password than the same UUID in lower case.
	if got := proto.HysteriaPassword(strings.ToUpper(uuid)); got != want {
		t.Errorf("an upper-case UUID produced %q", got)
	}
	if got := proto.HysteriaPassword("11111111-2222-4333-8444-555555555556"); got == want {
		t.Error("two different UUIDs produced the same password")
	}
	// It travels inside "user:password" and inside a URI.
	if strings.ContainsAny(proto.HysteriaPassword(uuid), ":@/?# ") {
		t.Error("the password carries a character that would end it in a URI or an auth string")
	}
	// The user name is derived too: lower case, no colon, stable for the same UUID.
	if got := proto.HysteriaUser(uuid); got != "u11111111" {
		t.Errorf("HysteriaUser(%q) = %q, want u11111111", uuid, got)
	}
	if proto.HysteriaUser(strings.ToUpper(uuid)) != proto.HysteriaUser(uuid) {
		t.Error("the user name depends on the case of the UUID")
	}
}
