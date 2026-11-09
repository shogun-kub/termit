package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/shogun-kub/termit/internal/mn/store"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "termit.db")
	s, err := store.Open(context.Background(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testUV(id, domain string) store.UV {
	return store.UV{
		ID:              id,
		Label:           "lab node",
		Domain:          domain,
		SelfUUID:        "8f1c2d3e-4a5b-4c6d-8e7f-90a1b2c3d4e5",
		ProfileJSON:     `{"network":"xhttp"}`,
		State:           store.StatePending,
		CreatedAt:       testNow,
		AgentKeyHash:    "argon2id$fake",
		CredentialEpoch: 1,
	}
}

func TestOpenCreatesSchema(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()

	version, err := s.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != store.SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, store.SchemaVersion)
	}
	if err := s.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "newer.db")

	// Simulate a database written by a later build.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	if _, err := store.Open(context.Background(), store.Options{Path: path}); err == nil {
		t.Fatal("Open accepted a database from a newer build")
	}
}

func TestCreateAndReadUV(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	uv := testUV("uv_1", "node1.example.com")

	if err := s.CreateUV(ctx, uv); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}
	got, err := s.UVByID(ctx, uv.ID)
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if got.Domain != uv.Domain || got.State != store.StatePending || !got.CreatedAt.Equal(testNow) {
		t.Errorf("round trip changed the node: %+v", got)
	}
	if _, err := s.UVByDomain(ctx, uv.Domain); err != nil {
		t.Errorf("UVByDomain: %v", err)
	}
	if _, err := s.UVByAgentKeyHash(ctx, "argon2id$fake"); err != nil {
		t.Errorf("UVByAgentKeyHash: %v", err)
	}
	if _, err := s.UVByID(ctx, "uv_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing node = %v, want ErrNotFound", err)
	}

	// A second node cannot claim the same domain: the mesh routes by it.
	duplicate := testUV("uv_2", uv.Domain)
	if err := s.CreateUV(ctx, duplicate); !errors.Is(err, store.ErrDuplicate) {
		t.Errorf("duplicate domain = %v, want ErrDuplicate", err)
	}
}

func TestSetUVStateIsTransactional(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateUV(ctx, testUV("uv_1", "node1.example.com")); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	event, err := s.SetUVState(ctx, "uv_1", store.StateActive, "checks passed", "scheduler", testNow)
	if err != nil {
		t.Fatalf("SetUVState: %v", err)
	}
	if event.FromState != store.StatePending || event.ToState != store.StateActive {
		t.Errorf("event = %+v, want pending -> active", event)
	}

	uv, err := s.UVByID(ctx, "uv_1")
	if err != nil {
		t.Fatalf("UVByID: %v", err)
	}
	if uv.State != store.StateActive || uv.StateReason != "checks passed" {
		t.Errorf("node state = %s (%q)", uv.State, uv.StateReason)
	}

	events, err := s.UVEvents(ctx, "uv_1", 10)
	if err != nil {
		t.Fatalf("UVEvents: %v", err)
	}
	if len(events) != 1 || events[0].ToState != store.StateActive {
		t.Errorf("events = %+v, want one transition", events)
	}

	if _, err := s.SetUVState(ctx, "uv_missing", store.StateActive, "", "test", testNow); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("state change on a missing node = %v, want ErrNotFound", err)
	}
	if _, err := s.SetUVState(ctx, "uv_1", "nonsense", "", "test", testNow); err == nil {
		t.Error("SetUVState accepted an unknown state")
	}
}

