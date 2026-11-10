package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/version"
)

// masterKeyEnv lets a deployment take the master key from the environment instead of a file, which is
// what a container or a secret manager wants. The value is hex, like the file.
const masterKeyEnv = "TERMIT_MN_MASTER"

// defaultConfigPath is where the server keeps its configuration.
const defaultConfigPath = "/etc/termit/mn/config.yaml"

// runInit creates the server: configuration, master key, database and the first credentials.
func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "path to config.yaml")
	domain := flags.String("domain", "", "the name this server answers on (required)")
	dataDir := flags.String("data-dir", "", "where the database and certificates live")
	listen := flags.String("listen", "", "listen address, host:port")
	acmeDirectory := flags.String("acme-ca", "", "ACME directory URL")
	acmeInsecure := flags.Bool("acme-skip-tls-verify", false, "lab only: do not verify the ACME server certificate")
	email := flags.String("email", "", "ACME account contact address")
	mode := flags.String("mode", "", "enrollment mode: invite, invite+approval or open")
	inviteUses := flags.Int("invite-uses", 1, "how many nodes the first invite code may register")
	adminName := flags.String("admin", "operator", "name of the first admin token")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "termit-mn init: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	cfg := config.Default()
	freshConfig := true
	if existing, err := config.Load(*configPath); err == nil {
		freshConfig = false
		// Re-running init on an installed server keeps every decision it already made.
		cfg = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitFailure
	}
	if *domain != "" {
		if cfg.Domain != "" && cfg.Domain != *domain {
			fmt.Fprintf(stderr, "termit-mn init: this server is already %q; refusing to rename it to %q\n", cfg.Domain, *domain)
			return exitFailure
		}
		cfg.Domain = *domain
	}
	if cfg.Domain == "" {
		fmt.Fprintln(stderr, "termit-mn init: --domain is required on the first run")
		return exitUsage
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
		cfg.DBPath = filepath.Join(*dataDir, "termit.db")
	}
	if freshConfig {
		// The master key belongs next to the configuration it serves: a --config elsewhere is a
		// deployment that keeps its secrets there too.
		cfg.MasterKeyFile = filepath.Join(filepath.Dir(*configPath), "master.key")
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *acmeDirectory != "" {
		cfg.ACME.Directory = *acmeDirectory
	}
	if *acmeInsecure {
		cfg.ACME.SkipTLSVerify = true
	}
	if *email != "" {
		cfg.ACME.Email = *email
	}
	if *mode != "" {
		cfg.Enrollment.Mode = *mode
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Dir(*configPath), 0o750},
		{cfg.DataDir, 0o750},
		{cfg.CertDir(), 0o700},
	} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			fmt.Fprintf(stderr, "termit-mn init: create %s: %v\n", dir.path, err)
			return exitFailure
		}
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			fmt.Fprintf(stderr, "termit-mn init: chmod %s: %v\n", dir.path, err)
			return exitFailure
		}
	}

	masterKey, created, err := ensureMasterKey(cfg.MasterKeyFile)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitFailure
	}
	if err := cfg.Save(*configPath); err != nil {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitFailure
	}

	db, err := store.Open(ctx, store.Options{Path: cfg.DBPath})
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitFailure
	}
	defer func() { _ = db.Close() }()

	now := time.Now().UTC()
	if err := recordSettings(ctx, db, cfg, now); err != nil {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitFailure
	}

	adminToken, err := ensureAdmin(ctx, db, *adminName, now, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitFailure
	}
	inviteCode, err := ensureInvite(ctx, db, *inviteUses, now, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "termit-mn init: %v\n", err)
		return exitFailure
	}

	fmt.Fprintf(stdout, "domain:       %s\n", cfg.Domain)
	fmt.Fprintf(stdout, "listen:       %s\n", cfg.Listen)
	fmt.Fprintf(stdout, "config:       %s\n", *configPath)
	fmt.Fprintf(stdout, "database:     %s (schema %d)\n", cfg.DBPath, store.SchemaVersion)
	fmt.Fprintf(stdout, "master key:   %s%s\n", cfg.MasterKeyFile, map[bool]string{true: " (generated now)", false: " (kept)"}[created])
	fmt.Fprintf(stdout, "master key id: %s\n", secret.Fingerprint(hex.EncodeToString(sha256Of(masterKey))))
	if adminToken != "" {
		fmt.Fprintf(stdout, "\nadmin token (shown once, store it now):\n  %s\n", adminToken)
	}
	if inviteCode != "" {
		fmt.Fprintf(stdout, "\nfirst invite code (shown once, %d use(s)):\n  %s\n", *inviteUses, inviteCode)
	}
	fmt.Fprintf(stdout, "\nback up %s together with the database: without it no pair credential can be derived.\n",
		cfg.MasterKeyFile)
	return exitOK
}

