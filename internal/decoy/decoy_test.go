package decoy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/decoy"
)

var fixedTime = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func specFor(template, seed string) decoy.Spec {
	return decoy.Spec{
		Domain:   "vpn.example.com",
		Seed:     seed,
		Template: template,
		Now:      fixedTime,
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	t.Parallel()
	for _, template := range decoy.Templates() {
		first, err := decoy.Generate(specFor(template, "0011aabbccddeeff"))
		if err != nil {
			t.Fatalf("%s: Generate: %v", template, err)
		}
		second, err := decoy.Generate(specFor(template, "0011aabbccddeeff"))
		if err != nil {
			t.Fatalf("%s: Generate: %v", template, err)
		}
		if first.Digest != second.Digest {
			t.Errorf("%s: digest differs between two identical generations", template)
		}
		if len(first.Files) != len(second.Files) {
			t.Fatalf("%s: file count %d vs %d", template, len(first.Files), len(second.Files))
		}
		for name, data := range first.Files {
			if !bytes.Equal(data, second.Files[name]) {
				t.Errorf("%s: %s differs between two identical generations", template, name)
			}
		}
	}
}

func TestGenerateVariesBySeedAndTemplate(t *testing.T) {
	t.Parallel()
	base, err := decoy.Generate(specFor(decoy.TemplatePortal, "0011aabbccddeeff"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	otherSeed, err := decoy.Generate(specFor(decoy.TemplatePortal, "ffeeddccbbaa1100"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if base.Digest == otherSeed.Digest {
		t.Error("two seeds produced the same site")
	}
	if bytes.Equal(base.Files["index.html"], otherSeed.Files["index.html"]) {
		t.Error("two seeds produced the same page")
	}
	otherTemplate, err := decoy.Generate(specFor(decoy.TemplateProduct, "0011aabbccddeeff"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if bytes.Equal(base.Files["index.html"], otherTemplate.Files["index.html"]) {
		t.Error("two templates produced the same page")
	}
}

func TestGenerateRejectsBadSpec(t *testing.T) {
	t.Parallel()
	cases := map[string]decoy.Spec{
		"no domain":         {Seed: "0011aabb", Template: decoy.TemplatePortal},
		"no seed":           {Domain: "vpn.example.com", Template: decoy.TemplatePortal},
		"short seed":        {Domain: "vpn.example.com", Seed: "abc", Template: decoy.TemplatePortal},
		"unknown template":  {Domain: "vpn.example.com", Seed: "0011aabb", Template: "marketing"},
		"domain with space": {Domain: "vpn example.com", Seed: "0011aabb", Template: decoy.TemplatePortal},
	}
	for name, spec := range cases {
		if _, err := decoy.Generate(spec); err == nil {
			t.Errorf("%s: Generate accepted the spec", name)
		}
	}
}

// The three tests below enforce the hard rules from DESCRIPTION.md §5.3: a decoy that fetches
// anything, or that is visibly machine-generated, is a fingerprint.

func TestNoExternalReferences(t *testing.T) {
	t.Parallel()
	forbidden := []string{"http://", "https://", "//fonts.", "@import", "preconnect", "dns-prefetch", "integrity=", "crossorigin"}
	for _, template := range decoy.Templates() {
		for _, seed := range seeds(12) {
			site, err := decoy.Generate(specFor(template, seed))
			if err != nil {
				t.Fatalf("%s/%s: Generate: %v", template, seed, err)
			}
			for name, data := range site.Files {
				text := strings.ToLower(string(data))
				for _, marker := range forbidden {
					if strings.Contains(text, marker) {
						t.Errorf("%s/%s: %s contains %q", template, seed, name, marker)
					}
				}
			}
		}
	}
}

func TestNoGeneratorShapedIdentifiers(t *testing.T) {
	t.Parallel()
	shaped := []*regexp.Regexp{
		regexp.MustCompile(`\b[fbu]_[0-9a-f]{6,}`),
		regexp.MustCompile(`\bcsrf_[0-9a-f]{4,}`),
		regexp.MustCompile(`\bgen[0-9a-f]{8,}`),
	}
	for _, template := range decoy.Templates() {
		for _, seed := range seeds(12) {
			site, err := decoy.Generate(specFor(template, seed))
			if err != nil {
				t.Fatalf("%s/%s: Generate: %v", template, seed, err)
			}
			for name, data := range site.Files {
				for _, pattern := range shaped {
					if match := pattern.FindString(string(data)); match != "" {
						t.Errorf("%s/%s: %s contains the generated-looking identifier %q", template, seed, name, match)
					}
				}
			}
		}
	}
}

func TestSubmitPathVariesPerDeployment(t *testing.T) {
	t.Parallel()
	counts := map[string]int{}
	for _, seed := range seeds(60) {
		site, err := decoy.Generate(specFor(decoy.TemplatePortal, seed))
		if err != nil {
			t.Fatalf("%s: Generate: %v", seed, err)
		}
		path := site.Auth.Path
		if !regexp.MustCompile(`^/[a-z0-9/_-]+$`).MatchString(path) {
			t.Errorf("auth path %q is not a plausible request path", path)
		}
		counts[path]++
		// The form must actually post there, otherwise the endpoint is decoration.
		if !strings.Contains(string(site.Files["index.html"]), `action="`+path+`"`) {
			t.Errorf("%s: the form does not post to %q", seed, path)
		}
	}
	if len(counts) < 6 {
		t.Errorf("only %d distinct submit paths over 60 seeds: %v", len(counts), counts)
	}
	worst := 0
	for _, count := range counts {
		if count > worst {
			worst = count
		}
	}
	if worst*10 > len(seeds(60))*3 {
		t.Errorf("one submit path is used by %d of %d deployments", worst, len(seeds(60)))
	}
}

func TestAuthRouteIsPlausible(t *testing.T) {
	t.Parallel()
	site, err := decoy.Generate(specFor(decoy.TemplatePortal, "0011aabbccddeeff"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	auth := site.Auth
	if auth.Method != "POST" {
		t.Errorf("methods = %q, want POST", auth.Method)
	}
	if auth.Status != 401 {
		t.Errorf("status = %d, want 401", auth.Status)
	}
	if auth.ContentType != "application/json" {
		t.Errorf("content type = %q", auth.ContentType)
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(auth.Body), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", auth.Body, err)
	}
	if body.Error == "" || body.Message == "" {
		t.Errorf("body %q lacks an error or a message", auth.Body)
	}
	if !regexp.MustCompile(`^[a-z_]+=[0-9a-f]{32}; Path=/; HttpOnly; SameSite=Lax$`).MatchString(auth.Cookie) {
		t.Errorf("cookie %q does not look like a session cookie", auth.Cookie)
	}
}

func TestLayoutVariesAcrossSeeds(t *testing.T) {
	t.Parallel()
	widths := map[string]bool{}
	layouts := map[string]bool{}
	for _, seed := range seeds(24) {
		site, err := decoy.Generate(specFor(decoy.TemplatePortal, seed))
		if err != nil {
			t.Fatalf("%s: Generate: %v", seed, err)
		}
		page := string(site.Files["index.html"])
		if match := regexp.MustCompile(`--w:(\d+)px`).FindStringSubmatch(page); match != nil {
			widths[match[1]] = true
		}
		if match := regexp.MustCompile(`data-layout="([a-z]+)"`).FindStringSubmatch(page); match != nil {
			layouts[match[1]] = true
		}
	}
	if len(widths) < 3 {
		t.Errorf("card width took %d values over 24 seeds", len(widths))
	}
	if len(layouts) < 2 {
		t.Errorf("layout took %d values over 24 seeds", len(layouts))
	}
}

func TestFaviconIsAValidDeterministicPNG(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, seed := range seeds(8) {
		site, err := decoy.Generate(specFor(decoy.TemplatePortal, seed))
		if err != nil {
			t.Fatalf("%s: Generate: %v", seed, err)
		}
		icon := site.Files["favicon.png"]
		config, err := png.DecodeConfig(bytes.NewReader(icon))
		if err != nil {
			t.Fatalf("%s: favicon is not a PNG: %v", seed, err)
		}
		if config.Width != 32 || config.Height != 32 {
			t.Errorf("%s: favicon is %dx%d, want 32x32", seed, config.Width, config.Height)
		}
		if previous, ok := seen[string(icon)]; ok {
			t.Logf("seeds %s and %s share an icon; not ideal but not a defect", previous, seed)
		}
		seen[string(icon)] = seed
	}

	again, err := decoy.Generate(specFor(decoy.TemplatePortal, seeds(1)[0]))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	first, err := decoy.Generate(specFor(decoy.TemplatePortal, seeds(1)[0]))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !bytes.Equal(again.Files["favicon.png"], first.Files["favicon.png"]) {
		t.Error("the favicon is not deterministic")
	}
	// The icon is derived from the seed, so most deployments must not look identical.
	if len(seen)*4 < 8*3 {
		t.Errorf("only %d distinct icons across 8 seeds: %d", len(seen), 8)
	}
}

func TestRobotsTxtHidesInternalPaths(t *testing.T) {
	t.Parallel()
	site, err := decoy.Generate(specFor(decoy.TemplateProduct, "0011aabbccddeeff"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	robots := string(site.Files["robots.txt"])
	if !strings.Contains(robots, "User-agent: *") {
		t.Errorf("robots.txt has no user agent line:\n%s", robots)
	}
	if !strings.Contains(robots, "Disallow: /") {
		t.Errorf("robots.txt disallows nothing:\n%s", robots)
	}
}

// seeds returns n distinct hex seeds of the length a real install uses.
func seeds(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%016x", 0x5eed0000+i))
	}
	return out
}

func TestWriteRemovesStaleFiles(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "site")
	first, err := decoy.Generate(specFor(decoy.TemplatePortal, "0011aabbccddeeff"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := first.Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	stale := filepath.Join(dir, "assets", "old-chunk.js")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "legacy"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	second, err := decoy.Generate(specFor(decoy.TemplateProduct, "ffeeddccbbaa1100"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := second.Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale file survived regeneration")
	}
	if _, err := os.Stat(filepath.Join(dir, "legacy")); !os.IsNotExist(err) {
		t.Error("a stale directory survived regeneration")
	}
	for name := range second.Files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
}

func TestReadFile(t *testing.T) {
	t.Parallel()
	site, err := decoy.Generate(specFor(decoy.TemplatePortal, "0011aabbccddeeff"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := site.ReadFile("index.html"); err != nil {
		t.Errorf("ReadFile(index.html): %v", err)
	}
	if _, err := site.ReadFile("nope.html"); err == nil {
		t.Error("ReadFile accepted a path that is not part of the site")
	}
}
