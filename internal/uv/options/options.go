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
	"net/url"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/i18n"
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
	// Lang is the language the page is written in. It follows the owner's choice and does not depend
	// on what the console can show: a page is read in a browser, which always has the glyphs.
	Lang i18n.Lang
	// PageURL is the address the owner should keep; it is printed on the page too, so a screenshot
	// still tells them how to open it again.
	PageURL string
	// Local is this node's own device, one link per transport it serves. It is what survives a
	// management server that cannot be reached, so it is always present. A second transport is another
	// way into the same node — not a local device, which is why it belongs here and not in Devices.
	Local []Link
	// Devices are the node's local users: the owner's phone, laptop, router. They live only on this
	// node, and the page is where the owner picks the one they need without a terminal.
	Devices []Link
	// Peers is the mesh as the subscription last described it, without this node.
	Peers []Link
	// Warning explains an empty or stale Peers list in one sentence. Render shows it above the mesh
	// section instead of silently pretending the mesh is empty.
	Warning string
	// SubscriptionURL and JSONURL are the two forms of the same peer list. They carry the node's
	// token, which is why they belong on this page and nowhere else.
	SubscriptionURL string
	JSONURL         string
	Generated       time.Time

	// Text is filled by Render from Lang, so callers never have to keep translations in step with the
	// template. It is exported because html/template reads only exported fields.
	Text pageText
	// LocalView, DevicesView and PeersView pair each link with the words its row needs. html/template
	// rebinds $ when a named template is called, so the words have to travel with the link.
	LocalView   []LinkView
	DevicesView []LinkView
	PeersView   []LinkView
}

// LinkView is one link plus the words around it.
type LinkView struct {
	Link
	Text pageText
	// QR is the link drawn as a scannable code, empty when it does not fit. It is markup this package
	// built, which is why it is a value the template prints rather than escapes.
	QR template.HTML
}

// pageText is the resolved strings for one language. It exists so the template holds no prose and the
// two languages cannot drift apart silently.
type pageText struct {
	Title, Subtitle, Local, LocalNote, Devices, DevicesNote, Subscription string
	Mesh, NoPeers, Warning, PlainName, PlainNote                          string
	JSONName, JSONNote, ThisPage, ThisPageNote                            string
	Copy, QR, Copied, CopyFailed, Footer, Degraded                        string
}

