// Package admin is the operator interface: a request/response protocol over a unix socket, and the
// service behind it.
//
// It is not HTTP on purpose. The public HTTPS surface stays two endpoints wide — enrollment for an
// invitee, everything else behind an agent key — and an operator sitting on the box talks to a socket
// that only root can read. That is a shorter list of things to get wrong than an authenticated admin
// API reachable from the internet.
package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// Request is one operation.
type Request struct {
	// Token is the admin token printed by `mn init`. It travels over the socket, never over the wire
	// of the public API.
	Token string          `json:"token"`
	Op    string          `json:"op"`
	Args  json.RawMessage `json:"args,omitempty"`
}

// Response is the reply. Exactly one of Result and Error is set.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a refusal the CLI prints and exits non-zero on.
type Error struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// Codes a caller can branch on.
const (
	CodeUnauthorized = "unauthorized"
	CodeBadRequest   = "bad_request"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeInternal     = "internal_error"
)

// Service executes operations against the registry.
type Service struct {
	Store *store.Store
	// Now is the clock; tests set it.
	Now func() time.Time
	// NewID returns an invite id; tests set it.
	NewID func() string
	// NewCode returns an invite code; tests set it.
	NewCode func() (string, error)
	// Master seals the subscription token so the CLI can print a usable URL after a rotation.
	Master []byte
}

// New validates the wiring.
func New(db *store.Store) (*Service, error) {
	if db == nil {
		return nil, errors.New("admin: a store is required")
	}
	return &Service{Store: db, Now: time.Now, NewID: randomInviteID, NewCode: secret.Code}, nil
}

// Handle authenticates and dispatches one request.
func (s *Service) Handle(ctx context.Context, req Request) (any, *Error) {
	if err := s.authenticate(ctx, req.Token); err != nil {
		return nil, err
	}
	switch req.Op {
	case "invite.list":
		return s.listInvites(ctx)
	case "invite.create":
		return s.createInvite(ctx, req.Args)
	case "invite.revoke":
		return s.revokeInvite(ctx, req.Args)
	case "uv.list":
		return s.listUVs(ctx)
	case "uv.show":
		return s.showUV(ctx, req.Args)
	case "uv.approve":
		return s.setUVState(ctx, req.Args, store.StateActive, "approved by an operator")
	case "uv.reject":
		return s.setUVState(ctx, req.Args, store.StateRemoved, "rejected by an operator")
	case "uv.reissue":
		return s.reissueAgentKey(ctx, req.Args)
	case "uv.agentkey":
		return s.showAgentKey(ctx, req.Args)
	case "sub.rotate":
		return s.rotateSubToken(ctx, req.Args)
	default:
		return nil, &Error{Code: CodeBadRequest, Detail: fmt.Sprintf("unknown operation %q", req.Op)}
	}
}

// authenticate compares the token against every stored digest. The registry holds a handful of admin
// tokens, so a scan is cheaper than an index and cannot leak which prefix is right.
func (s *Service) authenticate(ctx context.Context, token string) *Error {
	if token == "" {
		return &Error{Code: CodeUnauthorized, Detail: "an admin token is required"}
	}
	digest := secret.Digest(token)
	admins, err := s.Store.Admins(ctx)
	if err != nil {
		return &Error{Code: CodeInternal, Detail: "cannot read the admin list"}
	}
	for _, admin := range admins {
		if admin.TokenHash == digest {
			now := s.Now().UTC()
			if err := s.Store.TouchAdmin(ctx, admin.ID, now); err != nil {
				// A failed bookkeeping write must not fail the operation.
				_ = err
			}
			return nil
		}
	}
	return &Error{Code: CodeUnauthorized, Detail: "the admin token is not valid"}
}

