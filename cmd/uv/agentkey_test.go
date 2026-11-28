package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/state"
)

// installedNode writes a minimal node whose state holds agentKey, and returns its config and state paths.
func installedNode(t *testing.T, agentKey string) (configPath, statePath string) {
	t.Helper()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	statePath = filepath.Join(stateDir, "state.json")
	st := &state.State{}
	if agentKey != "" {
		st.Credentials = &state.Credentials{AgentKey: agentKey}
	}
	if err := st.Save(statePath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	configPath = filepath.Join(dir, "config.yaml")
	body := "schema_version: 1\ndomain: node.example.com\nemail: root@node.example.com\n" +
		"paths:\n  state_dir: " + stateDir + "\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write the config: %v", err)
	}
	return configPath, statePath
}

func readKey(t *testing.T, statePath string) string {
	t.Helper()
	st, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.Credentials == nil {
		return ""
	}
	return st.Credentials.AgentKey
}

// A key a new home issued is installed into the node's state, and the command says which identity it left
// and which it took: the swap is immediate and until the node syncs it answers to nobody.
func TestAgentKeyInstallsAKeyANewHomeIssued(t *testing.T) {
	t.Parallel()
	skipWithoutPOSIXPermissions(t)
	configPath, statePath := installedNode(t, "old-key")
	keyFile := filepath.Join(t.TempDir(), "agent.key")
	if err := os.WriteFile(keyFile, []byte("new-key\n"), 0o600); err != nil {
		t.Fatalf("write the key: %v", err)
	}

	app, stdout, stderr := newTestApp(t)
	if code := app.runAgentKey([]string{"--config", configPath, "--install", keyFile}); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if got := readKey(t, statePath); got != "new-key" {
		t.Errorf("the state holds %q, want the installed key", got)
	}
	if !strings.Contains(stdout.String(), state.Fingerprint("old-key")) ||
		!strings.Contains(stdout.String(), state.Fingerprint("new-key")) {
		t.Errorf("the command does not name both identities: %s", stdout.String())
	}
	if info, err := os.Stat(statePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the state file is %v, want 0600: %v", info.Mode().Perm(), err)
	}
}

// The file is a credential: a key the group can read is a key to refuse, and refusing must change nothing.
func TestAgentKeyRefusesALooseKeyFile(t *testing.T) {
	t.Parallel()
	skipWithoutPOSIXPermissions(t)
	configPath, statePath := installedNode(t, "old-key")
	keyFile := filepath.Join(t.TempDir(), "agent.key")
	if err := os.WriteFile(keyFile, []byte("new-key\n"), 0o644); err != nil {
		t.Fatalf("write the key: %v", err)
	}

	app, _, stderr := newTestApp(t)
	if code := app.runAgentKey([]string{"--config", configPath, "--install", keyFile}); code != exitFailure {
		t.Fatalf("a world-readable key file was accepted (exit %d)", code)
	}
	if !strings.Contains(stderr.String(), "must not be readable") {
		t.Errorf("the refusal does not say why: %s", stderr.String())
	}
	if got := readKey(t, statePath); got != "old-key" {
		t.Errorf("the key changed to %q despite the refusal", got)
	}
}

func TestAgentKeyPrintsTheIdentity(t *testing.T) {
	t.Parallel()
	configPath, _ := installedNode(t, "old-key")

	app, stdout, stderr := newTestApp(t)
	if code := app.runAgentKey([]string{"--config", configPath}); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), state.Fingerprint("old-key")) {
		t.Errorf("the identity is not printed: %s", stdout.String())
	}
}

func TestAgentKeyIsIdempotent(t *testing.T) {
	t.Parallel()
	skipWithoutPOSIXPermissions(t)
	configPath, statePath := installedNode(t, "same-key")
	keyFile := filepath.Join(t.TempDir(), "agent.key")
	if err := os.WriteFile(keyFile, []byte("same-key\n"), 0o644); err != nil {
		t.Fatalf("write the key: %v", err)
	}

	app, stdout, stderr := newTestApp(t)
	if code := app.runAgentKey([]string{"--config", configPath, "--install", keyFile}); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already installed") {
		t.Errorf("re-installing the same key does not say so: %s", stdout.String())
	}
	if got := readKey(t, statePath); got != "same-key" {
		t.Errorf("the state holds %q, want the key it already had", got)
	}
}

// A node that never enrolled has no identity to keep: the command says so instead of writing a key into a
// state file that has nothing to use it with.
func TestAgentKeyRefusesANodeWithoutACredential(t *testing.T) {
	t.Parallel()
	configPath, _ := installedNode(t, "")
	keyFile := filepath.Join(t.TempDir(), "agent.key")
	if err := os.WriteFile(keyFile, []byte("new-key\n"), 0o600); err != nil {
		t.Fatalf("write the key: %v", err)
	}
	skipWithoutPOSIXPermissions(t)

	app, _, stderr := newTestApp(t)
	if code := app.runAgentKey([]string{"--config", configPath, "--install", keyFile}); code != exitFailure {
		t.Fatalf("a node without a credential accepted a key (exit %d)", code)
	}
	if !strings.Contains(stderr.String(), "no agent key") {
		t.Errorf("the refusal does not say why: %s", stderr.String())
	}
}
