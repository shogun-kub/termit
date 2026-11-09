// Package proto holds the wire contract between a node and the management server: request and
// response bodies, and the stable problem codes both sides branch on.
//
// Nothing here may carry a secret in a field name that ends up in a log: the agent key and the
// subscription token travel in headers and URLs, never in a response body that a proxy might record.
package proto

import (
	"fmt"
	"strings"
)

// APIVersion is the protocol version this build speaks.
const APIVersion = "1"

// ProfileV1 is the only transport profile: VLESS + XHTTP + TLS through nginx.
const ProfileV1 = "v1"

// Problem codes. A client branches on these, never on the message text.
const (
	CodeInviteInvalid   = "invite_invalid"
	CodeInviteExhausted = "invite_exhausted"
	CodeURLInvalid      = "url_invalid"
	CodeDomainInvalid   = "domain_invalid"
	CodeDomainTaken     = "domain_taken"
	CodeDNSMismatch     = "dns_mismatch"
	CodeCheckFailed     = "check_failed"
	CodePendingApproval = "pending_approval"
	CodeRateLimited     = "rate_limited"
	CodeUnsupported     = "unsupported_profile"
	CodeUnauthorized    = "unauthorized"
	CodeConflict        = "conflict"
	// CodeNodeInactive is answered to a blocked or removed node on any endpoint but unblock.
	CodeNodeInactive = "node_inactive"
	// CodeRelocateRequired is what an unblock answers when the supplied URL names a different host:
	// moving a node to a new domain is Scenario C, not Scenario B.
	CodeRelocateRequired = "relocate_required"
	CodeInternal         = "internal_error"
)

// EnrollRequest is what `POST /api/v1/enroll` accepts.
type EnrollRequest struct {
	InviteCode    string   `json:"invite_code"`
	Domain        string   `json:"domain"`
	BasicVLESSURL string   `json:"basic_vless_url"`
	AgentVersion  string   `json:"agent_version"`
	Profile       string   `json:"profile"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

// Validate checks the parts that need no network.
func (r EnrollRequest) Validate() error {
	if r.Domain == "" {
		return fmt.Errorf("domain is required")
	}
	if r.BasicVLESSURL == "" {
		return fmt.Errorf("basic_vless_url is required")
	}
	if r.Profile != "" && r.Profile != ProfileV1 {
		return fmt.Errorf("profile %q is not supported; this server speaks %s", r.Profile, ProfileV1)
	}
	if len(r.AgentVersion) > 64 {
		return fmt.Errorf("agent_version is longer than 64 characters")
	}
	return nil
}

// Node is the node identity a client learns about itself.
type Node struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Domain is echoed so a client can confirm what the server recorded.
	Domain string `json:"domain"`
}

// EnrollResponse is returned once, at enrollment. The three secrets in it are shown to the node and
// never again: MN stores only hashes.
type EnrollResponse struct {
	APIVersion string `json:"api_version"`
	Node       Node   `json:"node"`
	AgentKey   string `json:"agent_key"`
	SubToken   string `json:"sub_token"`
	ProbeUUID  string `json:"probe_uuid"`
	// SubscriptionURL is where the owner (or a client) fetches the peer list.
	SubscriptionURL string `json:"subscription_url"`
	// Notice explains a non-active state without leaking anything: "pending approval" is a fact, the
	// approval queue position is not.
	Notice string `json:"notice,omitempty"`
}

// UnblockRequest is `POST /api/v1/agent/unblock`. Both fields are optional: a node that still has
// its old identity sends nothing, and a rebuilt one sends the URL it now answers on.
type UnblockRequest struct {
	Domain        string `json:"domain,omitempty"`
	BasicVLESSURL string `json:"basic_vless_url,omitempty"`
}

// UnblockResponse confirms the state after a successful request, or explains that nothing changed.
type UnblockResponse struct {
	APIVersion string `json:"api_version"`
	Node       Node   `json:"node"`
	Notice     string `json:"notice,omitempty"`
}

// Problem is the RFC 9457-style error body.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
	// APIVersion is present on errors too: a client that cannot parse a success body still learns
	// which server it is talking to.
	APIVersion string `json:"api_version"`
}

// NewProblem builds a problem body. detail is for humans; it must not contain a secret.
func NewProblem(status int, code, title, detail string) Problem {
	return Problem{
		Type:       "about:blank",
		Title:      title,
		Status:     status,
		Code:       code,
		Detail:     detail,
		APIVersion: APIVersion,
	}
}

// ValidProfile reports whether profile is one this build understands, treating the empty string as
// the current version: an older agent that does not send the field is not an incompatible agent.
func ValidProfile(profile string) bool {
	return profile == "" || strings.EqualFold(profile, ProfileV1)
}
