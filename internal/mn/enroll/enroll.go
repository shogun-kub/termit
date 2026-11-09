// Package enroll decides whether a node joins the mesh.
//
// The order of the checks is the contract: the invite is validated but not consumed, the domain must
// resolve to the address the request came from, the node must answer as a proxy, and only then are the
// invite and the record written in one transaction. A caller can repeat a failed enrollment without
// burning an invite, and a node cannot exist in the registry without a credential it can use.
package enroll

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// Target is one node as the prober sees it.
type Target struct {
	Domain string
	Path   string
	UUID   string
	// Pin is the leaf certificate fingerprint the probe must accept and nothing else.
	Pin string
}

// Pin is what the certificate check produced: the identity the tunnel probe will insist on.
type Pin struct {
	Fingerprint string
	NotAfter    time.Time
}

// CertChecker reads the certificate a node presents.
type CertChecker interface {
	Check(ctx context.Context, domain string) (Pin, error)
}

// Outcome is one probe result. Class is empty on success; ClassUnknown means the prober could not do
// its job, which is never a failure of the node.
type Outcome struct {
	Class   string
	Unknown bool
	Err     error
}

// Prober checks a node end to end.
type Prober interface {
	Probe(ctx context.Context, target Target) Outcome
}

// Resolver answers a domain's addresses. Two of them are used, because one resolver's answer is one
// resolver's mistake.
type Resolver interface {
	Lookup(ctx context.Context, domain string) ([]net.IP, error)
}

// Options wire a Service.
type Options struct {
	Store  *store.Store
	Mode   string
	Policy config.Policy
	// Resolvers are consulted in order; every one that answers must agree with the source address.
	Resolvers []Resolver
	Prober    Prober
	Certs     CertChecker
	Now       func() time.Time
	// NewID returns a node id; tests set it.
	NewID func() string
}

// Service implements enrollment.
type Service struct {
	opts Options
}

// New validates the wiring.
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("enroll: a store is required")
	}
	if len(opts.Resolvers) == 0 {
		return nil, errors.New("enroll: at least one resolver is required")
	}
	if opts.Prober == nil {
		return nil, errors.New("enroll: a prober is required")
	}
	if opts.Certs == nil {
		return nil, errors.New("enroll: a certificate checker is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = randomID
	}
	switch opts.Mode {
	case config.ModeInvite, config.ModeInviteApproval, config.ModeOpen:
	default:
		return nil, fmt.Errorf("enroll: %q is not an enrollment mode", opts.Mode)
	}
	return &Service{opts: opts}, nil
}

// Error is a refusal with a stable problem code and the HTTP status it maps to.
type Error struct {
	Status int
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func refuse(status int, code, detail string) *Error {
	return &Error{Status: status, Code: code, Detail: detail}
}

// Result carries the created node and the secrets to hand back exactly once.
type Result struct {
	UV      store.UV
	Secrets proto.EnrollResponse
}

// Enroll validates req, checks the node and creates it. sourceIP is the address the request came
// from: the DNS check exists to prove it is the node's own address.
func (s *Service) Enroll(ctx context.Context, req proto.EnrollRequest, sourceIP string, agentVersion string) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, refuse(400, proto.CodeURLInvalid, err.Error())
	}
	if !proto.ValidProfile(req.Profile) {
		return Result{}, refuse(400, proto.CodeUnsupported,
			fmt.Sprintf("profile %q is not supported; this server speaks %s", req.Profile, proto.ProfileV1))
	}
	profile, err := proto.ParseVLESSUrl(req.BasicVLESSURL)
	if err != nil {
		return Result{}, refuse(400, proto.CodeURLInvalid, err.Error())
	}
	if !strings.EqualFold(profile.Domain, req.Domain) {
		// A URL for another host would make every peer dial the wrong name.
		return Result{}, refuse(400, proto.CodeDomainInvalid,
			fmt.Sprintf("basic_vless_url names %q, the request says %q", profile.Domain, req.Domain))
	}

	invite, err := s.validateInvite(ctx, req.InviteCode)
	if err != nil {
		return Result{}, err
	}
	if _, err := s.opts.Store.UVByDomain(ctx, strings.ToLower(req.Domain)); err == nil {
		return Result{}, refuse(409, proto.CodeDomainTaken, "this domain is already registered")
	} else if !errors.Is(err, store.ErrNotFound) {
		return Result{}, err
	}

	if err := s.checkDNS(ctx, req.Domain, sourceIP); err != nil {
		return Result{}, err
	}
	if err := s.checkNode(ctx, profile); err != nil {
		return Result{}, err
	}

	now := s.opts.Now().UTC()
	state := store.StateActive
	notice := ""
	if s.opts.Mode == config.ModeInviteApproval {
		// The design asks for approval in this mode, and a pending node is invisible to peers, so it
		// cannot route anybody's traffic before an operator says yes.
		state = store.StatePending
		notice = "enrolled; an operator has to approve this node before it joins the mesh"
	}

	uv := store.UV{
		ID:              s.opts.NewID(),
		Domain:          strings.ToLower(req.Domain),
		SelfUUID:        profile.UUID,
		ProfileJSON:     string(proto.ProfileV1),
		State:           state,
		StateReason:     "enrolled from " + sourceIP,
		CreatedAt:       now,
		LastSeenAt:      &now,
		LastRequestIP:   sourceIP,
		AgentVersion:    agentVersion,
		CredentialEpoch: 1,
		InviteID:        invite.ID,
	}
	if state == store.StateActive {
		uv.ApprovedAt = &now
	}
	uv.ProbeUUID = newUUID()
	agentKey, err := secret.Token()
	if err != nil {
		return Result{}, err
	}
	subToken, err := secret.Token()
	if err != nil {
		return Result{}, err
	}
	uv.AgentKeyHash = secret.Digest(agentKey)
	uv.SubTokenHash = secret.Digest(subToken)

	if err := s.opts.Store.CreateUVRedeemingInvite(ctx, uv, invite, now); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return Result{}, refuse(409, proto.CodeDomainTaken, "this domain is already registered")
		}
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, refuse(400, proto.CodeInviteInvalid, "the invite code is no longer valid")
		}
		return Result{}, err
	}

	if err := s.opts.Store.AppendAudit(ctx, store.AuditRecord{
		At: now, Actor: "agent", Action: "uv.enroll", Target: uv.ID,
		DetailJSON: fmt.Sprintf(`{"domain":%q,"state":%q,"invite":%q}`, uv.Domain, uv.State, invite.ID),
	}); err != nil {
		return Result{}, err
	}

	return Result{
		UV: uv,
		Secrets: proto.EnrollResponse{
			APIVersion:      proto.APIVersion,
			Node:            proto.Node{ID: uv.ID, State: string(uv.State), Domain: uv.Domain},
			AgentKey:        agentKey,
			SubToken:        subToken,
			ProbeUUID:       uv.ProbeUUID,
			SubscriptionURL: "/sub/" + subToken,
			Notice:          notice,
		},
	}, nil
}

