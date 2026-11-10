package main

import (
	"context"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// The seed is the whole of the site's stability: minting a new one per start would show a prober a
// different website on every restart, which is worse than no website at all.
func TestDecoySeedIsMintedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	first, err := decoySeed(ctx, db)
	if err != nil {
		t.Fatalf("decoySeed: %v", err)
	}
	if len(first) < 32 {
		t.Errorf("seed %q is shorter than 32 characters", first)
	}
	second, err := decoySeed(ctx, db)
	if err != nil {
		t.Fatalf("decoySeed: %v", err)
	}
	if first != second {
		t.Errorf("the seed changed between calls: %q then %q", first, second)
	}
	// And it is in the database, where a restart will find it.
	stored, err := db.GetSetting(ctx, decoySeedKey)
	if err != nil || stored != first {
		t.Errorf("stored seed = %q (%v), want %q", stored, err, first)
	}
}

// The site is deterministic from the domain, the seed and the template, so two servers with the same
// configuration must serve the same bytes — that is what makes it a decoy and not a random page.
func TestDecoySiteIsDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Default()
	cfg.Domain = "mn.example.com"
	cfg.Decoy.Template = "portal"
	first, err := decoySite(ctx, cfg, db)
	if err != nil {
		t.Fatalf("decoySite: %v", err)
	}
	second, err := decoySite(ctx, cfg, db)
	if err != nil {
		t.Fatalf("decoySite: %v", err)
	}
	if first.Digest != second.Digest {
		t.Errorf("the site changed between two starts: %s then %s", first.Digest, second.Digest)
	}

	// A different template is a different site; the seed stays where it is.
	cfg.Decoy.Template = "product"
	other, err := decoySite(ctx, cfg, db)
	if err != nil {
		t.Fatalf("decoySite: %v", err)
	}
	if other.Digest == first.Digest {
		t.Errorf("changing the template did not change the site")
	}
}

// init seeds the site it will serve, and says which archetype it chose.
func TestInitSeedsTheMaskingSite(t *testing.T) {
	t.Parallel()
	out, code := initIn(t)
	if code != exitOK {
		t.Fatalf("init = %d:\n%s", code, out)
	}
	if !strings.Contains(out, "masking site: "+config.DefaultDecoyTemplate) {
		t.Errorf("init did not report the masking site:\n%s", out)
	}
	chosen, code := initIn(t, "--decoy-template", "product")
	if code != exitOK {
		t.Fatalf("init with a template = %d:\n%s", code, chosen)
	}
	if !strings.Contains(chosen, "masking site: product") {
		t.Errorf("init ignored --decoy-template:\n%s", chosen)
	}
	refused, code := initIn(t, "--decoy-template", "wiki")
	if code == exitOK {
		t.Fatalf("init accepted an unknown template:\n%s", refused)
	}
}
