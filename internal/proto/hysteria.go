package proto

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// hysteriaDomain separates this derivation from every other use of the same UUID, so a value from one
// place cannot be mistaken for a value from another.
const hysteriaDomain = "termit/hysteria2/"

// HysteriaPort is the UDP port a node's transport listens on. It is also the HTTPS port, which is what
// makes Alt-Svc able to point a browser at it.
const HysteriaPort = 443

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

// HysteriaPassword is the password a pair presents to a node's QUIC transport. It is a one-way derivation
// of the pair's UUID, which both sides already hold: nothing new is stored, and the credential epoch does
// not move. The accepted trade is that whoever holds a subscription holds both access links to that node
// (IMPLEMENTATION_PLAN.md §10.3).
//
// The output is unpadded base64url: no colon, so it cannot break the "user:password" string the server
// splits on, and safe inside a URI.
func HysteriaPassword(uuid string) string {
	sum := sha256.Sum256([]byte(hysteriaDomain + strings.ToLower(strings.TrimSpace(uuid))))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// BuildHysteriaURL renders the link a client imports: hy2://user:password@domain:port?sni=domain#label.
// The user name and the password come from the pair's UUID, so the same UUID produces the same link on the
// server and on the node.
func BuildHysteriaURL(domain, uuid, label string) (string, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if domain == "" || strings.ContainsAny(domain, " /?#") {
		return "", fmt.Errorf("proto: %q is not a domain", domain)
	}
	query := url.Values{}
	// The certificate is the node's own; a client that checks the name has to be told which name.
	query.Set("sni", domain)
	uri := url.URL{
		Scheme:   "hy2",
		User:     url.UserPassword(HysteriaUser(uuid), HysteriaPassword(uuid)),
		Host:     net.JoinHostPort(domain, strconv.Itoa(HysteriaPort)),
		RawQuery: query.Encode(),
		Fragment: label,
	}
	return uri.String(), nil
}

// HysteriaLink is a link read back: the node it points at, the user name it presents, and its label.
type HysteriaLink struct {
	Domain string
	User   string
	Label  string
}

// ParseHysteriaURL reads a link back, so a round trip is testable and a link a client would refuse can be
// recognised before it is handed out.
func ParseHysteriaURL(raw string) (HysteriaLink, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return HysteriaLink{}, fmt.Errorf("proto: %w", err)
	}
	if parsed.Scheme != "hy2" {
		return HysteriaLink{}, fmt.Errorf("proto: %q is not a hy2 link", parsed.Scheme)
	}
	link := HysteriaLink{Domain: parsed.Hostname(), Label: parsed.Fragment}
	if link.Domain == "" {
		return HysteriaLink{}, errors.New("proto: the link has no host")
	}
	if parsed.User != nil {
		link.User = parsed.User.Username()
	}
	if link.User == "" {
		return HysteriaLink{}, errors.New("proto: the link carries no user")
	}
	return link, nil
}
