package decoy

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"math/rand/v2"
	"strings"
	texttemplate "text/template"
)

//go:embed templates
var templateFS embed.FS

var (
	// pageTemplates holds the HTML documents, parsed once.
	pageTemplates = htmltemplate.Must(htmltemplate.ParseFS(templateFS, "templates/*.html.tmpl"))
	// styleTemplates holds the stylesheets; they are plain text, escaped nowhere.
	styleTemplates = texttemplate.Must(texttemplate.ParseFS(templateFS, "templates/*.css.tmpl"))
	// bootJS is the small script both archetypes load.
	bootJS = mustRead("templates/boot.js")
)

func mustRead(name string) []byte {
	data, err := fs.ReadFile(templateFS, name)
	if err != nil {
		panic(err)
	}
	return data
}

// link is one footer or secondary link.
type link struct {
	Label string
	Href  string
}

// palette is the colour set of one generation.
type palette struct {
	Background string
	Panel      string
	Ink        string
	Muted      string
	Accent     string
	AccentInk  string
	Line       string
}

// pageData is what the templates see.
type pageData struct {
	Domain     string
	Brand      string
	Badge      string
	Title      string
	Subtitle   string
	Year       int
	FormID     string
	UserID     string
	PassID     string
	Remember   string
	RememberID string
	UserLabel  string
	PassLabel  string
	CTA        string
	Forgot     string
	Company    string
	Links      []link
	Legal      []link
	Action     string
	Theme      string
	Palette    palette
	CardWidth  int
	Radius     int
	Layout     string
	FooterMode string
	ShowSSO    bool
	SSOLabel   string
	Mark       htmltemplate.HTML
	CSS        htmltemplate.CSS
}

// rendered is the complete output of one generation.
type rendered struct {
	index    []byte
	notFound []byte
	robots   []byte
	favicon  []byte
	auth     AuthRoute
}

var (
	portalBrands   = []string{"Nimbus", "Vantage", "Halcyon", "Meridian", "Northwind", "Obsidian", "Keystone", "Lattice", "Cobalt", "Quartz", "Beacon", "Foundry", "Ardent", "Vireo"}
	portalSuffixes = []string{"Ops", "Cloud", "Systems", "Analytics", "Platform", "Grid", "Networks", "Works"}
	portalKinds    = []string{"Console", "Access", "Operations", "Control Plane", "Operator Access"}
	portalBadges   = []string{"OPS HUB", "CONTROL PLANE", "ACCESS GATEWAY", "SECURE OPERATIONS", "INFRASTRUCTURE ACCESS", "DATA PLANE", "IDENTITY FABRIC", "OPERATOR PORTAL"}

	portalSubtitles = []string{
		"Metrics, trace ingestion and alert routing for the %s platform.",
		"Unified access to the %s control plane and its regional relays.",
		"Session gateway for the %s infrastructure estate.",
		"Operator access to %s service health, quotas and audit trails.",
		"Consolidated telemetry, routing policy and capacity for %s.",
	}

	credentialPairs = [][2]string{
		{"Identity Principal", "Credential Key"},
		{"Operator ID", "Access Token"},
		{"Account Alias", "Secret Phrase"},
		{"Principal", "Passphrase"},
		{"Subject", "Verification Key"},
		{"Identity", "Session Secret"},
	}

	userIDs    = []string{"principal", "operator", "account", "identity", "subject", "member"}
	passIDs    = []string{"secret", "token", "passphrase", "credential", "key"}
	rememberID = []string{"remember", "keep-session", "trust-device", "stay-signed-in"}
	formIDs    = []string{"login-form", "signin", "session-form", "auth-form", "access-form"}
	ctaLabels  = []string{"Establish Session", "Verify Credentials", "Open Console", "Continue to Console", "Authenticate"}
	remember   = []string{"Keep session active", "Remember this device", "Trust this browser", "Stay signed in"}
	forgot     = []string{"Forgot credentials?", "Need help signing in?", "Recover access", ""}
	ssoLabels  = []string{"Continue with SSO", "Sign in with SAML", "Use organisation sign-in"}

	companies = []string{
		"SecureData Telecommunications Group",
		"Nexus Identity Management Alliance",
		"Halcyon Infrastructure Holdings",
		"Vector Network Systems",
		"Meridian Datacentre Services",
		"Ardent Network Holdings",
		"Northwind Carrier Assets",
	}

	legalLinks = []link{
		{"Trust & Safety", "/trust"},
		{"Legal Disclosures", "/legal"},
		{"System Operating Rules", "/rules"},
		{"Acceptable Use", "/acceptable-use"},
		{"Data Processing", "/dpa"},
		{"Status", "/status"},
		{"Support", "/support"},
	}

	productBrands = []string{"Attest", "Ledgerline", "Northgate", "Fieldwork", "Clearpath", "Trellis", "Mountaintop", "Rivet", "Longview", "Kestrel"}
	productKinds  = []string{"Workspace", "Console", "Cloud", "Studio", "Platform"}
	productHints  = []string{
		"Use your %s account to continue.",
		"Continue to your %s workspace.",
		"Sign in to manage your %s projects.",
	}
	productSecondary = []link{
		{"Forgot password?", "/account/recovery"},
		{"Create account", "/signup"},
		{"Privacy", "/privacy"},
		{"Terms", "/terms"},
		{"Help", "/help"},
	}

	authPaths = []string{
		"/session/verify",
		"/auth/token",
		"/api/session",
		"/login/authenticate",
		"/access/verify",
		"/portal/session",
		"/v1/session/open",
		"/identity/verify",
	}
	cookieNames = []string{"sid", "session", "portal_sid", "auth_session", "sess"}

	authCodes = []string{"invalid_credentials", "authentication_failed", "auth_required", "session_denied"}
	authTexts = []string{
		"The provided credentials could not be verified.",
		"Authentication failed. Check your credentials and try again.",
		"This account is not permitted to open a session right now.",
		"The credential pair is not recognised for this deployment.",
	}

	adminPaths = []string{"admin", "internal", "metrics", "api", "private", "staging", "_debug", "ops"}
)

