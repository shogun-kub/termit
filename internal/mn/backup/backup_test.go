package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/backup"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
)

var created = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// fixture is an installed server: config, master key, database and one certificate.
type fixture struct {
	dir        string
	configPath string
	cfg        *config.Config
	store      *store.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Domain = "mn.example.com"
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.DBPath = filepath.Join(cfg.DataDir, "termit.db")
	cfg.MasterKeyFile = filepath.Join(dir, "master.key")
	if err := os.MkdirAll(cfg.CertDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := store.WriteMasterKey(cfg.MasterKeyFile, []byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatalf("WriteMasterKey: %v", err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for name, body := range map[string]string{
		"fullchain.pem": "-----BEGIN CERTIFICATE-----\nnot a real certificate\n",
		"privkey.pem":   "-----BEGIN PRIVATE KEY-----\nnot a real key\n",
	} {
		if err := os.WriteFile(filepath.Join(cfg.CertDir(), name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	db, err := store.Open(context.Background(), store.Options{Path: cfg.DBPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SetSetting(context.Background(), "enrollment.mode", "invite"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := db.CreateUV(context.Background(), store.UV{
		ID: "uv_1", Domain: "node.example.com", SelfUUID: "uuid", State: store.StateActive,
		CreatedAt: created, CredentialEpoch: 1,
	}); err != nil {
		t.Fatalf("CreateUV: %v", err)
	}
	return &fixture{dir: dir, configPath: configPath, cfg: cfg, store: db}
}

func TestCreateWritesEveryPart(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	archivePath := filepath.Join(fx.dir, "backup.tar.gz")

	result, err := backup.Create(context.Background(), backup.Options{
		Config:     fx.cfg,
		Store:      fx.store,
		ConfigPath: fx.configPath,
		Out:        archivePath,
		Now:        func() time.Time { return created },
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.Bytes == 0 {
		t.Error("the archive is empty")
	}
	if result.Manifest.Domain != fx.cfg.Domain || result.Manifest.SchemaVersion != store.SchemaVersion {
		t.Errorf("manifest = %+v", result.Manifest)
	}

	entries := readEntries(t, archivePath)
	want := []string{"manifest.json", "termit.db", "config.yaml", "master.key",
		"certs/fullchain.pem", "certs/privkey.pem"}
	for _, name := range want {
		if _, ok := entries[name]; !ok {
			t.Errorf("the archive has no %s; entries: %v", name, keys(entries))
		}
	}
	// The private key must not become world-readable inside the archive.
	if mode := entries["certs/privkey.pem"]; mode != 0o600 {
		t.Errorf("private key mode in the archive = %04o, want 0600", mode)
	}
	if mode := entries["master.key"]; mode != 0o600 {
		t.Errorf("master key mode in the archive = %04o, want 0600", mode)
	}
	if _, ok := entries["config.yaml"]; ok {
		if mode := entries["config.yaml"]; mode != 0o644 {
			t.Errorf("config mode in the archive = %04o, want 0644", mode)
		}
	}
}

func TestRestoreRebuildsTheServer(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	archivePath := filepath.Join(fx.dir, "backup.tar.gz")
	if _, err := backup.Create(context.Background(), backup.Options{
		Config: fx.cfg, Store: fx.store, ConfigPath: fx.configPath, Out: archivePath,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	target := t.TempDir()
	configPath := filepath.Join(target, "config.yaml")
	manifest, err := backup.Restore(context.Background(), backup.RestoreOptions{
		Archive:    archivePath,
		ConfigPath: configPath,
		DataDir:    filepath.Join(target, "data"),
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if manifest.Domain != "mn.example.com" {
		t.Errorf("restored manifest domain = %q", manifest.Domain)
	}

	restored, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("the restored configuration does not load: %v", err)
	}
	if restored.Domain != fx.cfg.Domain {
		t.Errorf("restored domain = %q", restored.Domain)
	}

	key, err := store.ReadMasterKey(filepath.Join(target, "master.key"))
	if err != nil {
		t.Fatalf("the restored master key does not read: %v", err)
	}
	if string(key) != "0123456789abcdef0123456789abcdef" {
		t.Error("the restored master key differs from the original")
	}

	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(target, "data", "termit.db")})
	if err != nil {
		t.Fatalf("the restored database does not open: %v", err)
	}
	defer func() { _ = db.Close() }()
	uv, err := db.UVByID(context.Background(), "uv_1")
	if err != nil {
		t.Fatalf("the restored database has no node: %v", err)
	}
	if uv.Domain != "node.example.com" || uv.State != store.StateActive {
		t.Errorf("restored node = %+v", uv)
	}
}

func TestRestoreRefusesToOverwrite(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	archivePath := filepath.Join(fx.dir, "backup.tar.gz")
	if _, err := backup.Create(context.Background(), backup.Options{
		Config: fx.cfg, Store: fx.store, ConfigPath: fx.configPath, Out: archivePath,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Restoring onto a live server must not silently replace its registry.
	_, err := backup.Restore(context.Background(), backup.RestoreOptions{
		Archive:    archivePath,
		ConfigPath: fx.configPath,
		DataDir:    fx.cfg.DataDir,
	})
	if err == nil {
		t.Fatal("Restore overwrote an existing database without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error %q does not mention --force", err)
	}

	// Force replaces a database that is in use: close it first, as an operator would stop the server.
	if err := fx.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := backup.Restore(context.Background(), backup.RestoreOptions{
		Archive:    archivePath,
		ConfigPath: fx.configPath,
		DataDir:    fx.cfg.DataDir,
		Force:      true,
	}); err != nil {
		t.Fatalf("Restore with Force: %v", err)
	}
}

func TestRestoreRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	archivePath := filepath.Join(fx.dir, "backup.tar.gz")
	if _, err := backup.Create(context.Background(), backup.Options{
		Config: fx.cfg, Store: fx.store, ConfigPath: fx.configPath, Out: archivePath,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Rewrite the manifest as a future build would have written it.
	rewriteManifest(t, archivePath, `{"schema_version": 99, "domain": "mn.example.com", "files": ["termit.db", "config.yaml", "master.key"]}`)

	target := t.TempDir()
	_, err := backup.Restore(context.Background(), backup.RestoreOptions{
		Archive:    archivePath,
		ConfigPath: filepath.Join(target, "config.yaml"),
		DataDir:    filepath.Join(target, "data"),
	})
	if err == nil {
		t.Fatal("Restore accepted an archive from a newer build")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("error %q does not mention the schema", err)
	}
}

func TestRestoreRefusesATruncatedArchive(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	archivePath := filepath.Join(fx.dir, "backup.tar.gz")
	if _, err := backup.Create(context.Background(), backup.Options{
		Config: fx.cfg, Store: fx.store, ConfigPath: fx.configPath, Out: archivePath,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The manifest lists a file the archive does not carry.
	rewriteManifest(t, archivePath, `{"schema_version": 1, "domain": "mn.example.com", "files": ["termit.db", "config.yaml", "master.key", "certs/missing.pem"]}`)

	target := t.TempDir()
	_, err := backup.Restore(context.Background(), backup.RestoreOptions{
		Archive:    archivePath,
		ConfigPath: filepath.Join(target, "config.yaml"),
		DataDir:    filepath.Join(target, "data"),
	})
	if err == nil {
		t.Fatal("Restore accepted an archive missing an entry the manifest lists")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

func TestRestoreRefusesAForeignArchive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "foreign.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"../../etc/passwd", "termit.db"} {
		body := []byte("payload")
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	target := t.TempDir()
	_, err = backup.Restore(context.Background(), backup.RestoreOptions{
		Archive:    archivePath,
		ConfigPath: filepath.Join(target, "config.yaml"),
		DataDir:    filepath.Join(target, "data"),
	})
	if err == nil {
		t.Fatal("Restore accepted an archive with a path escape")
	}
	if !strings.Contains(err.Error(), "not part of a Termit backup") {
		t.Errorf("error %q does not explain the refusal", err)
	}
}

func readEntries(t *testing.T, archivePath string) map[string]os.FileMode {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string]os.FileMode{}
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		out[header.Name] = os.FileMode(header.Mode).Perm()
	}
	return out
}

// rewriteManifest rebuilds the archive with a replaced manifest, which is how a future build or a
// corrupted archive looks to the restore path.
func rewriteManifest(t *testing.T, archivePath, manifest string) {
	t.Helper()
	sourceFile, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sourceGz, err := gzip.NewReader(sourceFile)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	source := tar.NewReader(sourceGz)

	tmpPath := archivePath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	outGz := gzip.NewWriter(out)
	outTar := tar.NewWriter(outGz)
	for {
		header, err := source.Next()
		if err != nil {
			break
		}
		body := make([]byte, header.Size)
		// tar.Reader may report io.EOF together with the last bytes of an entry, so a plain Read
		// cannot tell "done" from "truncated".
		if _, err := io.ReadFull(source, body); err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		if header.Name == "manifest.json" {
			body = []byte(manifest)
		}
		header.Size = int64(len(body))
		if err := outTar.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := outTar.Write(body); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	for _, closer := range []interface{ Close() error }{outTar, outGz, out} {
		if err := closer.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	_ = sourceFile.Close()
	if err := os.Rename(tmpPath, archivePath); err != nil {
		t.Fatalf("Rename: %v", err)
	}
}

func keys(entries map[string]os.FileMode) []string {
	out := make([]string, 0, len(entries))
	for name := range entries {
		out = append(out, name)
	}
	return out
}
