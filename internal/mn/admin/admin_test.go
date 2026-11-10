package admin_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/admin"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

const testToken = "admin-token-for-tests"

// testMaster is what the service seals credentials with; it never leaves the process in production.
var testMaster = []byte("0123456789abcdef0123456789abcdef")

// start runs the service on a loopback TCP listener: the admin transport is a unix socket, but the
// service, the protocol and the client are what these tests cover, and TCP is the same stream.
func start(t *testing.T) (admin.Client, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.CreateAdmin(context.Background(), store.Admin{
		ID: "admin_1", Name: "operator", TokenHash: secret.Digest(testToken), CreatedAt: testNow,
	}); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	service, err := admin.New(db)
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	service.Now = func() time.Time { return testNow }
	service.Master = testMaster
	service.NewID = func() string { return "inv_test" }
	service.NewCode = func() (string, error) { return "AAAA-BBBB-CCCC", nil }

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = admin.Serve(ctx, listener, service, slog.New(slog.DiscardHandler)) }()
	t.Cleanup(func() { _ = listener.Close() })

	return admin.Client{Network: "tcp", Address: listener.Addr().String(), Token: testToken}, db
}

func call(ctx context.Context, client admin.Client, op string, args any, out any) error {
	return client.Call(ctx, op, args, out)
}

func TestUnauthorized(t *testing.T) {
	t.Parallel()
	client, _ := start(t)
	client.Token = "wrong-token"
	err := call(context.Background(), client, "invite.list", nil, nil)
	var refused *admin.Error
	if !errors.As(err, &refused) || refused.Code != admin.CodeUnauthorized {
		t.Fatalf("Call = %v, want an unauthorized refusal", err)
	}
}

func TestInviteLifecycle(t *testing.T) {
	t.Parallel()
	client, db := start(t)
	ctx := context.Background()

	var created struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	if err := call(ctx, client, "invite.create", map[string]any{"uses": 2, "note": "lab"}, &created); err != nil {
		t.Fatalf("invite.create: %v", err)
	}
	if created.Code != "AAAA-BBBB-CCCC" || created.ID != "inv_test" {
		t.Errorf("created = %+v", created)
	}

	var invites []admin.InviteView
	if err := call(ctx, client, "invite.list", nil, &invites); err != nil {
		t.Fatalf("invite.list: %v", err)
	}
	if len(invites) != 1 || !invites[0].Usable || invites[0].MaxUses != 2 || invites[0].Note != "lab" {
		t.Errorf("invites = %+v", invites)
	}

	if err := call(ctx, client, "invite.revoke", map[string]string{"id": "inv_test"}, nil); err != nil {
		t.Fatalf("invite.revoke: %v", err)
	}
	if err := call(ctx, client, "invite.list", nil, &invites); err != nil {
		t.Fatalf("invite.list: %v", err)
	}
	if invites[0].Usable || !invites[0].Revoked {
		t.Errorf("a revoked invite still reports itself usable: %+v", invites[0])
	}
	// The revocation is recorded, and the code is still only a digest.
	invite, err := db.InviteByCodeHash(ctx, secret.Digest("AAAA-BBBB-CCCC"))
	if err != nil {
		t.Fatalf("InviteByCodeHash: %v", err)
	}
	if invite.RevokedAt == nil {
		t.Error("the invite was not marked revoked in the store")
	}
	if count, _ := db.AuditCount(ctx, "inv_test"); count != 1 {
		t.Errorf("audit records for the invite = %d, want 1 (creation only)", count)
	}
}

func TestUnknownOperationsAndArguments(t *testing.T) {
	t.Parallel()
	client, _ := start(t)
	ctx := context.Background()

	var refused *admin.Error
	if err := call(ctx, client, "uv.explode", nil, nil); !errors.As(err, &refused) || refused.Code != admin.CodeBadRequest {
		t.Errorf("unknown op = %v, want bad_request", err)
	}
	if err := call(ctx, client, "invite.revoke", map[string]string{}, nil); !errors.As(err, &refused) || refused.Code != admin.CodeBadRequest {
		t.Errorf("missing id = %v, want bad_request", err)
	}
	if err := call(ctx, client, "invite.revoke", map[string]string{"id": "inv_missing"}, nil); !errors.As(err, &refused) || refused.Code != admin.CodeNotFound {
		t.Errorf("missing invite = %v, want not_found", err)
	}
}

