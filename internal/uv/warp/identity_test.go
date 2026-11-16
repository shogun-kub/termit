package warp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/warp"
)

func testIdentity() *warp.Identity {
	return &warp.Identity{
		PrivateKey:    "cHJpdmF0ZS1rZXktZm9yLXRlc3RzLW9ubHk=",
		PublicKey:     "cHVibGljLWtleS1mb3ItdGVzdHMtb25seQ==",
		Addresses:     []string{"172.16.0.2/32", "2606:4700:110:8d9c::2/128"},
		PeerPublicKey: "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
		Endpoint:      "162.159.192.1:2408",
		Reserved:      []byte{240, 25, 146},
		DeviceID:      "00000000-0000-0000-0000-000000000001",
		RegisteredAt:  time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC),
	}
}

// The file is a credential: 0600 on disk, and a round trip that keeps every field the tunnel needs.
func TestIdentityRoundTripsAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "warp.yaml")
	if err := warp.SaveIdentity(path, testIdentity()); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("permissions = %04o, want 0600", perm)
		}
	}
	loaded, err := warp.LoadIdentity(path)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if loaded.PrivateKey != testIdentity().PrivateKey || loaded.Endpoint != testIdentity().Endpoint {
		t.Errorf("loaded = %+v", loaded)
	}
	if len(loaded.Reserved) != 3 || loaded.Reserved[0] != 240 {
		t.Errorf("reserved = %v", loaded.Reserved)
	}
	if len(loaded.Addresses) != 2 {
		t.Errorf("addresses = %v", loaded.Addresses)
	}
}

func TestIdentityRefusesAWorldReadableFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits; the node only runs on Linux")
	}
	path := filepath.Join(t.TempDir(), "warp.yaml")
	if err := warp.SaveIdentity(path, testIdentity()); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	_, err := warp.LoadIdentity(path)
	if err == nil || !strings.Contains(err.Error(), "readable by group or others") {
		t.Fatalf("LoadIdentity = %v, want a refusal naming the permissions", err)
	}
}

func TestIdentityRefusesAnIncompleteFile(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*warp.Identity){
		"no private key": func(i *warp.Identity) { i.PrivateKey = "" },
		"no peer key":    func(i *warp.Identity) { i.PeerPublicKey = "" },
		"no endpoint":    func(i *warp.Identity) { i.Endpoint = "" },
		"no address":     func(i *warp.Identity) { i.Addresses = nil },
		"two reserved":   func(i *warp.Identity) { i.Reserved = []byte{1, 2} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "warp.yaml")
			identity := testIdentity()
			mutate(identity)
			if err := warp.SaveIdentity(path, identity); err == nil {
				t.Fatalf("SaveIdentity accepted %+v", identity)
			}
		})
	}
}

// A re-run of init must not mint a new device: the file is written once and the second write would be
// a different identity only if the caller asked for one.
func TestSavingTwiceIsStable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "warp.yaml")
	if err := warp.SaveIdentity(path, testIdentity()); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := warp.SaveIdentity(path, testIdentity()); err != nil {
		t.Fatalf("SaveIdentity again: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile again: %v", err)
	}
	if string(first) != string(second) {
		t.Error("writing the same identity twice changed the file")
	}

}
