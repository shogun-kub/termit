// Package backup writes and restores the management server's state as one archive: the database, the
// configuration, the master key and the certificates.
//
// The archive is not a convenience copy. Without the master key no pair credential can be derived
// again, and without the database the mesh has no registry, so a restore has to be able to rebuild a
// working server on a fresh machine — and has to refuse anything it cannot verify.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/atomicfile"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/version"
)

// Names inside the archive.
const (
	manifestName   = "manifest.json"
	databaseName   = "termit.db"
	configName     = "config.yaml"
	masterKeyName  = "master.key"
	certsPrefix    = "certs/"
	databaseMode   = os.FileMode(0o600)
	masterKeyMode  = os.FileMode(0o600)
	privateKeyMode = os.FileMode(0o600)
	publicMode     = os.FileMode(0o644)
)

// Manifest is the first entry of every archive: what is inside, and which build wrote it.
type Manifest struct {
	CreatedAt     time.Time `json:"created_at"`
	Domain        string    `json:"domain"`
	Build         string    `json:"build"`
	APIVersion    string    `json:"api_version"`
	SchemaVersion int       `json:"schema_version"`
	// Files lists the entries, so a truncated archive is detectable before it is applied.
	Files []string `json:"files"`
}

// Result describes a written archive.
type Result struct {
	Path     string
	Manifest Manifest
	Bytes    int64
}

// Options configure Create.
type Options struct {
	Config *config.Config
	Store  *store.Store
	// ConfigPath is the configuration file to include; it cannot be derived from the struct.
	ConfigPath string
	// Out is the archive path.
	Out string
	Now func() time.Time
}