func TestListUVs(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	for _, uv := range []store.UV{
		testUV("uv_1", "one.example.com"),
		testUV("uv_2", "two.example.com"),
		testUV("uv_3", "three.example.com"),
	} {
		if err := s.CreateUV(ctx, uv); err != nil {
			t.Fatalf("CreateUV %s: %v", uv.ID, err)
		}
	}
	if _, err := s.SetUVState(ctx, "uv_3", store.StateRemoved, "", "test", testNow); err != nil {
		t.Fatalf("SetUVState: %v", err)
	}

	all, err := s.ListUVs(ctx, store.UVFilter{})
	if err != nil {
		t.Fatalf("ListUVs: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("default listing returned %d nodes, want 2 (removed ones are hidden)", len(all))
	}

	withRemoved, err := s.ListUVs(ctx, store.UVFilter{IncludeRemoved: true})
	if err != nil {
		t.Fatalf("ListUVs: %v", err)
	}
	if len(withRemoved) != 3 {
		t.Errorf("listing with removed returned %d nodes, want 3", len(withRemoved))
	}

	pending, err := s.ListUVs(ctx, store.UVFilter{States: []store.State{store.StatePending}})
	if err != nil {
		t.Fatalf("ListUVs: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("pending listing returned %d nodes, want 2", len(pending))
	}
}

func TestInvites(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	expires := testNow.Add(24 * time.Hour)
	invite := store.Invite{
		ID:        "inv_1",
		CodeHash:  "argon2id$code",
		CreatedAt: testNow,
		ExpiresAt: &expires,
		MaxUses:   2,
	}

	if err := s.CreateInvite(ctx, invite); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	got, err := s.InviteByCodeHash(ctx, invite.CodeHash)
	if err != nil {
		t.Fatalf("InviteByCodeHash: %v", err)
	}
	if !got.Usable(testNow) {
		t.Error("a fresh invite is not usable")
	}

	for i := 0; i < 2; i++ {
		if err := s.RedeemInvite(ctx, invite.ID, testNow); err != nil {
			t.Fatalf("RedeemInvite %d: %v", i+1, err)
		}
	}
	if err := s.RedeemInvite(ctx, invite.ID, testNow); err == nil {
		t.Fatal("an exhausted invite was redeemed")
	}
	got, err = s.InviteByCodeHash(ctx, invite.CodeHash)
	if err != nil {
		t.Fatalf("InviteByCodeHash: %v", err)
	}
	if got.UsedCount != 2 {
		t.Errorf("used_count = %d, want 2", got.UsedCount)
	}
	if got.Usable(testNow) {
		t.Error("an exhausted invite reports itself usable")
	}

	// A revoked invite cannot be redeemed even with uses left.
	other := store.Invite{ID: "inv_2", CodeHash: "argon2id$other", CreatedAt: testNow, MaxUses: 5}
	if err := s.CreateInvite(ctx, other); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.RevokeInvite(ctx, other.ID, testNow); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if err := s.RedeemInvite(ctx, other.ID, testNow); err == nil {
		t.Fatal("a revoked invite was redeemed")
	}

	// An expired invite is refused by the store, not only by the caller.
	expiredAt := testNow.Add(-time.Minute)
	if err := s.CreateInvite(ctx, store.Invite{ID: "inv_3", CodeHash: "argon2id$old", CreatedAt: testNow.Add(-time.Hour), ExpiresAt: &expiredAt, MaxUses: 1}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.RedeemInvite(ctx, "inv_3", testNow); err == nil {
		t.Fatal("an expired invite was redeemed")
	}

	list, err := s.ListInvites(ctx)
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("ListInvites returned %d, want 3", len(list))
	}
}

func TestSettings(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.GetSetting(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing setting = %v, want ErrNotFound", err)
	}
	if err := s.SetSetting(ctx, "enrollment.mode", "invite+approval"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := s.SetSetting(ctx, "enrollment.mode", "invite"); err != nil {
		t.Fatalf("SetSetting (update): %v", err)
	}
	value, err := s.GetSetting(ctx, "enrollment.mode")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if value != "invite" {
		t.Errorf("setting = %q, want the updated value", value)
	}
	all, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("Settings returned %d rows, want 1", len(all))
	}
}

func TestChecksAndAudit(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.LatestCheck(ctx, "uv_1", "probe"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("check on a node with no checks = %v, want ErrNotFound", err)
	}
	if err := s.AppendCheck(ctx, store.Check{UVID: "uv_1", At: testNow, Kind: "probe", OK: true, LatencyMS: 42}); err != nil {
		t.Fatalf("AppendCheck: %v", err)
	}
	if err := s.AppendCheck(ctx, store.Check{UVID: "uv_1", At: testNow.Add(time.Minute), Kind: "probe", OK: false, ErrorClass: "timeout"}); err != nil {
		t.Fatalf("AppendCheck: %v", err)
	}
	latest, err := s.LatestCheck(ctx, "uv_1", "probe")
	if err != nil {
		t.Fatalf("LatestCheck: %v", err)
	}
	if latest.OK || latest.ErrorClass != "timeout" {
		t.Errorf("latest check = %+v, want the newest (failed) one", latest)
	}

	if err := s.AppendAudit(ctx, store.AuditRecord{At: testNow, Actor: "operator", Action: "uv.approve", Target: "uv_1"}); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	count, err := s.AuditCount(ctx, "uv_1")
	if err != nil {
		t.Fatalf("AuditCount: %v", err)
	}
	if count != 1 {
		t.Errorf("audit count = %d, want 1", count)
	}
	if err := s.AppendAudit(ctx, store.AuditRecord{At: testNow}); err == nil {
		t.Error("AppendAudit accepted a record with no action")
	}
}

func TestBackupAndMasterKey(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateUV(ctx, testUV("uv_1", "node1.example.com")); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	copied, err := store.OpenReadOnly(ctx, backupPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = copied.Close() }()
	if _, err := copied.UVByID(ctx, "uv_1"); err != nil {
		t.Errorf("the backup does not contain the node: %v", err)
	}

	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := store.WriteMasterKey(keyPath, []byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatalf("WriteMasterKey: %v", err)
	}
	if err := store.WriteMasterKey(keyPath, []byte("another key that must not win")); err == nil {
		t.Error("WriteMasterKey replaced an existing key")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Only Linux keeps the permission bits; the same check runs in the lab acceptance.
	if runtime.GOOS == "linux" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("master key mode = %04o, want 0600", perm)
		}
	}
	if _, err := store.ReadMasterKey(keyPath); err != nil {
		t.Errorf("ReadMasterKey: %v", err)
	}
	if err := store.WriteMasterKey(filepath.Join(t.TempDir(), "short.key"), nil); err == nil {
		t.Error("WriteMasterKey accepted an empty key")
	}
}