// InviteView is an invite as the CLI prints it: the code itself is not recoverable, only its use.
type InviteView struct {
	ID        string     `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	MaxUses   int        `json:"max_uses"`
	UsedCount int        `json:"used_count"`
	Note      string     `json:"note,omitempty"`
	Revoked   bool       `json:"revoked"`
	Usable    bool       `json:"usable"`
}

func (s *Service) listInvites(ctx context.Context) (any, *Error) {
	invites, err := s.Store.ListInvites(ctx)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	now := s.Now().UTC()
	out := make([]InviteView, 0, len(invites))
	for _, invite := range invites {
		out = append(out, InviteView{
			ID:        invite.ID,
			CreatedAt: invite.CreatedAt,
			CreatedBy: invite.CreatedBy,
			ExpiresAt: invite.ExpiresAt,
			MaxUses:   invite.MaxUses,
			UsedCount: invite.UsedCount,
			Note:      invite.Note,
			Revoked:   invite.RevokedAt != nil,
			Usable:    invite.Usable(now),
		})
	}
	return out, nil
}

type createInviteArgs struct {
	Uses         int    `json:"uses"`
	Note         string `json:"note"`
	ExpiresHours int    `json:"expires_hours"`
}

type createInviteResult struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

func (s *Service) createInvite(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args createInviteArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &Error{Code: CodeBadRequest, Detail: "unreadable arguments: " + err.Error()}
		}
	}
	if args.Uses <= 0 {
		args.Uses = 1
	}
	if args.Uses > 100 {
		return nil, &Error{Code: CodeBadRequest, Detail: "an invite with more than 100 uses is a policy, not a code"}
	}
	code, err := s.NewCode()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	now := s.Now().UTC()
	invite := store.Invite{
		ID:        s.NewID(),
		CodeHash:  secret.Digest(code),
		CreatedAt: now,
		CreatedBy: "admin cli",
		MaxUses:   args.Uses,
		Note:      args.Note,
	}
	if args.ExpiresHours > 0 {
		expires := now.Add(time.Duration(args.ExpiresHours) * time.Hour)
		invite.ExpiresAt = &expires
	}
	if err := s.Store.CreateInvite(ctx, invite); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	if err := s.Store.AppendAudit(ctx, store.AuditRecord{
		At: now, Actor: "admin", Action: "invite.create", Target: invite.ID,
		DetailJSON: fmt.Sprintf(`{"uses":%d,"expires_hours":%d}`, args.Uses, args.ExpiresHours),
	}); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	return createInviteResult{ID: invite.ID, Code: code}, nil
}

type inviteIDArgs struct {
	ID string `json:"id"`
}

func (s *Service) revokeInvite(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args inviteIDArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "an invite id is required"}
	}
	now := s.Now().UTC()
	if err := s.Store.RevokeInvite(ctx, args.ID, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &Error{Code: CodeNotFound, Detail: "no such invite"}
		}
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	return map[string]bool{"revoked": true}, nil
}

// NodeView is a node as the CLI prints it, with secrets reduced to their presence.
type NodeView struct {
	ID              string     `json:"id"`
	Label           string     `json:"label,omitempty"`
	Domain          string     `json:"domain"`
	State           string     `json:"state"`
	StateReason     string     `json:"state_reason,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	LastRequestIP   string     `json:"last_request_ip,omitempty"`
	AgentVersion    string     `json:"agent_version,omitempty"`
	FailedCycles    int        `json:"failed_cycles"`
	CredentialEpoch int        `json:"credential_epoch"`
	HasAgentKey     bool       `json:"has_agent_key"`
	HasSubToken     bool       `json:"has_sub_token"`
}

func view(uv store.UV) NodeView {
	return NodeView{
		ID: uv.ID, Label: uv.Label, Domain: uv.Domain, State: string(uv.State),
		StateReason: uv.StateReason, CreatedAt: uv.CreatedAt, ApprovedAt: uv.ApprovedAt,
		LastSeenAt: uv.LastSeenAt, LastRequestIP: uv.LastRequestIP, AgentVersion: uv.AgentVersion,
		FailedCycles: uv.FailedCycles, CredentialEpoch: uv.CredentialEpoch,
		HasAgentKey: uv.AgentKeyHash != "", HasSubToken: uv.SubTokenHash != "",
	}
}