// validateInvite resolves the code without consuming it. An open server still accepts an invite when
// one is offered: that is how an operator migrates from open to invited without a flag day.
func (s *Service) validateInvite(ctx context.Context, code string) (store.Invite, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		if s.opts.Mode == config.ModeOpen {
			return store.Invite{}, nil
		}
		return store.Invite{}, refuse(400, proto.CodeInviteInvalid, "an invite code is required")
	}
	// The digest is deterministic: an invite has to be found by the code the owner typed.
	invite, err := s.opts.Store.InviteByCodeHash(ctx, digestCode(code))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Invite{}, refuse(400, proto.CodeInviteInvalid, "the invite code is not valid")
		}
		return store.Invite{}, err
	}
	if !invite.Usable(s.opts.Now().UTC()) {
		if invite.RevokedAt != nil {
			return store.Invite{}, refuse(400, proto.CodeInviteInvalid, "the invite code was revoked")
		}
		if invite.MaxUses > 0 && invite.UsedCount >= invite.MaxUses {
			return store.Invite{}, refuse(400, proto.CodeInviteExhausted, "the invite code has no uses left")
		}
		return store.Invite{}, refuse(400, proto.CodeInviteInvalid, "the invite code expired")
	}
	return invite, nil
}

// checkDNS requires every resolver that answers to place the node on sourceIP.
func (s *Service) checkDNS(ctx context.Context, domain, sourceIP string) error {
	want := net.ParseIP(sourceIP)
	if want == nil {
		return refuse(400, proto.CodeDNSMismatch, "the request carries no usable source address")
	}
	answered := 0
	for _, resolver := range s.opts.Resolvers {
		ips, err := resolver.Lookup(ctx, domain)
		if err != nil {
			// A resolver that cannot answer is not evidence against the node.
			continue
		}
		answered++
		if !containsIP(ips, want) {
			return refuse(409, proto.CodeDNSMismatch,
				fmt.Sprintf("%s does not resolve to %s", domain, sourceIP))
		}
	}
	if answered == 0 {
		return refuse(503, proto.CodeDNSMismatch, "no resolver could answer; retry when DNS is available")
	}
	return nil
}

// checkNode reads the node's certificate, then probes the tunnel pinned to exactly that certificate.
// The probe credential exists but the node learns it only in the response, so enrollment uses the self
// credential the URL already carries.
func (s *Service) checkNode(ctx context.Context, profile proto.Profile) error {
	pin, err := s.opts.Certs.Check(ctx, profile.Domain)
	if err != nil {
		return refuse(502, proto.CodeCheckFailed, err.Error())
	}
	outcome := s.opts.Prober.Probe(ctx, Target{
		Domain: profile.Domain,
		Path:   profile.Path,
		UUID:   profile.UUID,
		Pin:    pin.Fingerprint,
	})
	if outcome.Unknown {
		return refuse(503, proto.CodeCheckFailed, "the prober could not check this node; retry")
	}
	if outcome.Err != nil {
		return refuse(502, proto.CodeCheckFailed, outcome.Err.Error())
	}
	if outcome.Class != "" {
		return refuse(502, proto.CodeCheckFailed, "the node did not answer as a proxy: "+outcome.Class)
	}
	return nil
}

// digestCode normalises the code the way the owner reads it back: grouped, upper case, and with the
// spaces a chat window added.
func digestCode(code string) string {
	return secret.Digest(strings.ToUpper(strings.TrimSpace(code)))
}

func containsIP(ips []net.IP, want net.IP) bool {
	for _, ip := range ips {
		if ip.Equal(want) {
			return true
		}
	}
	return false
}

func randomID() string {
	token, err := secret.Token()
	if err != nil {
		return "uv_fallback"
	}
	return "uv_" + token[:12]
}

func newUUID() string {
	token, err := secret.Token()
	if err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	raw := []byte(token)[:16]
	// Version 4 and the RFC 4122 variant, so the value is a UUID a client will accept.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
