package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/decoy"
	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/server"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// decoyFixture builds a handler that serves the masking site, with a fixed seed so the assertions can
// name what the page says.
func decoyFixture(t *testing.T) (http.Handler, *decoy.Site) {
	t.Helper()
	site, err := decoy.Generate(decoy.Spec{
		Domain: "mn.example.com", Seed: "0123456789abcdef", Template: decoy.TemplatePortal,
	})
	if err != nil {
		t.Fatalf("decoy.Generate: %v", err)
	}

	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/termit.db"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := enroll.New(enroll.Options{
		Store: db, Mode: config.ModeInvite, Resolvers: []enroll.Resolver{staticResolver{}},
		Prober: okProber{}, Certs: okCerts{}, Master: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("enroll.New: %v", err)
	}
	handler, err := server.New(server.Options{
		Store: db, Enrollment: service, Domain: "mn.example.com", APIPrefix: "/k3f9x2",
		MasterKey: []byte("0123456789abcdef0123456789abcdef"), Decoy: site,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler, site
}

func get(handler http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

// The point of the site: a visitor asking for the root of a host that is supposed to look ordinary gets
// an ordinary page, not Go's plain-text 404.
func TestDecoyServesTheSiteAtTheRoot(t *testing.T) {
	t.Parallel()
	handler, site := decoyFixture(t)
	recorder := get(handler, "/")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d", recorder.Code)
	}
	if kind := recorder.Header().Get("Content-Type"); !strings.HasPrefix(kind, "text/html") {
		t.Errorf("content type = %q", kind)
	}
	if recorder.Body.String() != string(site.Files["index.html"]) {
		t.Errorf("the root is not the generated page")
	}
	if recorder.Header().Get("ETag") == "" {
		t.Error("no ETag on the page")
	}
}

// A real site has a real 404, with its own look: that is what an unknown path must return, and it must
// not be mistaken for a healthy page.
func TestDecoyUnknownPathIsTheSitesOwn404(t *testing.T) {
	t.Parallel()
	handler, site := decoyFixture(t)
	recorder := get(handler, "/nothing/here")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /nothing/here = %d", recorder.Code)
	}
	if kind := recorder.Header().Get("Content-Type"); !strings.HasPrefix(kind, "text/html") {
		t.Errorf("content type = %q, want the site's HTML page", kind)
	}
	if recorder.Body.String() != string(site.Files["404.html"]) {
		t.Errorf("the 404 body is not the generated page")
	}
}

// The files a browser asks for on its own must exist, or the site looks generated.
func TestDecoyServesTheHouseholdFiles(t *testing.T) {
	t.Parallel()
	handler, site := decoyFixture(t)
	for path, kind := range map[string]string{
		"/robots.txt":     "text/plain",
		"/favicon.png":    "image/png",
		"/assets/boot.js": "",
	} {
		recorder := get(handler, path)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, recorder.Code)
			continue
		}
		if got := recorder.Header().Get("Content-Type"); kind != "" && !strings.HasPrefix(got, kind) {
			t.Errorf("GET %s content type = %q, want %q", path, got, kind)
		}
		if recorder.Body.Len() == 0 {
			t.Errorf("GET %s served nothing", path)
		}
	}
	// And the file is the generated one, not something else with the right name.
	if body := get(handler, "/robots.txt").Body.String(); body != string(site.Files["robots.txt"]) {
		t.Errorf("robots.txt is not the generated one")
	}
}

// The page posts to a sign-in endpoint. It has to answer like one, or the page is a dead end that a
// curious prober can spot in a minute.
func TestDecoyAnswersItsSignInEndpoint(t *testing.T) {
	t.Parallel()
	handler, site := decoyFixture(t)

	req := httptest.NewRequest(http.MethodPost, site.Auth.Path, strings.NewReader("{}"))
	req.RemoteAddr = "203.0.113.10:5555"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != site.Auth.Status {
		t.Errorf("POST %s = %d, want %d", site.Auth.Path, recorder.Code, site.Auth.Status)
	}
	if got := recorder.Body.String(); got != site.Auth.Body {
		t.Errorf("body = %q, want %q", got, site.Auth.Body)
	}
	if got := recorder.Header().Get("Set-Cookie"); got != site.Auth.Cookie {
		t.Errorf("cookie = %q, want %q", got, site.Auth.Cookie)
	}

	// A GET on it is not a page, and saying so is what a real endpoint does.
	if got := get(handler, site.Auth.Path); got.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s = %d, want 405", site.Auth.Path, got.Code)
	}
}

// Two visitors must be able to use their caches, and the second one must be cheap.
func TestDecoyAnswersConditionalRequests(t *testing.T) {
	t.Parallel()
	handler, _ := decoyFixture(t)
	first := get(handler, "/")
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.10:5555"
	req.Header.Set("If-None-Match", etag)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", recorder.Code)
	}
}

// The API keeps winning over the site: the prefix is not a page, and an old path is not either.
func TestDecoyDoesNotSwallowTheAPI(t *testing.T) {
	t.Parallel()
	handler, _ := decoyFixture(t)
	if got := get(handler, "/k3f9x2/healthz"); got.Code != http.StatusOK {
		t.Errorf("%s/healthz = %d", "/k3f9x2", got.Code)
	}
	if got := get(handler, "/api/v1/enroll"); got.Header().Get("Content-Type") == "application/json" {
		t.Errorf("/api/v1/enroll still answers as the API")
	}
}

// A handler built without a site keeps the old behaviour: the decoy is a feature, not a change of
// contract for every test and every deployment that turns it off.
func TestNoDecoyMeansTheOld404(t *testing.T) {
	t.Parallel()
	handler := prefixHandler(t, "/k3f9x2")
	recorder := get(handler, "/")
	if recorder.Code != http.StatusNotFound {
		t.Errorf("GET / = %d, want 404", recorder.Code)
	}
	if strings.Contains(recorder.Header().Get("Content-Type"), "html") {
		t.Errorf("content type = %q, want the plain default", recorder.Header().Get("Content-Type"))
	}
}
