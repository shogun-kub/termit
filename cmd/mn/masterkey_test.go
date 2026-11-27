package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/config"
)

// Decision 8 of §5.2.1: the environment first, the file second, and a deployment that hands over a broken key
// must fail rather than fall through to a file it did not mean to use.
func TestTheMasterKeyComesFromTheEnvironmentFirst(t *testing.T) {
	fromEnv := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	fromFile := []byte("fedcba9876543210fedcba9876543210")
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, fromFile, 0o600); err != nil {
		t.Fatalf("write the key file: %v", err)
	}
	cfg := &config.Config{MasterKeyFile: path}

	key, source, err := resolveMasterKey(cfg)
	if err != nil {
		t.Fatalf("resolveMasterKey: %v", err)
	}
	if source != masterKeyFromFile || string(key) != string(fromFile) {
		t.Errorf("without the variable: %q from %q, want the file", key, source)
	}

	t.Setenv(masterKeyEnv, fromEnv)
	key, source, err = resolveMasterKey(cfg)
	if err != nil {
		t.Fatalf("resolveMasterKey: %v", err)
	}
	if source != masterKeyFromEnv || hex.EncodeToString(key) != fromEnv {
		t.Errorf("with the variable: %s from %q, want the environment", hex.EncodeToString(key), source)
	}

	// A broken value is an error, not a quiet fallback: the operator meant this host to use it.
	for _, broken := range []string{"not-hex", "0011", strings.Repeat("zz", 32)} {
		t.Setenv(masterKeyEnv, broken)
		if _, _, err := resolveMasterKey(cfg); err == nil {
			t.Errorf("a %q value in %s was accepted", broken, masterKeyEnv)
		}
	}
}

func TestAMissingKeyIsReported(t *testing.T) {
	cfg := &config.Config{MasterKeyFile: filepath.Join(t.TempDir(), "absent.key")}
	t.Setenv(masterKeyEnv, "")
	if _, _, err := resolveMasterKey(cfg); err == nil {
		t.Fatal("resolveMasterKey accepted a host with no key at all")
	}
}