// Create writes the archive at opts.Out.
func Create(ctx context.Context, opts Options) (Result, error) {
	if opts.Config == nil || opts.Store == nil {
		return Result{}, errors.New("backup: a configuration and an open database are required")
	}
	if opts.Out == "" {
		return Result{}, errors.New("backup: an output path is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(opts.Out), 0o750); err != nil {
		return Result{}, fmt.Errorf("backup: create %s: %w", filepath.Dir(opts.Out), err)
	}

	// VACUUM INTO needs a file that does not exist yet, and the archive wants a file, not a database.
	staging, err := os.MkdirTemp("", "termit-mn-backup-")
	if err != nil {
		return Result{}, fmt.Errorf("backup: staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	dbPath := filepath.Join(staging, databaseName)
	if err := opts.Store.Backup(ctx, dbPath); err != nil {
		return Result{}, err
	}

	certDir := opts.Config.CertDir()
	certFiles, err := listRegularFiles(certDir)
	if err != nil {
		return Result{}, err
	}

	manifest := Manifest{
		CreatedAt:     opts.Now().UTC(),
		Domain:        opts.Config.Domain,
		Build:         version.Version,
		APIVersion:    version.APIVersion,
		SchemaVersion: store.SchemaVersion,
		Files:         []string{databaseName, configName, masterKeyName},
	}
	for _, name := range certFiles {
		manifest.Files = append(manifest.Files, certsPrefix+name)
	}
	sort.Strings(manifest.Files)

	tmp, err := os.CreateTemp(filepath.Dir(opts.Out), ".termit-backup-*.tar.gz")
	if err != nil {
		return Result{}, fmt.Errorf("backup: create the archive: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	writeErr := func() error {
		meta, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("backup: encode the manifest: %w", err)
		}
		if err := writeEntry(tw, manifestName, meta, publicMode, manifest.CreatedAt); err != nil {
			return err
		}
		for _, entry := range []struct {
			name string
			path string
			mode os.FileMode
		}{
			{databaseName, dbPath, databaseMode},
			{configName, opts.ConfigPath, publicMode},
			{masterKeyName, opts.Config.MasterKeyFile, masterKeyMode},
		} {
			data, err := os.ReadFile(entry.path) // #nosec G304 -- paths come from the validated config.
			if err != nil {
				return fmt.Errorf("backup: read %s: %w", entry.path, err)
			}
			if err := writeEntry(tw, entry.name, data, entry.mode, manifest.CreatedAt); err != nil {
				return err
			}
		}
		for _, name := range certFiles {
			data, err := os.ReadFile(filepath.Join(certDir, name)) // #nosec G304 -- see above.
			if err != nil {
				return fmt.Errorf("backup: read certificate %s: %w", name, err)
			}
			entryMode := os.FileMode(publicMode)
			if strings.HasSuffix(name, ".key") || strings.HasSuffix(name, "privkey.pem") {
				entryMode = privateKeyMode
			}
			if err := writeEntry(tw, certsPrefix+name, data, entryMode, manifest.CreatedAt); err != nil {
				return err
			}
		}
		return nil
	}()
	if writeErr != nil {
		return Result{}, writeErr
	}
	if err := tw.Close(); err != nil {
		return Result{}, fmt.Errorf("backup: close the archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Result{}, fmt.Errorf("backup: close the compressor: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return Result{}, fmt.Errorf("backup: sync the archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("backup: close the archive file: %w", err)
	}
	info, err := os.Stat(tmpName)
	if err != nil {
		return Result{}, fmt.Errorf("backup: stat the archive: %w", err)
	}
	if err := os.Rename(tmpName, opts.Out); err != nil {
		return Result{}, fmt.Errorf("backup: publish %s: %w", opts.Out, err)
	}
	return Result{Path: opts.Out, Manifest: manifest, Bytes: info.Size()}, nil
}

// RestoreOptions configure Restore.
type RestoreOptions struct {
	Archive string
	// ConfigPath and DataDir are where the restored files land; MasterKeyPath follows the config.
	ConfigPath    string
	DataDir       string
	MasterKeyPath string
	// Force allows replacing an existing database.
	Force bool
}

// Restore unpacks an archive into a fresh location. It refuses to overwrite a database unless Force
// is set, and refuses an archive written by a newer build: an old binary reading a new schema is how
// a registry gets corrupted.
func Restore(ctx context.Context, opts RestoreOptions) (Manifest, error) {
	if opts.Archive == "" || opts.ConfigPath == "" || opts.DataDir == "" {
		return Manifest{}, errors.New("restore: archive, config path and data directory are required")
	}
	if opts.MasterKeyPath == "" {
		opts.MasterKeyPath = filepath.Join(filepath.Dir(opts.ConfigPath), "master.key")
	}
	staging, err := os.MkdirTemp("", "termit-mn-restore-")
	if err != nil {
		return Manifest{}, fmt.Errorf("restore: staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := extract(ctx, opts.Archive, staging); err != nil {
		return Manifest{}, err
	}

	data, err := os.ReadFile(filepath.Join(staging, manifestName))
	if err != nil {
		return Manifest{}, errors.New("restore: the archive has no manifest; it was not written by this command")
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("restore: unreadable manifest: %w", err)
	}
	if manifest.SchemaVersion > store.SchemaVersion {
		return Manifest{}, fmt.Errorf("restore: the archive has schema %d, this build understands %d: upgrade Termit",
			manifest.SchemaVersion, store.SchemaVersion)
	}
	// A missing entry means a truncated archive; applying half of it is worse than refusing.
	for _, name := range manifest.Files {
		if _, err := os.Stat(filepath.Join(staging, filepath.FromSlash(name))); err != nil {
			return Manifest{}, fmt.Errorf("restore: the archive is missing %s: %w", name, err)
		}
	}

	dbTarget := filepath.Join(opts.DataDir, databaseName)
	if !opts.Force {
		if _, err := os.Stat(dbTarget); err == nil {
			return Manifest{}, fmt.Errorf("restore: %s exists; pass --force to replace it", dbTarget)
		}
	}
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{
		{opts.DataDir, 0o750},
		{filepath.Join(opts.DataDir, "certs"), 0o700},
		{filepath.Dir(opts.ConfigPath), 0o750},
	} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return Manifest{}, fmt.Errorf("restore: create %s: %w", dir.path, err)
		}
	}

	placements := []struct {
		from string
		to   string
		mode os.FileMode
	}{
		{filepath.Join(staging, databaseName), dbTarget, databaseMode},
		{filepath.Join(staging, configName), opts.ConfigPath, publicMode},
		{filepath.Join(staging, masterKeyName), opts.MasterKeyPath, masterKeyMode},
	}
	for _, name := range manifest.Files {
		if !strings.HasPrefix(name, certsPrefix) {
			continue
		}
		target := filepath.Join(opts.DataDir, filepath.FromSlash(name))
		entryMode := os.FileMode(publicMode)
		if strings.HasSuffix(name, ".key") || strings.HasSuffix(name, "privkey.pem") {
			entryMode = privateKeyMode
		}
		placements = append(placements, struct {
			from string
			to   string
			mode os.FileMode
		}{filepath.Join(staging, filepath.FromSlash(name)), target, entryMode})
	}
	for _, placement := range placements {
		data, err := os.ReadFile(placement.from) // #nosec G304 -- entries come from the verified manifest.
		if err != nil {
			return Manifest{}, fmt.Errorf("restore: read %s: %w", placement.from, err)
		}
		if err := atomicfile.Write(placement.to, data, placement.mode); err != nil {
			return Manifest{}, fmt.Errorf("restore: write %s: %w", placement.to, err)
		}
	}
	return manifest, nil
}

// extract unpacks the archive, admitting only the names an archive may carry.
func extract(ctx context.Context, archivePath, dir string) error {
	file, err := os.Open(archivePath) // #nosec G304 -- the operator chose the archive.
	if err != nil {
		return fmt.Errorf("restore: open %s: %w", archivePath, err)
	}
	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("restore: %s is not a gzip archive: %w", archivePath, err)
	}
	tr := tar.NewReader(gz)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("restore: read the archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("restore: %s is not a regular file; refusing the archive", header.Name)
		}
		if !allowedName(header.Name) {
			return fmt.Errorf("restore: %s is not part of a Termit backup; refusing the archive", header.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(header.Name))
		data, err := io.ReadAll(io.LimitReader(tr, 1<<30))
		if err != nil {
			return fmt.Errorf("restore: read %s: %w", header.Name, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("restore: create %s: %w", filepath.Dir(target), err)
		}
		// The staging copy is private regardless of what the archive claims; the final mode is
		// applied when the file is placed.
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return fmt.Errorf("restore: write %s: %w", target, err)
		}
	}
}

// allowedName accepts the manifest and the four entry kinds, and nothing with a path escape.
func allowedName(name string) bool {
	if name == manifestName || name == databaseName || name == configName || name == masterKeyName {
		return true
	}
	if !strings.HasPrefix(name, certsPrefix) {
		return false
	}
	rest := strings.TrimPrefix(name, certsPrefix)
	return rest != "" && !strings.Contains(rest, "/") && rest != "." && rest != ".."
}

func writeEntry(tw *tar.Writer, name string, data []byte, mode os.FileMode, now time.Time) error {
	header := &tar.Header{
		Name:     name,
		Mode:     int64(mode.Perm()),
		Size:     int64(len(data)),
		ModTime:  now,
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("backup: write the header of %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	return nil
}

// listRegularFiles returns the names directly inside dir, sorted; a missing directory is empty.
func listRegularFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: read %s: %w", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
