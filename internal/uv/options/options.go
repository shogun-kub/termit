// Package options renders the node owner's private page: every client link this node can hand out, in
// one place, behind a path only the owner knows.
//
// The page is a capability, like the subscription token: whoever has the URL has the links. So the
// path is long, random and generated once, the page says nothing about where it came from, asks for
// nothing from the network at render time, and is served from the same tree as the decoy site so that
// nginx needs no new location to expose it.
package options

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// TokenBytes is the entropy behind the page's path segment. 16 bytes is what the node's XHTTP path
// and the subscription token use; a page that lists every link should not be easier to find than the
// links themselves.
const TokenBytes = 16

// Link is one client configuration: a label a person recognises and the URI a client imports.
type Link struct {
	Label string
	URI   string
	// Degraded marks a mesh peer the server currently considers degraded. It is a hint, not a
	// verdict: the client still connects, the server is just less sure it should.
	Degraded bool
}

// Data is everything the page shows. It is a value: the same data always renders the same page, apart
// from Generated, which the caller controls.
type Data struct {
	Domain string
	Token  string
	// PageURL is the address the owner should keep; it is printed on the page too, so a screenshot
	// still tells them how to open it again.
	PageURL string
	// Local is this node's own device. It is what survives a management server that cannot be
	// reached, so it is always present.
	Local Link
	// Peers is the mesh as the subscription last described it. Empty when the node is not enrolled
	// or the subscription could not be fetched.
	Peers []Link
	// Warning explains an empty or stale Peers list in one sentence. Render shows it above the mesh
	// section instead of silently pretending the mesh is empty.
	Warning string
	// SubscriptionURL and JSONURL are the two forms of the same peer list. They carry the node's
	// token, which is why they belong on this page and nowhere else.
	SubscriptionURL string
	JSONURL         string
	Generated       time.Time
}

