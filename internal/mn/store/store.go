// Package store is the management server's persistence: one SQLite file, an explicit schema version,
// and queries that the HTTP API and the admin CLI share.
//
// The driver is pure Go (modernc.org/sqlite), so the binary needs no cgo and cross-compiles. Every
// connection sets the pragmas that make a proxy registry safe to write under load: WAL for readers
// during a write, foreign keys enforced, and a busy timeout instead of an immediate "database is
// locked" error.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the pure-Go driver

	"github.com/shogun-kub/termit/internal/atomicfile"
)

// SchemaVersion is the schema this build understands. A database with a higher version is refused
// rather than migrated down: an older binary writing a newer schema is how registries get corrupted.
const SchemaVersion = 5

//go:embed schema.sql
var schemaFS embed.FS

// ErrNotInitialised reports a database that has no schema yet.
var ErrNotInitialised = errors.New("store: database is not initialised")

// Store is an open database.
type Store struct {
	db   *sql.DB
	path string
}

// Options configure Open.
type Options struct {
	// Path is the database file. Its directory is created when missing.
	Path string
	// Now is the clock used for migration bookkeeping; tests set it.
	Now func() time.Time
}

// Open opens (and creates) the database at opts.Path, applies the schema and returns it.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("store: path is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o750); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", filepath.Dir(opts.Path), err)
	}
	return open(ctx, opts.Path, "rwc")
}

// OpenReadOnly opens an existing database without creating it. `termit-mn status` and backups use it, so a
// diagnostic command can never migrate a database by accident.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	return open(ctx, path, "ro")
}

func open(ctx context.Context, path, mode string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&mode=" + mode
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// A single writer keeps the pragmas and the WAL sane; SQLite allows one writer at a time anyway.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	s := &Store{db: db, path: path}
	if mode != "ro" {
		// The registry holds hashes and node metadata: nobody but the server reads it.
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: restrict %s: %w", path, err)
		}
		if err := s.migrate(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is the file this store was opened from.
func (s *Store) Path() string { return s.path }

// Ping reports whether the database answers.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Version returns the schema version recorded in the database.
func (s *Store) Version(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return version, nil
}

// migrations carry an existing database to a later version, keyed by the version they produce. A fresh
// database gets the whole schema instead, so this map records what changed rather than duplicating it.
var migrations = map[int][]string{
	2: {`ALTER TABLE uv ADD COLUMN sub_token_sealed TEXT NOT NULL DEFAULT ''`},
	3: {`ALTER TABLE uv ADD COLUMN agent_key_sealed TEXT NOT NULL DEFAULT ''`},
	4: {
		`ALTER TABLE uv ADD COLUMN next_check_at TEXT`,
		`ALTER TABLE uv ADD COLUMN last_check_at TEXT`,
		`CREATE INDEX IF NOT EXISTS uv_next_check ON uv (next_check_at)`,
	},
	5: {`ALTER TABLE uv ADD COLUMN cert_sha256 TEXT NOT NULL DEFAULT ''`},
}

// migrate brings a database to SchemaVersion. Version 0 is a fresh file: the schema is applied as one
// transaction and the version recorded afterwards, so a crash leaves either nothing or everything.
func (s *Store) migrate(ctx context.Context) error {
	version, err := s.Version(ctx)
	if err != nil {
		return err
	}
	switch {
	case version == SchemaVersion:
		return nil
	case version > SchemaVersion:
		return fmt.Errorf("store: %s has schema %d, this build understands %d: upgrade Termit", s.path, version, SchemaVersion)
	}

	if version > 0 {
		return s.applyMigrations(ctx, version)
	}

	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("store: read the embedded schema: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range splitStatements(string(schema)) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("store: apply schema: %w", err)
		}
	}
	// PRAGMA does not accept a bound parameter, and the value is a constant of this build.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("store: record the schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration: %w", err)
	}
	return nil
}

// applyMigrations runs the statements between the database's version and this build's, each step in its
// own transaction, so a failure leaves the database at a version that is still understood.
func (s *Store) applyMigrations(ctx context.Context, from int) error {
	for version := from + 1; version <= SchemaVersion; version++ {
		statements, ok := migrations[version]
		if !ok {
			return fmt.Errorf("store: %s is at schema %d and this build has no migration to %d", s.path, from, version)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration to %d: %w", version, err)
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("store: migrate to %d: %w", version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record version %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration to %d: %w", version, err)
		}
	}
	return nil
}

// splitStatements cuts the schema into statements SQLite will accept one at a time. Comment lines are
// dropped first: a ";" inside a comment would otherwise leave half a sentence to be executed as SQL.
func splitStatements(schema string) []string {
	var cleaned strings.Builder
	for _, line := range strings.Split(schema, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		cleaned.WriteString(line)
		cleaned.WriteString("\n")
	}
	parts := strings.Split(cleaned.String(), ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// Backup writes a consistent copy of the database to path. It uses VACUUM INTO, which is the only
// supported way to copy a live SQLite database without stopping the server.
func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("store: create %s: %w", filepath.Dir(path), err)
	}
	// VACUUM INTO refuses an existing file, and the operator asked for this one to be replaced.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: replace %s: %w", path, err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("store: backup to %s: %w", path, err)
	}
	return nil
}

// WriteMasterKey stores the master key with 0600, or returns the existing one. It never regenerates a
// key: losing it would invalidate every pair credential in the mesh.
func WriteMasterKey(path string, key []byte) error {
	if len(key) == 0 {
		return errors.New("store: refusing to write an empty master key")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("store: %s already exists; refusing to replace the master key", path)
	}
	return atomicfile.Write(path, key, 0o600)
}

// ReadMasterKey reads the master key file.
func ReadMasterKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the operator configured this path.
	if err != nil {
		return nil, fmt.Errorf("store: read the master key: %w", err)
	}
	if len(data) < 32 {
		return nil, fmt.Errorf("store: %s holds %d bytes, want at least 32", path, len(data))
	}
	return data, nil
}
