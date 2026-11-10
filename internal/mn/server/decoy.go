package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/decoy"
)

// decoyHandler is the masking site: what a prober gets instead of the bare 404 a Go server answers by
// default.
//
// The node has served one of these from nginx since M3; the management server served nothing at `/`,
// which is itself a fingerprint — a host that answers every path with "404 page not found" and
// nothing else is not a website, it is a machine. Serving the same archetype the nodes do also makes
// the server look like its own product rather than a different one.
//
// The site is generated once at startup and held in memory: it is deterministic from (domain, seed,
// template), so there is no file to keep in step, nothing to serve stale, and no directory for an
// operator to edit by accident.
type decoyHandler struct {
	site  *decoy.Site
	etags map[string]string
}

func newDecoyHandler(site *decoy.Site) *decoyHandler {
	h := &decoyHandler{site: site, etags: make(map[string]string, len(site.Files))}
	for name, content := range site.Files {
		sum := sha256.Sum256(content)
		// A short, stable ETag per file: the same seed and template produce the same bytes on every
		// start, so a client's cache is right across restarts.
		h.etags[name] = `"` + hex.EncodeToString(sum[:8]) + `"`
	}
	return h
}

// ServeHTTP maps the request to a file of the site, falling back to the generated 404 page. Unknown
// paths answer 404 with HTML, because that is what a real site does.
func (h *decoyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == h.site.Auth.Path {
		h.auth(w, r)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	content, ok := h.site.Files[name]
	status := http.StatusOK
	if !ok {
		name = "404.html"
		content, ok = h.site.Files[name]
		status = http.StatusNotFound
		if !ok {
			http.NotFound(w, r)
			return
		}
	}

	w.Header().Set("ETag", h.etags[name])
	if isPage(name) {
		// A sign-in page that is cached is a page whose error message outlives the session.
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	if status == http.StatusNotFound {
		// ServeContent would answer 200: the page exists, the URL does not. The status has to be set
		// before anything is written, and the length spelled out because ServeContent is not doing it.
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = w.Write(content)
		}
		return
	}
	// ServeContent sets the content type from the name and answers If-None-Match from the ETag above,
	// so a repeat visitor gets 304 and the browser still checks.
	http.ServeContent(w, r, name, zeroTime, bytes.NewReader(content))
}

// auth is the fake sign-in endpoint the page posts to: the answer a portal gives a wrong password.
func (h *decoyHandler) auth(w http.ResponseWriter, r *http.Request) {
	if r.Method != h.site.Auth.Method {
		w.Header().Set("Allow", h.site.Auth.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", h.site.Auth.ContentType)
	if h.site.Auth.Cookie != "" {
		w.Header().Set("Set-Cookie", h.site.Auth.Cookie)
	}
	w.WriteHeader(h.site.Auth.Status)
	_, _ = w.Write([]byte(h.site.Auth.Body))
}

// isPage reports whether a file is HTML, which decides the caching policy.
func isPage(name string) bool {
	return strings.HasSuffix(name, ".html") || name == "robots.txt"
}

// zeroTime is passed to http.ServeContent for the site's files: a deterministically generated page has
// no mtime worth reporting, and omitting Last-Modified keeps the ETag as the only validator.
var zeroTime = time.Time{}
