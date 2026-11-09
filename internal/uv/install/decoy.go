package install

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/shogun-kub/termit/internal/uv/decoy"
	"github.com/shogun-kub/termit/internal/uv/state"
	"github.com/shogun-kub/termit/internal/uv/web"
)

// decoyStep writes the masking site and the nginx fragment that serves its sign-in form.
type decoyStep struct{ e *stepEnv }

func (*decoyStep) Name() string { return "decoy" }

// generate builds the site from the persisted seed. Nothing here reads the clock except the
// copyright year, which is why the digest only changes when the content does.
func (s *decoyStep) generate() (*decoy.Site, error) {
	return decoy.Generate(decoy.Spec{
		Domain:   s.e.cfg.Domain,
		Seed:     s.e.st.Decoy.Seed,
		Template: s.e.cfg.Decoy.Template,
		Now:      s.e.now(),
	})
}

func (s *decoyStep) Check(context.Context) (CheckResult, error) {
	switch {
	case s.e.st.Decoy.Seed == "":
		return CheckResult{Detail: "no decoy seed has been generated"}, nil
	case s.e.st.Decoy.Digest == "":
		return CheckResult{Detail: "the site has not been generated"}, nil
	case s.e.st.Decoy.Template != s.e.cfg.Decoy.Template:
		return CheckResult{Detail: fmt.Sprintf("the site was generated from template %q, configuration says %q",
			s.e.st.Decoy.Template, s.e.cfg.Decoy.Template)}, nil
	}

	site, err := s.generate()
	if err != nil {
		return CheckResult{}, err
	}
	if site.Digest != s.e.st.Decoy.Digest {
		return CheckResult{Detail: "the generated content no longer matches the recorded digest"}, nil
	}
	for _, name := range []string{"index.html", "404.html", "favicon.png", "robots.txt"} {
		if _, err := os.Stat(filepath.Join(s.e.cfg.Paths.SiteDir, filepath.FromSlash(name))); err != nil {
			return CheckResult{Detail: name + " is missing from " + s.e.cfg.Paths.SiteDir}, nil
		}
	}
	if _, err := os.Stat(s.e.cfg.Paths.DecoySnippet); err != nil {
		return CheckResult{Detail: "the nginx fragment is missing at " + s.e.cfg.Paths.DecoySnippet}, nil
	}
	return CheckResult{Satisfied: true, Detail: fmt.Sprintf("%s site, %d files", site.Template, len(site.Files))}, nil
}

func (s *decoyStep) Run(context.Context) error {
	site, err := s.generate()
	if err != nil {
		return err
	}
	files, err := site.Write(s.e.cfg.Paths.SiteDir)
	if err != nil {
		return err
	}
	snippet, err := web.DecoySnippet(site)
	if err != nil {
		return err
	}
	if err := state.WriteFileAtomic(s.e.cfg.Paths.DecoySnippet, snippet, 0o644); err != nil {
		return err
	}

	now := s.e.now()
	s.e.st.AddPath(s.e.cfg.Paths.SiteDir, state.KindDir, "decoy", now)
	for _, name := range files {
		s.e.st.AddPath(filepath.Join(s.e.cfg.Paths.SiteDir, filepath.FromSlash(name)), state.KindFile, "decoy", now)
	}
	s.e.st.AddPath(path.Dir(s.e.cfg.Paths.DecoySnippet), state.KindDir, "decoy", now)
	s.e.st.AddPath(s.e.cfg.Paths.DecoySnippet, state.KindFile, "decoy", now)

	s.e.st.Decoy.Template = s.e.cfg.Decoy.Template
	s.e.st.Decoy.Digest = site.Digest
	s.e.st.Touch()

	s.e.log.Info("generated decoy site",
		"template", site.Template,
		"files", len(site.Files),
		"digest", shortDigest(site.Digest),
		"form", site.Auth.Path,
		"dir", s.e.cfg.Paths.SiteDir)
	return nil
}

// shortDigest is the first eight characters of a hex digest, enough to compare by eye.
func shortDigest(digest string) string {
	if len(digest) <= 8 {
		return digest
	}
	return digest[:8]
}