func textFor(lang i18n.Lang) pageText {
	tr := i18n.New(lang)
	return pageText{
		Title:        tr.T(i18n.PageTitle),
		Subtitle:     tr.T(i18n.PageSubtitle),
		Local:        tr.T(i18n.PageLocal),
		LocalNote:    tr.T(i18n.PageLocalNote),
		Devices:      tr.T(i18n.PageDevices),
		DevicesNote:  tr.T(i18n.PageDevicesNote),
		Mesh:         tr.T(i18n.PageMesh),
		NoPeers:      tr.T(i18n.PageNoPeers),
		Warning:      tr.T(i18n.PageWarning),
		Subscription: tr.T(i18n.PageSubscription),
		PlainName:    tr.T(i18n.PagePlainName),
		PlainNote:    tr.T(i18n.PagePlainNote),
		JSONName:     tr.T(i18n.PageJSONName),
		JSONNote:     tr.T(i18n.PageJSONNote),
		ThisPage:     tr.T(i18n.PageThisPage),
		ThisPageNote: tr.T(i18n.PageThisPageNote),
		Copy:         tr.T(i18n.PageCopy),
		QR:           tr.T(i18n.PageQR),
		Copied:       tr.T(i18n.PageCopied),
		CopyFailed:   tr.T(i18n.PageCopyFailed),
		Footer:       tr.T(i18n.PageFooter),
		Degraded:     tr.T(i18n.PageDegraded),
	}
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

// WithoutLocal drops the entry that describes this node itself. A subscription is rendered for every
// peer, and the node is one of them, so without this the page would list it twice — once as "this
// node", once as a peer — and the owner would have to work out which link is which.
//
// The domain is what identifies it, not the label: a label is chosen by the operator and can be
// anything, while the host in the URI is what the client dials. The label is the fallback for a URI
// that will not parse.
func WithoutLocal(peers []Link, domain string) []Link {
	if domain == "" {
		return peers
	}
	want := strings.ToLower(strings.TrimSpace(domain))
	out := make([]Link, 0, len(peers))
	for _, peer := range peers {
		if strings.ToLower(hostOf(peer.URI)) == want || strings.ToLower(peer.Label) == want {
			continue
		}
		out = append(out, peer)
	}
	return out
}

// hostOf returns the host a vless URI dials, or "" when it cannot tell.
func hostOf(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// pageTemplate is self-contained on purpose, with the same rule as the decoy site: no external host,
// no font, no analytics. A page that phones home is a fingerprint of the node that serves it.
var pageTemplate = template.Must(template.New("options").Funcs(template.FuncMap{
	"when": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format("2006-01-02 15:04 UTC")
	},
}).Parse(`<!DOCTYPE html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<title>{{printf .Text.Title .Domain}}</title>
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
.qr { margin-top: .55rem; }
.qr summary { cursor: pointer; font-size: .8rem; color: var(--dim); }
.qr svg { display: block; width: 100%; max-width: 20rem; height: auto; margin-top: .5rem;
  background: #fff; border-radius: .3rem; }
.note { color: var(--dim); font-size: .85rem; margin: .6rem 0 0; }
.warn { color: var(--warn); }
footer { margin-top: 2.5rem; color: var(--dim); font-size: .8rem; text-align: center; }
</style>
</head>
<body>
<main>
<h1>{{printf .Text.Title .Domain}}</h1>
<p class="meta">{{printf .Text.Subtitle (when .Generated)}}</p>

<h2>{{.Text.Local}}</h2>
<section>
  <ul class="links">
    {{range .LocalView}}{{template "link" .}}{{end}}
  </ul>
  <p class="note">{{.Text.LocalNote}}</p>
</section>

{{if .Devices}}
<h2>{{printf .Text.Devices (len .Devices)}}</h2>
<section>
  <ul class="links">
    {{range .DevicesView}}{{template "link" .}}{{end}}
  </ul>
  <p class="note">{{.Text.DevicesNote}}</p>
</section>
{{end}}

<h2>{{printf .Text.Mesh (len .Peers)}}</h2>
<section>
  {{if .Warning}}<p class="note warn">{{.Text.Warning}}</p>{{end}}
  {{if .Peers}}
  <ul class="links">
    {{range .PeersView}}{{template "link" .}}{{end}}
  </ul>
  {{else if not .Warning}}
  <p class="note">{{.Text.NoPeers}}</p>
  {{end}}
</section>

{{if .SubscriptionURL}}
<h2>{{.Text.Subscription}}</h2>
<section>
  <div class="label"><span class="name">{{.Text.PlainName}}</span></div>
  <div class="uri"><code>{{.SubscriptionURL}}</code><button type="button" data-copy>{{.Text.Copy}}</button></div>
  <p class="note">{{.Text.PlainNote}}</p>
  {{if .JSONURL}}
  <div class="label" style="margin-top:1rem"><span class="name">{{.Text.JSONName}}</span></div>
  <div class="uri"><code>{{.JSONURL}}</code><button type="button" data-copy>{{.Text.Copy}}</button></div>
  <p class="note">{{.Text.JSONNote}}</p>
  {{end}}
</section>
{{end}}

<h2>{{.Text.ThisPage}}</h2>
<section>
  <div class="uri"><code>{{.PageURL}}</code><button type="button" data-copy>{{.Text.Copy}}</button></div>
  <p class="note">{{.Text.ThisPageNote}}</p>
</section>

<footer>{{.Text.Footer}}</footer>
</main>
<script>
var words = { copy: {{.Text.Copy}}, copied: {{.Text.Copied}}, failed: {{.Text.CopyFailed}} };
document.addEventListener('click', function (event) {
  var button = event.target.closest('[data-copy]');
  if (!button) return;
  var code = button.parentElement.querySelector('code');
  if (!code) return;
  var settle = function (ok) {
    button.textContent = ok ? words.copied : words.failed;
    setTimeout(function () { button.textContent = words.copy; }, 1500);
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(code.textContent).then(function () { settle(true); }, function () { settle(false); });
    return;
  }
  var area = document.createElement('textarea');
  area.value = code.textContent;
  document.body.appendChild(area);
  area.select();
  try { settle(document.execCommand('copy')); } catch (e) { settle(false); }
  document.body.removeChild(area);
});
</script>
</body>
</html>
{{define "link"}}
<li class="link">
  <div class="label">
    <span class="name">{{.Label}}</span>
    {{if .Degraded}}<span class="tag">{{.Text.Degraded}}</span>{{end}}
  </div>
  <div class="uri"><code>{{.URI}}</code><button type="button" data-copy>{{.Text.Copy}}</button></div>
  {{if .QR}}<details class="qr"><summary>{{.Text.QR}}</summary>{{.QR}}</details>{{end}}
</li>
{{end}}
`))

// Render produces the page in d.Lang. The result depends only on d, which keeps the command honest
// about what it is about to write.
func Render(d Data) ([]byte, error) {
	if !d.Lang.Valid() {
		d.Lang = i18n.Default
	}
	d.Text = textFor(d.Lang)
	d.LocalView = make([]LinkView, 0, len(d.Local))
	for _, link := range d.Local {
		d.LocalView = append(d.LocalView, LinkView{Link: link, Text: d.Text, QR: qrSVG(link.URI)})
	}
	d.DevicesView = make([]LinkView, 0, len(d.Devices))
	for _, device := range d.Devices {
		d.DevicesView = append(d.DevicesView, LinkView{Link: device, Text: d.Text, QR: qrSVG(device.URI)})
	}
	d.PeersView = make([]LinkView, 0, len(d.Peers))
	for _, peer := range d.Peers {
		d.PeersView = append(d.PeersView, LinkView{Link: peer, Text: d.Text, QR: qrSVG(peer.URI)})
	}
	var buf strings.Builder
	if err := pageTemplate.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("options: render: %w", err)
	}
	return []byte(buf.String()), nil
}