func TestApproveAndReject(t *testing.T) {
	t.Parallel()
	client, db := start(t)
	ctx := context.Background()

	for _, uv := range []store.UV{
		{ID: "uv_1", Domain: "one.example.com", SelfUUID: "u1", State: store.StatePending, CreatedAt: testNow, CredentialEpoch: 1},
		{ID: "uv_2", Domain: "two.example.com", SelfUUID: "u2", State: store.StatePending, CreatedAt: testNow, CredentialEpoch: 1},
	} {
		if err := db.CreateUV(ctx, uv); err != nil {
			t.Fatalf("CreateUV: %v", err)
		}
	}

	var nodes []admin.NodeView
	if err := call(ctx, client, "uv.list", nil, &nodes); err != nil {
		t.Fatalf("uv.list: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("uv.list returned %d nodes, want 2", len(nodes))
	}
	// The view never carries a credential, only whether one exists.
	for _, node := range nodes {
		if node.HasAgentKey || node.HasSubToken {
			t.Errorf("node %s reports credentials it does not have", node.ID)
		}
	}

	var decision struct {
		Changed bool           `json:"changed"`
		Node    admin.NodeView `json:"node"`
	}
	// Approving by domain, because that is what an operator reads in the log.
	if err := call(ctx, client, "uv.approve", map[string]string{"id": "one.example.com"}, &decision); err != nil {
		t.Fatalf("uv.approve: %v", err)
	}
	if !decision.Changed || decision.Node.State != string(store.StateActive) {
		t.Errorf("approve = %+v", decision)
	}
	stored, err := db.UVByID(ctx, "uv_1")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if stored.ApprovedAt == nil {
		t.Error("approval did not record a timestamp")
	}

	// A second approval changes nothing and says so.
	if err := call(ctx, client, "uv.approve", map[string]string{"id": "uv_1"}, &decision); err != nil {
		t.Fatalf("uv.approve again: %v", err)
	}
	if decision.Changed {
		t.Error("a repeated approval reported a change")
	}

	if err := call(ctx, client, "uv.reject", map[string]string{"id": "uv_2"}, &decision); err != nil {
		t.Fatalf("uv.reject: %v", err)
	}
	if decision.Node.State != string(store.StateRemoved) {
		t.Errorf("reject left the node in %s", decision.Node.State)
	}

	var shown struct {
		Node   admin.NodeView `json:"node"`
		Events []store.Event  `json:"events"`
	}
	if err := call(ctx, client, "uv.show", map[string]string{"id": "uv_1"}, &shown); err != nil {
		t.Fatalf("uv.show: %v", err)
	}
	if shown.Node.State != string(store.StateActive) || len(shown.Events) == 0 {
		t.Errorf("uv.show = %+v", shown)
	}
	if err := call(ctx, client, "uv.show", map[string]string{"id": "nope.example.com"}, &shown); err == nil {
		t.Error("uv.show accepted an unknown node")
	}
}

func TestClientReportsAMissingSocket(t *testing.T) {
	t.Parallel()
	client := admin.Client{Network: "tcp", Address: "127.0.0.1:1", Token: testToken, Timeout: time.Second}
	if err := call(context.Background(), client, "uv.list", nil, nil); err == nil {
		t.Fatal("Call succeeded without a server")
	}
}

func TestAgentKeyCanBeReadBack(t *testing.T) {
	t.Parallel()
	client, db := start(t)
	ctx := context.Background()

	// Issue a key through the socket, then read it back through the socket.
	var issued struct {
		AgentKey string `json:"agent_key"`
	}
	if err := call(ctx, client, "uv.reissue", map[string]string{"id": "uv_absent"}, &issued); err == nil {
		t.Fatal("reissue accepted an unknown node")
	}

	if err := db.CreateUV(ctx, store.UV{ID: "uv_1", Domain: "one.example.com", SelfUUID: "u1", State: store.StateActive, CreatedAt: testNow, CredentialEpoch: 1}); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}
	if err := call(ctx, client, "uv.reissue", map[string]string{"id": "uv_1"}, &issued); err != nil {
		t.Fatalf("uv.reissue: %v", err)
	}
	if issued.AgentKey == "" {
		t.Fatal("reissue returned no key")
	}

	var read struct {
		ID       string `json:"id"`
		AgentKey string `json:"agent_key"`
	}
	if err := call(ctx, client, "uv.agentkey", map[string]string{"id": "one.example.com"}, &read); err != nil {
		t.Fatalf("uv.agentkey: %v", err)
	}
	if read.AgentKey != issued.AgentKey {
		t.Errorf("uv.agentkey returned %q, want the issued key", read.AgentKey)
	}

	// A node with no stored key says so instead of returning an empty string.
	if err := db.CreateUV(ctx, store.UV{ID: "uv_2", Domain: "two.example.com", SelfUUID: "u2", State: store.StatePending, CreatedAt: testNow, CredentialEpoch: 1}); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}
	if err := call(ctx, client, "uv.agentkey", map[string]string{"id": "uv_2"}, &read); err == nil {
		t.Fatal("uv.agentkey invented a key for a node that has none")
	}
}