func (s *Service) listUVs(ctx context.Context) (any, *Error) {
	nodes, err := s.Store.ListUVs(ctx, store.UVFilter{IncludeRemoved: true})
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	out := make([]NodeView, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, view(node))
	}
	return out, nil
}

type uvShowResult struct {
	Node   NodeView      `json:"node"`
	Events []store.Event `json:"events"`
	Check  *store.Check  `json:"check,omitempty"`
}

func (s *Service) showUV(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args inviteIDArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a node id or domain is required"}
	}
	node, refused := s.resolveUV(ctx, args.ID)
	if refused != nil {
		return nil, refused
	}
	events, err := s.Store.UVEvents(ctx, node.ID, 20)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	result := uvShowResult{Node: view(node), Events: events}
	if check, err := s.Store.LatestCheck(ctx, node.ID, "probe"); err == nil {
		result.Check = &check
	}
	return result, nil
}

// resolveUV accepts either a node id or a domain, because an operator reads domains and copies ids.
func (s *Service) resolveUV(ctx context.Context, selector string) (store.UV, *Error) {
	node, err := s.Store.UVByID(ctx, selector)
	if err == nil {
		return node, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.UV{}, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	node, err = s.Store.UVByDomain(ctx, selector)
	if err == nil {
		return node, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return store.UV{}, &Error{Code: CodeNotFound, Detail: fmt.Sprintf("no node %q", selector)}
	}
	return store.UV{}, &Error{Code: CodeInternal, Detail: err.Error()}
}

func (s *Service) setUVState(ctx context.Context, raw json.RawMessage, to store.State, reason string) (any, *Error) {
	var args inviteIDArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a node id or domain is required"}
	}
	node, refused := s.resolveUV(ctx, args.ID)
	if refused != nil {
		return nil, refused
	}
	if string(node.State) == string(to) {
		return map[string]any{"node": view(node), "changed": false}, nil
	}
	now := s.Now().UTC()
	event, err := s.Store.SetUVState(ctx, node.ID, to, reason, "admin", now)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	if to == store.StateActive {
		if err := s.Store.ApproveUV(ctx, node.ID, now); err != nil {
			return nil, &Error{Code: CodeInternal, Detail: err.Error()}
		}
	}
	if err := s.Store.AppendAudit(ctx, store.AuditRecord{
		At: now, Actor: "admin", Action: string(to), Target: node.ID,
		DetailJSON: fmt.Sprintf(`{"from":%q,"to":%q}`, event.FromState, event.ToState),
	}); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	updated, err := s.Store.UVByID(ctx, node.ID)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	return map[string]any{"node": view(updated), "changed": true}, nil
}

// rotateSubToken issues a new subscription token. The old one stops working immediately; the URL is
// returned once, because MN only keeps the digest.
func (s *Service) rotateSubToken(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args inviteIDArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a node id or domain is required"}
	}
	node, refused := s.resolveUV(ctx, args.ID)
	if refused != nil {
		return nil, refused
	}
	token, err := secret.Token()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	sealed, err := secret.Seal(s.Master, token)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	if err := s.Store.SetSubTokenHash(ctx, node.ID, secret.Digest(token), sealed); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	now := s.Now().UTC()
	if err := s.Store.AppendAudit(ctx, store.AuditRecord{
		At: now, Actor: "admin", Action: "sub.rotate", Target: node.ID,
	}); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	return map[string]string{"id": node.ID, "token": token}, nil
}