// ensureMasterKey returns the master key, generating it on the first run. An existing key is never
// replaced: every pair credential in the mesh depends on it.
func ensureMasterKey(path string) ([]byte, bool, error) {
	if fromEnv := os.Getenv(masterKeyEnv); fromEnv != "" {
		key, err := hex.DecodeString(strings.TrimSpace(fromEnv))
		if err != nil {
			return nil, false, fmt.Errorf("%s is not hex: %w", masterKeyEnv, err)
		}
		if len(key) < 32 {
			return nil, false, fmt.Errorf("%s holds %d bytes, want at least 32", masterKeyEnv, len(key))
		}
		return key, false, nil
	}
	if key, err := store.ReadMasterKey(path); err == nil {
		return key, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, fmt.Errorf("read randomness: %w", err)
	}
	if err := store.WriteMasterKey(path, []byte(hex.EncodeToString(key)+"\n")); err != nil {
		return nil, false, err
	}
	return key, true, nil
}

// recordSettings writes the mutable configuration the scheduler reads at runtime.
func recordSettings(ctx context.Context, db *store.Store, cfg *config.Config, now time.Time) error {
	settings := map[string]string{
		"enrollment.mode":                  cfg.Enrollment.Mode,
		"policy.block_after_failed_cycles": fmt.Sprint(cfg.Policy.BlockAfterFailedCycles),
		"policy.probe_retries":             fmt.Sprint(cfg.Policy.ProbeRetries),
		"policy.check_interval":            cfg.Policy.CheckInterval.String(),
		"policy.sync_interval":             cfg.Policy.SyncInterval.String(),
		"server.created_at":                now.Format(time.RFC3339),
		"server.version":                   version.Version,
	}
	for key, value := range settings {
		if _, err := db.GetSetting(ctx, key); err == nil {
			continue // a re-run keeps what the operator set
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := db.SetSetting(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

// ensureAdmin creates the first admin token, or reports that one already exists.
func ensureAdmin(ctx context.Context, db *store.Store, name string, now time.Time, stdout, stderr io.Writer) (string, error) {
	// A re-run must not mint a second operator token: the operator would have two and no record of
	// which one the old scripts use.
	if count, err := db.AdminCount(ctx); err != nil {
		return "", err
	} else if count > 0 {
		fmt.Fprintln(stdout, "admin token: already set, not replaced")
		return "", nil
	}
	token, err := secret.Token()
	if err != nil {
		return "", err
	}
	hash := secret.Digest(token)
	id := "admin_" + secret.Fingerprint(token)
	if err := db.CreateAdmin(ctx, store.Admin{ID: id, Name: name, TokenHash: hash, CreatedAt: now}); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			fmt.Fprintln(stdout, "admin token: already set")
			return "", nil
		}
		return "", err
	}
	return token, nil
}

// ensureInvite creates the first invite code, or reports that invites already exist.
func ensureInvite(ctx context.Context, db *store.Store, uses int, now time.Time, stdout, stderr io.Writer) (string, error) {
	if count, err := db.InviteCount(ctx); err != nil {
		return "", err
	} else if count > 0 {
		fmt.Fprintln(stdout, "invite: already present, run `termit-mn invite create` for another")
		return "", nil
	}
	code, err := secret.Code()
	if err != nil {
		return "", err
	}
	hash := secret.Digest(code)
	id := "inv_" + secret.Fingerprint(code)
	if err := db.CreateInvite(ctx, store.Invite{
		ID:        id,
		CodeHash:  hash,
		CreatedAt: now,
		CreatedBy: "termit-mn init",
		MaxUses:   uses,
		Note:      "created by termit-mn init",
	}); err != nil {
		return "", err
	}
	return code, nil
}

// sha256Of is the short identity of the master key: never the key itself, only a way to tell two
// deployments apart in a log line.
func sha256Of(key []byte) []byte {
	sum := sha256.Sum256(key)
	return sum[:]
}
