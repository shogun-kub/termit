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

// TestHysteriaLinkRoundTrip: the link is what a participant actually pastes into a client, so what we
// build has to read back to the same node, the same user and the same label.
func TestHysteriaLinkRoundTrip(t *testing.T) {
	t.Parallel()
	const uuid = "11111111-2222-4333-8444-555555555555"
	raw, err := proto.BuildHysteriaURL("Node.Example.COM.", uuid, "my phone")
	if err != nil {
		t.Fatalf("BuildHysteriaURL: %v", err)
	}
	if !strings.HasPrefix(raw, "hy2://") {
		t.Fatalf("scheme: %q", raw)
	}
	link, err := proto.ParseHysteriaURL(raw)
	if err != nil {
		t.Fatalf("ParseHysteriaURL: %v", err)
	}
	if link.Domain != "node.example.com" {
		t.Errorf("domain = %q", link.Domain)
	}
	if link.User != proto.HysteriaUser(uuid) {
		t.Errorf("user = %q, want %q", link.User, proto.HysteriaUser(uuid))
	}
	if link.Label != "my phone" {
		t.Errorf("label = %q", link.Label)
	}
	// The password travels in the authority, not the query: a client that reads the URI the way hysteria
	// documents it must find it there.
	if !strings.Contains(raw, "@node.example.com:443?") {
		t.Errorf("the authority is not host:port with the password in front: %q", raw)
	}
	if _, err := proto.BuildHysteriaURL("not a domain", uuid, ""); err == nil {
		t.Error("BuildHysteriaURL accepted a string that is not a domain")
	}
	if _, err := proto.ParseHysteriaURL("vless://x@y:443"); err == nil {
		t.Error("ParseHysteriaURL accepted a vless link")
	}
}
