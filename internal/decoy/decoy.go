// Package decoy generates the masking site: the ordinary-looking sign-in page that answers an active
// prober instead of a proxy.
//
// Generation is deterministic from (domain, seed, template). The seed is created once at install and
// kept in the state file, so the site the owner sees today is the site the owner sees after a reboot,
// a reinstall of the package or a `termit-uv renew-site` that nobody asked for. Nothing is fetched at
// runtime: every byte of the tree is produced here, and no page references an external host.
//
// The site is a file tree, and the two binaries serve it differently because they are different kinds
// of listener. A node writes the tree and nginx serves it — the agent never listens on :80 or :443
// (see AGENTS.md §5), so Site.Auth describes the sign-in answer and package web turns it into an nginx
// location. The management server owns its socket, so it generates the same site and serves it from
// memory (internal/mn/server/decoy.go): same generator, same determinism, no tree on disk.
package decoy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/atomicfile"
)

// Template names accepted by Spec.
const (
	// TemplatePortal is the branded enterprise portal: dark or light card, invented product,
	// deliberately non-standard vocabulary.
	TemplatePortal = "portal"
	// TemplateProduct is the polished single-product sign-in screen: light, wide card, brand row,
	// SSO shortcut and legal footer.
	TemplateProduct = "product"
)

// Templates lists the archetypes this build can generate.
func Templates() []string { return []string{TemplateProduct, TemplatePortal} }

// KnownTemplate reports whether name is one of the archetypes this build can generate.
func KnownTemplate(name string) bool { return knownTemplate(name) }

func knownTemplate(name string) bool {
	for _, candidate := range Templates() {
		if candidate == name {
			return true
		}
	}
	return false
}

// Spec is everything the generator is allowed to look at.
type Spec struct {
	// Domain is the node's own name. It appears in the page title, the footer and robots.txt.
	Domain string
	// Seed is the per-install randomness. It is hex in practice, but any non-empty string works.
	Seed string
	// Template selects the archetype.
	Template string
	// Now supplies the copyright year. Zero means time.Now.
	Now time.Time
}

// Validate reports whether the spec can be generated.
func (s Spec) Validate() error {
	switch {
	case strings.TrimSpace(s.Domain) == "":
		return errors.New("decoy: domain is required")
	case strings.ContainsAny(s.Domain, " /"):
		return fmt.Errorf("decoy: domain %q is not a host name", s.Domain)
	case strings.TrimSpace(s.Seed) == "":
		return errors.New("decoy: seed is required")
	case len(s.Seed) < 8:
		return errors.New("decoy: seed must be at least 8 characters, so two installs cannot collide")
	}
	if !knownTemplate(s.Template) {
		return fmt.Errorf("decoy: unknown template %q (known: %s)", s.Template, strings.Join(Templates(), ", "))
	}
	return nil
}

// AuthRoute is the sign-in endpoint: what the page posts to, and what the server answers.
type AuthRoute struct {
	// Path is the request path, unique to this deployment.
	Path string
	// Method is the only method the endpoint accepts.
	Method string
	// Status is the status it answers with.
	Status int
	// ContentType and Body are the response.
	ContentType string
	Body        string
	// Cookie is the Set-Cookie value, so the page behaves like a session that failed.
	Cookie string
}

// Site is a generated decoy, held in memory until it is written.
type Site struct {
	Template string
	// Files maps a slash-separated path to its content. The map is complete: anything in the
	// directory that is not in here is stale.
	Files map[string][]byte
	// Auth is the endpoint the page posts to.
	Auth AuthRoute
	// Digest covers the file paths and contents, so a re-run can tell whether anything changed.
	Digest string
}

// Generate builds one site.
func Generate(spec Spec) (*Site, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Now.IsZero() {
		spec.Now = time.Now()
	}
	rng := newRNG(spec)

	site := &Site{Template: spec.Template, Files: map[string][]byte{}}
	pages, err := render(spec, rng)
	if err != nil {
		return nil, err
	}
	site.Files["index.html"] = pages.index
	site.Files["404.html"] = pages.notFound
	site.Files["robots.txt"] = pages.robots
	site.Files["favicon.png"] = pages.favicon
	site.Files["assets/boot.js"] = bootJS
	site.Auth = pages.auth
	site.Digest = digestFiles(site.Files)
	return site, nil
}

// Write materialises the site under dir and removes files a previous generation left behind.
// It returns the sorted list of paths it wrote.
//
// New files are written before stale ones are removed: a visitor during the rewrite sees the new
// page, a page whose assets are briefly missing, or the old page — never a page that half exists
// because everything was deleted first.
func (s *Site) Write(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("decoy: create %s: %w", dir, err)
	}

	names := make([]string, 0, len(s.Files))
	for name := range s.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("decoy: create %s: %w", filepath.Dir(target), err)
		}
		if err := atomicfile.Write(target, s.Files[name], 0o644); err != nil {
			return nil, err
		}
	}
	if err := removeStale(dir, s.Files); err != nil {
		return nil, err
	}
	return names, nil
}

// ReadFile returns one generated file.
func (s *Site) ReadFile(name string) ([]byte, error) {
	data, ok := s.Files[name]
	if !ok {
		return nil, fmt.Errorf("decoy: %s is not part of the site", name)
	}
	return data, nil
}

// removeStale deletes everything under dir that is not in keep, then prunes empty directories.
func removeStale(dir string, keep map[string][]byte) error {
	var staleFiles, staleDirs []string
	err := fs.WalkDir(os.DirFS(dir), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name != "." {
				staleDirs = append(staleDirs, name)
			}
			return nil
		}
		if _, ok := keep[name]; !ok {
			staleFiles = append(staleFiles, name)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("decoy: scan %s: %w", dir, err)
	}
	for _, name := range staleFiles {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("decoy: remove stale %s: %w", name, err)
		}
	}
	// Deepest first, so a directory whose only content was stale becomes empty before it is tried.
	sort.Sort(sort.Reverse(sort.StringSlice(staleDirs)))
	for _, name := range staleDirs {
		// A directory that still holds a kept file cannot be removed; that is the guard wanted.
		_ = os.Remove(filepath.Join(dir, filepath.FromSlash(name)))
	}
	return nil
}

func digestFiles(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		fmt.Fprintf(digest, "%s\x00%s\n", name, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(digest.Sum(nil))
}
