package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/shogun-kub/termit/internal/decoy"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// decoySeedKey is where the masking site's seed lives. It is a database setting rather than a config
// key because it is generated once, never by hand: an operator who could edit it would be able to make
// the site change under a prober's feet, which is the opposite of what the site is for.
const decoySeedKey = "decoy.seed"

// decoySeed returns this server's seed, minting one on first use.
//
// An install that predates the decoy has no seed, and generating a fresh one per start would mean a
// different website on every restart — a worse signal than no site at all. So it is created once and
// stored, and `termit-mn init` reaches it through the same function.
func decoySeed(ctx context.Context, db *store.Store) (string, error) {
	seed, err := db.GetSetting(ctx, decoySeedKey)
	if err == nil && seed != "" {
		return seed, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("read %s: %w", decoySeedKey, err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("seed the decoy: %w", err)
	}
	seed = hex.EncodeToString(buf)
	if err := db.SetSetting(ctx, decoySeedKey, seed); err != nil {
		return "", fmt.Errorf("store %s: %w", decoySeedKey, err)
	}
	return seed, nil
}

// decoySite builds the site the server answers a prober with. It is deterministic from the domain,
// the stored seed and the template, so the same server serves the same site on every start.
func decoySite(ctx context.Context, cfg *config.Config, db *store.Store) (*decoy.Site, error) {
	seed, err := decoySeed(ctx, db)
	if err != nil {
		return nil, err
	}
	site, err := decoy.Generate(decoy.Spec{
		Domain:   cfg.Domain,
		Seed:     seed,
		Template: cfg.Decoy.Template,
	})
	if err != nil {
		return nil, fmt.Errorf("generate the masking site: %w", err)
	}
	return site, nil
}
