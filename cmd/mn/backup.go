package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/shogun-kub/termit/internal/mn/backup"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// runBackup writes the archive an operator can rebuild the server from.
func runBackup(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "path to config.yaml")
	out := flags.String("out", "", "archive path (default: <data_dir>/backups/termit-<timestamp>.tar.gz)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "mn backup: %v\n", err)
		return exitFailure
	}
	if *out == "" {
		*out = filepath.Join(cfg.DataDir, "backups",
			"termit-"+time.Now().UTC().Format("20060102T150405Z")+".tar.gz")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// A read-only open is enough for VACUUM INTO and cannot migrate the database by accident.
	db, err := store.OpenReadOnly(ctx, cfg.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "mn backup: %v\n", err)
		return exitFailure
	}
	defer func() { _ = db.Close() }()

	result, err := backup.Create(ctx, backup.Options{
		Config:     cfg,
		Store:      db,
		ConfigPath: *configPath,
		Out:        *out,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mn backup: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "archive:  %s\n", result.Path)
	fmt.Fprintf(stdout, "bytes:    %d\n", result.Bytes)
	fmt.Fprintf(stdout, "domain:   %s\n", result.Manifest.Domain)
	fmt.Fprintf(stdout, "schema:   %d\n", result.Manifest.SchemaVersion)
	fmt.Fprintf(stdout, "entries:  %d\n", len(result.Manifest.Files))
	fmt.Fprintln(stdout, "the archive holds the master key: treat it like the key itself")
	return exitOK
}

// runRestore rebuilds a server from an archive.
func runRestore(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultConfigPath, "where config.yaml is restored")
	dataDir := flags.String("data-dir", "", "where the database and certificates are restored")
	masterKey := flags.String("master-key", "", "where the master key is restored (default: beside the config)")
	from := flags.String("from", "", "archive path")
	force := flags.Bool("force", false, "replace an existing database")
	if err := flags.Parse(flagsFirst(args, map[string]bool{"-force": true, "--force": true})); err != nil {
		return exitUsage
	}
	archive := *from
	if archive == "" {
		if flags.NArg() != 1 {
			fmt.Fprintln(stderr, "mn restore: exactly one archive path is required (as --from or as the argument)")
			return exitUsage
		}
		archive = flags.Arg(0)
	} else if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "mn restore: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}

	if *dataDir == "" {
		// The default layout is what `mn init` writes; anything else is passed explicitly.
		*dataDir = "/var/lib/termit/mn"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	manifest, err := backup.Restore(ctx, backup.RestoreOptions{
		Archive:       archive,
		ConfigPath:    *configPath,
		DataDir:       *dataDir,
		MasterKeyPath: *masterKey,
		Force:         *force,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mn restore: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "restored: %s\n", archive)
	fmt.Fprintf(stdout, "domain:   %s\n", manifest.Domain)
	fmt.Fprintf(stdout, "created:  %s\n", manifest.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(stdout, "config:   %s\n", *configPath)
	fmt.Fprintf(stdout, "data:     %s\n", *dataDir)
	if _, err := os.Stat(filepath.Join(*dataDir, "termit.db")); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "mn restore: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(stdout, "run `mn serve` to bring the server back up")
	return exitOK
}