// newRNG derives the generator from the spec, so two installs with the same seed produce the same
// site and two installs with different seeds share nothing.
func newRNG(spec Spec) *rand.Rand {
	sum := sha256.Sum256([]byte(spec.Domain + "|" + spec.Seed + "|" + spec.Template))
	var key [32]byte
	copy(key[:], sum[:])
	return rand.New(rand.NewChaCha8(key))
}

// render builds every file of the site.
func render(spec Spec, rng *rand.Rand) (*rendered, error) {
	// The endpoint is chosen before the page, so the form posts to exactly the path the nginx
	// fragment answers. Two independent picks would eventually disagree.
	auth := authRoute(rng)
	var data pageData
	switch spec.Template {
	case TemplatePortal:
		data = portalData(spec, rng, auth)
	case TemplateProduct:
		data = productData(spec, rng, auth)
	default:
		return nil, fmt.Errorf("decoy: unknown template %q", spec.Template)
	}

	var style bytes.Buffer
	if err := styleTemplates.ExecuteTemplate(&style, spec.Template+".css.tmpl", data); err != nil {
		return nil, fmt.Errorf("decoy: render stylesheet: %w", err)
	}
	data.CSS = htmltemplate.CSS(style.String())

	index, err := executePage(spec.Template+".html.tmpl", data)
	if err != nil {
		return nil, err
	}
	notFound, err := executePage("notfound.html.tmpl", data)
	if err != nil {
		return nil, err
	}

	favicon, err := faviconPNG(rng, data.Palette)
	if err != nil {
		return nil, err
	}
	return &rendered{
		index:    index,
		notFound: notFound,
		robots:   robotsTxt(spec, data, rng),
		favicon:  favicon,
		auth:     auth,
	}, nil
}

func executePage(name string, data pageData) ([]byte, error) {
	var out bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&out, name, data); err != nil {
		return nil, fmt.Errorf("decoy: render %s: %w", name, err)
	}
	return out.Bytes(), nil
}