func TestOperatorOverrideFollowsTheStateTable(t *testing.T) {
	t.Parallel()
	client, db := start(t)
	ctx := context.Background()
	if err := db.CreateUV(ctx, store.UV{ID: "uv_1", Domain: "one.example.com", SelfUUID: "u1",
		State: store.StateActive, CreatedAt: testNow, CredentialEpoch: 1}); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	type answer struct {
		Node struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"node"`
		Changed bool `json:"changed"`
	}

	var blocked answer
	if err := call(ctx, client, "uv.block", map[string]string{"id": "uv_1", "reason": "abuse report"}, &blocked); err != nil {
		t.Fatalf("uv.block: %v", err)
	}
	if blocked.Node.State != string(store.StateBlocked) || !blocked.Changed {
		t.Errorf("block: state=%s changed=%v", blocked.Node.State, blocked.Changed)
	}

	// Blocking a blocked node is a no-op, and a no-op writes nothing.
	var again answer
	if err := call(ctx, client, "uv.block", map[string]string{"id": "uv_1"}, &again); err != nil {
		t.Fatalf("second uv.block: %v", err)
	}
	if again.Changed {
		t.Error("blocking a blocked node reported a change")
	}
	if events, err := db.UVEvents(ctx, "uv_1", 10); err != nil || len(events) != 1 {
		t.Errorf("events = %d (err %v), want exactly the one real transition", len(events), err)
	}

	var unblocked answer
	if err := call(ctx, client, "uv.unblock", map[string]string{"id": "one.example.com"}, &unblocked); err != nil {
		t.Fatalf("uv.unblock: %v", err)
	}
	if unblocked.Node.State != string(store.StateActive) || !unblocked.Changed {
		t.Errorf("unblock: state=%s changed=%v", unblocked.Node.State, unblocked.Changed)
	}

	var removed answer
	if err := call(ctx, client, "uv.rm", map[string]string{"id": "uv_1"}, &removed); err != nil {
		t.Fatalf("uv.rm: %v", err)
	}
	if removed.Node.State != string(store.StateRemoved) {
		t.Errorf("rm: state=%s", removed.Node.State)
	}

	// Removal is final: approving a removed node is refused, not silently accepted.
	if err := call(ctx, client, "uv.approve", map[string]string{"id": "uv_1"}, &removed); err == nil {
		t.Error("uv.approve accepted a removed node")
	}
}

func TestForgetFreesTheDomainOnlyAfterRemoval(t *testing.T) {
	t.Parallel()
	client, db := start(t)
	ctx := context.Background()
	if err := db.CreateUV(ctx, store.UV{ID: "uv_1", Domain: "one.example.com", SelfUUID: "u1",
		State: store.StateActive, CreatedAt: testNow, CredentialEpoch: 1}); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	// A live node is not forgotten by accident.
	if err := call(ctx, client, "uv.forget", map[string]string{"id": "uv_1"}, &struct{}{}); err == nil {
		t.Fatal("uv.forget erased a live node")
	}
	if _, err := db.UVByID(ctx, "uv_1"); err != nil {
		t.Fatalf("the live node is gone: %v", err)
	}

	if err := call(ctx, client, "uv.rm", map[string]string{"id": "uv_1"}, &struct{}{}); err != nil {
		t.Fatalf("uv.rm: %v", err)
	}
	if err := call(ctx, client, "uv.forget", map[string]string{"id": "uv_1"}, &struct{}{}); err != nil {
		t.Fatalf("uv.forget: %v", err)
	}
	if _, err := db.UVByID(ctx, "uv_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the node survived forget: %v", err)
	}

	// The domain is free again: the same domain can be registered for a new node.
	if err := db.CreateUV(ctx, store.UV{ID: "uv_2", Domain: "one.example.com", SelfUUID: "u2",
		State: store.StatePending, CreatedAt: testNow, CredentialEpoch: 1}); err != nil {
		t.Fatalf("the domain is still taken: %v", err)
	}
}

// rotate replaces the credential of the admin that authenticates, which is the only one it may touch:
// a token entitled to its own access must not be able to re-issue somebody else's.
func TestRotateReplacesThePresentedToken(t *testing.T) {
	t.Parallel()
	client, db := start(t)
	ctx := context.Background()

	var rotated admin.AdminTokenView
	if err := call(ctx, client, "admin.rotate", nil, &rotated); err != nil {
		t.Fatalf("admin.rotate: %v", err)
	}
	if rotated.Token == "" || rotated.Token == testToken {
		t.Fatalf("the rotation returned no new token: %+v", rotated)
	}
	if rotated.Name != "operator" {
		t.Errorf("rotation named %q, want the authenticated admin", rotated.Name)
	}
	if want := "admin_" + secret.Fingerprint(rotated.Token); rotated.ID != want {
		t.Errorf("id = %q, want %q (the id follows the token)", rotated.ID, want)
	}

	// The old credential is gone, not merely demoted.
	stale := client
	if err := call(ctx, stale, "invite.list", nil, nil); err == nil {
		t.Fatal("the old token still works")
	}
	// And the new one is the way in.
	fresh := admin.Client{Network: client.Network, Address: client.Address, Token: rotated.Token}
	var invites []admin.InviteView
	if err := call(ctx, fresh, "invite.list", nil, &invites); err != nil {
		t.Fatalf("the new token does not work: %v", err)
	}

	// The store agrees: one row, new digest, and the old digest resolves to nothing.
	if _, err := db.AdminByTokenHash(ctx, secret.Digest(testToken)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the old digest still resolves: %v", err)
	}
	if got, err := db.AdminByTokenHash(ctx, secret.Digest(rotated.Token)); err != nil || got.ID != rotated.ID {
		t.Errorf("the new digest does not resolve to %q: %v", rotated.ID, err)
	}
}

// A rotated credential has never been presented, so it must not inherit the old one's "last used".
func TestRotateClearsLastUsed(t *testing.T) {
	t.Parallel()
	client, db := start(t)
	ctx := context.Background()
	if err := call(ctx, client, "invite.list", nil, nil); err != nil {
		t.Fatalf("invite.list: %v", err)
	}
	before, err := db.AdminByTokenHash(ctx, secret.Digest(testToken))
	if err != nil || before.LastUsedAt == nil {
		t.Fatalf("the test needs a used token first: %+v (%v)", before, err)
	}
	var rotated admin.AdminTokenView
	if err := call(ctx, client, "admin.rotate", nil, &rotated); err != nil {
		t.Fatalf("admin.rotate: %v", err)
	}
	after, err := db.AdminByTokenHash(ctx, secret.Digest(rotated.Token))
	if err != nil {
		t.Fatalf("AdminByTokenHash: %v", err)
	}
	if after.LastUsedAt != nil {
		t.Errorf("the new token already has last_used_at = %v", after.LastUsedAt)
	}
}

func TestRotateNeedsAuthentication(t *testing.T) {
	t.Parallel()
	client, _ := start(t)
	client.Token = ""
	err := call(context.Background(), client, "admin.rotate", nil, nil)
	var refused *admin.Error
	if !errors.As(err, &refused) || refused.Code != admin.CodeUnauthorized {
		t.Fatalf("Call = %v, want an unauthorized refusal", err)
	}
}