// reissueAgentKey gives a node a new agent key, which is what an owner who lost the old one needs. The
// old key stops working immediately, and the new one is shown once.
func (s *Service) reissueAgentKey(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args inviteIDArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a node id or domain is required"}
	}
	node, refused := s.resolveUV(ctx, args.ID)
	if refused != nil {
		return nil, refused
	}
	token, err := secret.Token()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	sealed, err := secret.Seal(s.Master, token)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	if err := s.Store.SetAgentKeyHash(ctx, node.ID, secret.Digest(token), sealed); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	now := s.Now().UTC()
	if err := s.Store.AppendAudit(ctx, store.AuditRecord{
		At: now, Actor: "admin", Action: "uv.reissue", Target: node.ID,
	}); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	return map[string]string{"id": node.ID, "agent_key": token}, nil
}

// showAgentKey returns the node's current key without rotating it. An owner who lost the key learns it
// again; a working node keeps working.
func (s *Service) showAgentKey(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args inviteIDArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.ID == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a node id or domain is required"}
	}
	node, refused := s.resolveUV(ctx, args.ID)
	if refused != nil {
		return nil, refused
	}
	if node.AgentKeySealed == "" {
		return nil, &Error{Code: CodeNotFound, Detail: "this node has no stored key: rotate it with `mn uv reissue`"}
	}
	key, err := secret.Open(s.Master, node.AgentKeySealed)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: "the stored key does not open: " + err.Error()}
	}
	return map[string]string{"id": node.ID, "agent_key": key}, nil
}

// Serve accepts requests on listener until ctx is cancelled. Each connection carries one request: the
// operator's CLI opens it, asks, and closes.
func Serve(ctx context.Context, listener net.Listener, svc *Service, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// A cancelled run must stop accepting; closing the listener is what unblocks Accept.
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("admin: accept: %w", err)
		}
		go handleConn(ctx, conn, svc, logger)
	}
}

func handleConn(ctx context.Context, conn net.Conn, svc *Service, logger *slog.Logger) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	reader := bufio.NewReader(conn)
	var req Request
	if err := json.NewDecoder(reader).Decode(&req); err != nil {
		writeResponse(conn, Response{Error: &Error{Code: CodeBadRequest, Detail: "unreadable request"}})
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	result, refused := svc.Handle(ctx, req)
	response := Response{}
	if refused != nil {
		response.Error = refused
	} else {
		encoded, err := json.Marshal(result)
		if err != nil {
			response.Error = &Error{Code: CodeInternal, Detail: "cannot encode the result"}
		} else {
			response.Result = encoded
		}
	}
	if refused != nil && refused.Code != CodeUnauthorized {
		logger.Info("admin operation refused", "op", req.Op, "code", refused.Code)
	}
	writeResponse(conn, response)
}

func writeResponse(conn net.Conn, response Response) {
	_ = json.NewEncoder(conn).Encode(response)
}

// Client calls the admin socket.
type Client struct {
	// Network and Address are "unix" and the socket path on Linux; tests use tcp.
	Network string
	Address string
	Token   string
	Timeout time.Duration
}

// Call performs one operation and decodes its result into out, which may be nil.
func (c Client) Call(ctx context.Context, op string, args any, out any) error {
	if c.Network == "" {
		c.Network = "unix"
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, c.Network, c.Address)
	if err != nil {
		return fmt.Errorf("admin: cannot reach the server at %s: %w", c.Address, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(c.Timeout))

	var encoded json.RawMessage
	if args != nil {
		encoded, err = json.Marshal(args)
		if err != nil {
			return fmt.Errorf("admin: encode arguments: %w", err)
		}
	}
	if err := json.NewEncoder(conn).Encode(Request{Token: c.Token, Op: op, Args: encoded}); err != nil {
		return fmt.Errorf("admin: send %s: %w", op, err)
	}

	// The server closes the connection after the response, so a decoder is enough.
	var response Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return fmt.Errorf("admin: read the reply to %s: %w", op, err)
	}
	if response.Error != nil {
		return response.Error
	}
	if out != nil && len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, out); err != nil {
			return fmt.Errorf("admin: decode the reply to %s: %w", op, err)
		}
	}
	return nil
}

func randomInviteID() string {
	token, err := secret.Token()
	if err != nil {
		return "inv_fallback"
	}
	return "inv_" + token[:10]
}