// portalData fills the branded-portal archetype.
func portalData(spec Spec, rng *rand.Rand, auth AuthRoute) pageData {
	brand := pick(rng, portalBrands) + " " + pick(rng, portalSuffixes)
	kind := pick(rng, portalKinds)
	title := brand + " " + kind
	pair := credentialPairs[rng.IntN(len(credentialPairs))]
	theme, colours := pickPalette(rng, false)

	data := pageData{
		Domain:     spec.Domain,
		Brand:      brand,
		Badge:      pick(rng, portalBadges),
		Title:      title,
		Subtitle:   fmt.Sprintf(pick(rng, portalSubtitles), brand),
		Year:       spec.Now.Year(),
		FormID:     pick(rng, formIDs),
		UserID:     pick(rng, userIDs),
		PassID:     pick(rng, passIDs),
		RememberID: pick(rng, rememberID),
		UserLabel:  pair[0],
		PassLabel:  pair[1],
		CTA:        pick(rng, ctaLabels),
		Remember:   pick(rng, remember),
		Forgot:     pick(rng, forgot),
		Company:    pick(rng, companies),
		Links:      pickLinks(rng, 2, 3),
		Theme:      theme,
		Palette:    colours,
		CardWidth:  380 + 20*rng.IntN(5),
		Radius:     6 + rng.IntN(8),
		Layout:     pick(rng, []string{"stacked", "stacked", "split"}),
		FooterMode: pick(rng, []string{"inline", "stacked"}),
		ShowSSO:    rng.IntN(5) < 2,
		SSOLabel:   pick(rng, ssoLabels),
		Mark:       mark(rng, colours.Accent),
	}
	data.Action = auth.Path
	return data
}

// productData fills the single-product sign-in archetype.
func productData(spec Spec, rng *rand.Rand, auth AuthRoute) pageData {
	brand := pick(rng, productBrands)
	_, colours := pickPalette(rng, true)

	data := pageData{
		Domain:     spec.Domain,
		Brand:      brand,
		Badge:      "",
		Title:      "Sign in to " + brand,
		Subtitle:   fmt.Sprintf(pick(rng, productHints), brand),
		Year:       spec.Now.Year(),
		FormID:     pick(rng, formIDs),
		UserID:     "email",
		PassID:     "password",
		RememberID: pick(rng, rememberID),
		UserLabel:  "Email",
		PassLabel:  "Password",
		CTA:        "Sign in",
		Remember:   "",
		Forgot:     "Forgot password?",
		Company:    brand + " " + pick(rng, productKinds),
		Links:      pickSubset(rng, productSecondary[1:2], 1),
		Legal:      pickSubset(rng, productSecondary[2:], 3),
		Theme:      "light",
		Palette:    colours,
		CardWidth:  400 + 20*rng.IntN(5),
		Radius:     8 + rng.IntN(6),
		Layout:     "stacked",
		FooterMode: "inline",
		ShowSSO:    true,
		SSOLabel:   pick(rng, ssoLabels),
		Mark:       mark(rng, colours.Accent),
	}
	data.Action = auth.Path
	return data
}

// pickPalette returns a dark or light preset. The product archetype is always light.
func pickPalette(rng *rand.Rand, lightOnly bool) (string, palette) {
	dark := []palette{
		{"#0b0f0d", "#111815", "#e8efe9", "#93a39a", "#2f7d5c", "#04140d", "#1e2a24"},
		{"#0c0d12", "#14161f", "#e9eaf2", "#98a0b8", "#3d5afe", "#ffffff", "#232637"},
		{"#0f0b14", "#171021", "#efe9f7", "#a89bb8", "#7c4dff", "#ffffff", "#2a2036"},
		{"#0a0d12", "#101722", "#e6edf6", "#94a3b8", "#0ea5e9", "#04121b", "#1d2939"},
	}
	light := []palette{
		{"#f5f6f8", "#ffffff", "#111827", "#6b7280", "#f97316", "#ffffff", "#e5e7eb"},
		{"#f7f7f9", "#ffffff", "#14161a", "#64687a", "#2563eb", "#ffffff", "#e4e6ec"},
		{"#f6f8f7", "#ffffff", "#101917", "#5f6f6a", "#0f766e", "#ffffff", "#dfe7e4"},
		{"#f8f6f4", "#ffffff", "#1a1614", "#6f645c", "#9a3412", "#ffffff", "#e9e1da"},
	}
	if lightOnly {
		return "light", light[rng.IntN(len(light))]
	}
	if rng.IntN(10) < 7 {
		return "dark", dark[rng.IntN(len(dark))]
	}
	return "light", light[rng.IntN(len(light))]
}