// NewToken returns a fresh path segment: hexadecimal, so it survives every URL bar and shell.
func NewToken() (string, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("options: generate a page token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ValidToken reports whether value looks like something NewToken produced. It is what stops a
// hand-edited state file from putting a path traversal or a query into the URL.
func ValidToken(value string) bool {
	if len(value) != TokenBytes*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// PageURL is the address of the page for a node that answers on domain.
func PageURL(domain, token string) string {
	return "https://" + domain + "/" + token + "/"
}

// ParseList reads the base64 body of a subscription into links.
//
// It deliberately shares nothing with the management server's renderer: the page is a client of the
// subscription, and reading it exactly as a client would is what keeps the two from drifting apart
// without one of them failing first.
func ParseList(body []byte) ([]Link, error) {
	text := strings.TrimSpace(string(body))
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("options: the subscription is not base64: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(decoded), "\n"), "\n")
	links := make([]Link, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		link := Link{URI: line}
		if hash := strings.IndexByte(line, '#'); hash >= 0 {
			link.Label = line[hash+1:]
			link.Degraded = strings.HasSuffix(link.Label, " (degraded)")
		}
		if link.Label == "" {
			link.Label = line
		}
		links = append(links, link)
	}
	return links, nil
}

// pageTemplate is self-contained on purpose, with the same rule as the decoy site: no external host,
// no font, no analytics. A page that phones home is a fingerprint of the node that serves it.
var pageTemplate = template.Must(template.New("options").Funcs(template.FuncMap{
	"when": func(t time.Time) string {
		if t.IsZero() {
			return "unknown"
		}
		return t.UTC().Format("2006-01-02 15:04 UTC")
	},
	"count": func(links []Link) int { return len(links) },
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<title>Client options — {{.Domain}}</title>
<style>
:root { color-scheme: light dark; --fg: #16181d; --dim: #5d6470; --bg: #f6f7f9; --card: #ffffff; --line: #dfe3e9; --accent: #2f5fd0; --warn: #8a5a00; }
@media (prefers-color-scheme: dark) { :root { --fg: #e7e9ee; --dim: #9aa3b2; --bg: #14161b; --card: #1c1f26; --line: #2c313b; --accent: #7aa2ff; --warn: #e0b45c; } }
* { box-sizing: border-box; }
body { margin: 0; padding: 2rem 1rem 4rem; background: var(--bg); color: var(--fg);
  font: 15px/1.5 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
main { max-width: 52rem; margin: 0 auto; }
h1 { font-size: 1.35rem; margin: 0 0 .25rem; }
h2 { font-size: 1rem; margin: 2rem 0 .75rem; text-transform: uppercase; letter-spacing: .06em; color: var(--dim); }
p.meta { margin: 0 0 1.5rem; color: var(--dim); font-size: .875rem; }
section { background: var(--card); border: 1px solid var(--line); border-radius: .6rem; padding: 1rem 1.15rem; }
ul.links { list-style: none; margin: 0; padding: 0; display: grid; gap: .9rem; }
li.link { border-top: 1px solid var(--line); padding-top: .9rem; }
li.link:first-child { border-top: 0; padding-top: 0; }
.label { display: flex; flex-wrap: wrap; gap: .5rem; align-items: baseline; margin-bottom: .35rem; }
.name { font-weight: 600; }
.tag { font-size: .75rem; color: var(--warn); border: 1px solid currentColor; border-radius: 999px; padding: 0 .45rem; }
.uri { display: flex; gap: .5rem; align-items: start; }
code { flex: 1 1 auto; min-width: 0; font: .8rem/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  background: var(--bg); border: 1px solid var(--line); border-radius: .4rem; padding: .5rem .6rem;
  word-break: break-all; user-select: all; }
button { flex: 0 0 auto; font: inherit; font-size: .8rem; padding: .45rem .7rem; border-radius: .4rem;
  border: 1px solid var(--line); background: var(--card); color: var(--accent); cursor: pointer; }
button:hover { border-color: var(--accent); }
.note { color: var(--dim); font-size: .85rem; margin: .6rem 0 0; }
.warn { color: var(--warn); }
footer { margin-top: 2.5rem; color: var(--dim); font-size: .8rem; text-align: center; }
</style>
</head>
<body>
<main>
<h1>Client options for {{.Domain}}</h1>
<p class="meta">Everything this node can hand to a client, in one place. Rendered {{when .Generated}}.</p>

<h2>This node</h2>
<section>
  <ul class="links">
    {{template "link" .Local}}
  </ul>
  <p class="note">This device works even when the management server cannot be reached.</p>
</section>

<h2>Mesh ({{count .Peers}})</h2>
<section>
  {{if .Warning}}<p class="note warn">{{.Warning}}</p>{{end}}
  {{if .Peers}}
  <ul class="links">
    {{range .Peers}}{{template "link" .}}{{end}}
  </ul>
  {{else if not .Warning}}
  <p class="note">No peers: this node is not enrolled with a management server.</p>
  {{end}}
</section>

{{if .SubscriptionURL}}
<h2>Subscription</h2>
<section>
  <div class="label"><span class="name">Plain list</span></div>
  <div class="uri"><code>{{.SubscriptionURL}}</code><button type="button" data-copy>copy</button></div>
  <p class="note">One VLESS URI per line, base64 — the field a client's “subscription URL” wants.</p>
  {{if .JSONURL}}
  <div class="label" style="margin-top:1rem"><span class="name">sing-box profile</span></div>
  <div class="uri"><code>{{.JSONURL}}</code><button type="button" data-copy>copy</button></div>
  <p class="note">For clients that read sing-box JSON directly.</p>
  {{end}}
</section>
{{end}}

<h2>This page</h2>
<section>
  <div class="uri"><code>{{.PageURL}}</code><button type="button" data-copy>copy</button></div>
  <p class="note">The address is the only secret: anyone who has it sees the same links. Rotate it with
  <code>termit-uv options --rotate</code> if it leaks.</p>
</section>

<footer>
  This page is secret: anyone with its address sees the same links. Keep it out of screenshots that
  leave your hands.
</footer>
</main>
<script>
document.addEventListener('click', function (event) {
  var button = event.target.closest('[data-copy]');
  if (!button) return;
  var code = button.parentElement.querySelector('code');
  if (!code) return;
  var text = code.textContent;
  var done = function (ok) {
    button.textContent = ok ? 'copied' : 'copy failed';
    setTimeout(function () { button.textContent = 'copy'; }, 1500);
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(false); });
    return;
  }
  var area = document.createElement('textarea');
  area.value = text;
  document.body.appendChild(area);
  area.select();
  try { done(document.execCommand('copy')); } catch (e) { done(false); }
  document.body.removeChild(area);
});
</script>
</body>
</html>
{{define "link"}}
<li class="link">
  <div class="label">
    <span class="name">{{.Label}}</span>
    {{if .Degraded}}<span class="tag">degraded</span>{{end}}
  </div>
  <div class="uri"><code>{{.URI}}</code><button type="button" data-copy>copy</button></div>
</li>
{{end}}
`))

// Render produces the page. The result depends only on d, which keeps the command honest about what
// it is about to write.
func Render(d Data) ([]byte, error) {
	var buf strings.Builder
	if err := pageTemplate.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("options: render: %w", err)
	}
	return []byte(buf.String()), nil
}
