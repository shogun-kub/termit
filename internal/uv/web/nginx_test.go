package web_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/uv/decoy"
	"github.com/shogun-kub/termit/internal/uv/web"
)

var update = flag.Bool("update", false, "rewrite the golden file")

func testSite(t *testing.T) *decoy.Site {
	t.Helper()
	site, err := decoy.Generate(decoy.Spec{
		Domain:   "vpn.example.com",
		Seed:     "0011aabbccddeeff",
		Template: decoy.TemplatePortal,
		Now:      time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return site
}

// TestDecoySnippetGolden pins the fragment that answers the page's form. The fragment and the page
// come from one decoy.Site, so this test and the page's golden test fail together if they ever drift.
func TestDecoySnippetGolden(t *testing.T) {
	t.Parallel()
	site := testSite(t)
	snippet, err := web.DecoySnippet(site)
	if err != nil {
		t.Fatalf("DecoySnippet: %v", err)
	}

	for _, want := range []string{
		"location = " + site.Auth.Path,
		`return ` + itoa(site.Auth.Status),
		"Set-Cookie",
		"$request_id",
	} {
		if !strings.Contains(string(snippet), want) {
			t.Errorf("fragment does not contain %q:\n%s", want, snippet)
		}
	}
	if strings.Contains(string(snippet), "proxy_pass") {
		t.Error("the decoy fragment must not proxy anywhere: nginx answers the form itself")
	}

	golden := filepath.Join("testdata", "decoy.conf")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(golden, snippet, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden) // #nosec G304 -- a fixed test path.
	if err != nil {
		t.Fatalf("read golden file: %v (run `go test ./internal/uv/web -update`)", err)
	}
	if !bytes.Equal(want, snippet) {
		t.Errorf("%s differs from the rendered fragment; regenerate with -update and review the diff", golden)
	}
}

func TestDecoySnippetRejectsUnsafeRoutes(t *testing.T) {
	t.Parallel()
	valid := decoy.AuthRoute{Path: "/session/verify", Method: "POST", Status: 401, ContentType: "application/json", Body: `{"error":"x"}`, Cookie: "sid=00; Path=/"}
	cases := map[string]decoy.AuthRoute{
		"relative path":   {Path: "session/verify", Method: "POST", Body: "{}", Cookie: "sid=00"},
		"space in path":   {Path: "/session verify", Method: "POST", Body: "{}", Cookie: "sid=00"},
		"method":          {Path: "/x", Method: "PUT", Body: "{}", Cookie: "sid=00"},
		"quote in body":   {Path: "/x", Method: "POST", Body: `{"error":"it's"}`, Cookie: "sid=00"},
		"newline in body": {Path: "/x", Method: "POST", Body: "{\"a\":1}\n}", Cookie: "sid=00"},
		"quote in cookie": {Path: "/x", Method: "POST", Body: "{}", Cookie: `sid="00"`},
	}
	for name, route := range cases {
		if _, err := web.DecoySnippet(&decoy.Site{Auth: route}); err == nil {
			t.Errorf("%s: DecoySnippet accepted the route", name)
		}
	}
	if _, err := web.DecoySnippet(&decoy.Site{Auth: valid}); err != nil {
		t.Errorf("DecoySnippet rejected a valid route: %v", err)
	}
	if _, err := web.DecoySnippet(nil); err == nil {
		t.Error("DecoySnippet accepted a nil site")
	}
}

func itoa(value int) string {
	if value == 401 {
		return "401"
	}
	return "0"
}