func authRoute(rng *rand.Rand) AuthRoute {
	return AuthRoute{
		Path:        pick(rng, authPaths),
		Method:      "POST",
		Status:      401,
		ContentType: "application/json",
		Body: fmt.Sprintf(`{"error":%q,"message":%q}`,
			pick(rng, authCodes), pick(rng, authTexts)),
		Cookie: fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Lax",
			pick(rng, cookieNames), randHex(rng, 16)),
	}
}

// robotsTxt hides a few internal-looking paths, as an internal portal would.
func robotsTxt(spec Spec, data pageData, rng *rand.Rand) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", spec.Domain)
	b.WriteString("User-agent: *\n")
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		name := pick(rng, adminPaths)
		if seen[name] {
			continue
		}
		seen[name] = true
		fmt.Fprintf(&b, "Disallow: /%s\n", name)
	}
	fmt.Fprintf(&b, "\n# %s\n", data.Company)
	return []byte(b.String())
}

// mark returns an inline SVG brand mark built from a few primitives, so the site needs no image
// file and no two deployments share the same mark.
func mark(rng *rand.Rand, accent string) htmltemplate.HTML {
	// No xmlns: the mark is inlined into an HTML document, where SVG inherits the HTML namespace,
	// and a namespace URL would be the only URL in the whole site. The checks insist on that.
	var body string
	switch rng.IntN(4) {
	case 0:
		body = fmt.Sprintf(`<rect x="2" y="2" width="24" height="24" rx="7" fill="%s" opacity="0.16"/><rect x="7" y="12" width="14" height="3" rx="1.5" fill="%s"/><rect x="7" y="17" width="9" height="3" rx="1.5" fill="%s"/>`, accent, accent, accent)
	case 1:
		body = fmt.Sprintf(`<circle cx="14" cy="14" r="10" stroke="%s" stroke-width="2.5"/><circle cx="14" cy="14" r="3" fill="%s"/>`, accent, accent)
	case 2:
		body = fmt.Sprintf(`<rect x="3" y="8" width="14" height="14" rx="3" stroke="%s" stroke-width="2.5"/><rect x="11" y="3" width="14" height="14" rx="3" fill="%s" opacity="0.55"/>`, accent, accent)
	default:
		body = fmt.Sprintf(`<path d="M4 20 L14 6 L24 20" stroke="%s" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/><circle cx="14" cy="20" r="2.4" fill="%s"/>`, accent, accent)
	}
	return htmltemplate.HTML(`<svg viewBox="0 0 28 28" width="28" height="28" fill="none" aria-hidden="true">` + body + `</svg>`) // #nosec G203 -- the markup is ours and contains no user input.
}

func pick(rng *rand.Rand, values []string) string { return values[rng.IntN(len(values))] }

// pickLinks chooses n legal links, without repeats.
func pickLinks(rng *rand.Rand, min, max int) []link {
	n := min + rng.IntN(max-min+1)
	return pickSubset(rng, legalLinks, n)
}

// pickSubset returns n distinct entries of values, in the order the permutation produced.
func pickSubset(rng *rand.Rand, values []link, n int) []link {
	if n > len(values) {
		n = len(values)
	}
	perm := rng.Perm(len(values))
	out := make([]link, 0, n)
	for _, index := range perm[:n] {
		out = append(out, values[index])
	}
	return out
}

// randHex returns n random bytes as hex.
func randHex(rng *rand.Rand, n int) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, n*2)
	for i := 0; i < n*2; i++ {
		out = append(out, digits[rng.IntN(len(digits))])
	}
	return string(out)
}
