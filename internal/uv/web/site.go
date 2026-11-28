package web

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

const (
	// DefaultHTTPPort is where ACME validation and the redirect to TLS live.
	DefaultHTTPPort = 80
	// DefaultHTTPSPort is the public port a client connects to.
	DefaultHTTPSPort = 443
)

var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// SiteSpec is everything the vhost generator may look at. It is a value: the same spec always
// produces the same file.
type SiteSpec struct {
	Domain string
	// SiteDir is the decoy tree's root.
	SiteDir string
	// CertPath and KeyPath switch the vhost to TLS. Both empty means the node has no certificate
	// yet: it serves the decoy over plain HTTP and ACME has a place to land.
	CertPath string
	KeyPath  string
	// SecretPath is the XHTTP path nginx forwards to Xray. Empty means no proxying yet.
	SecretPath string
	// XrayAddr is the loopback address of the Xray inbound, e.g. 127.0.0.1:8443.
	XrayAddr string
	// WebSocketPath and WebSocketAddr are the second rendering of the same profile: its own path,
	// forwarded to its own loopback inbound, with the upgrade headers a WebSocket client needs
	// (docs/IMPLEMENTATION_PLAN.md §5.2.6). Empty means the node serves XHTTP only.
	WebSocketPath string
	WebSocketAddr string
	// ACMEWebroot is the directory served at /.well-known/acme-challenge/.
	ACMEWebroot string
	// Fragment is the path of the generated decoy snippet.
	Fragment string
	// AccessLog and ErrorLog are absolute paths.
	AccessLog string
	ErrorLog  string

	HTTPPort  int
	HTTPSPort int
	// QUIC says the node also serves a QUIC transport on HTTPSPort over UDP. The vhost then advertises
	// it with Alt-Svc, which makes the port part of this site instead of a port someone finds by
	// scanning.
	QUIC bool

	// HTTP2Directive says the host's nginx understands the standalone `http2` directive, which
	// arrived in 1.25.1. Older nginx treats `http2` as a parameter of `listen` and refuses the
	// directive outright — and a vhost nginx refuses takes the whole web tier down with it, ACME
	// included. The caller asks the binary that will parse the file; the generator does not guess.
	HTTP2Directive bool
}

// TLS reports whether the spec produces a TLS vhost.
func (s SiteSpec) TLS() bool { return s.CertPath != "" && s.KeyPath != "" }

// SupportsHTTP2Directive reports whether the nginx that printed version is 1.25.1 or newer, the
// release that split HTTP/2 out of the `listen` parameter into a directive of its own.
//
// A version that cannot be parsed answers false: the listen parameter is understood by every nginx
// this project targets — 1.22 on Debian 12, 1.26 on Debian 13 — while the directive is a hard error
// everywhere older. Guessing wrong in that direction costs a warning on stderr; guessing the other
// way costs the web tier.
func SupportsHTTP2Directive(version string) bool {
	major, minor, patch, ok := parseNginxVersion(version)
	if !ok {
		return false
	}
	switch {
	case major != 1:
		return major > 1
	case minor != 25:
		return minor > 25
	default:
		return patch >= 1
	}
}

// parseNginxVersion reads "nginx/1.22.1" out of what `nginx -v` prints ("nginx version: nginx/1.26.3").
func parseNginxVersion(version string) (major, minor, patch int, ok bool) {
	if i := strings.LastIndexByte(version, '/'); i >= 0 {
		version = version[i+1:]
	}
	end := 0
	for end < len(version) && (version[end] >= '0' && version[end] <= '9' || version[end] == '.') {
		end++
	}
	parts := strings.Split(strings.Trim(version[:end], "."), ".")
	if len(parts) < 2 {
		return 0, 0, 0, false
	}
	numbers := [3]int{}
	for i, part := range parts {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, false
		}
		numbers[i] = n
	}
	return numbers[0], numbers[1], numbers[2], true
}

func (s SiteSpec) validate() error {
	var problems []error
	if !domainPattern.MatchString(strings.ToLower(s.Domain)) {
		problems = append(problems, fmt.Errorf("domain %q is not a host name", s.Domain))
	}
	for _, named := range []struct{ name, path string }{
		{"site directory", s.SiteDir},
		{"acme webroot", s.ACMEWebroot},
		{"decoy fragment", s.Fragment},
		{"access log", s.AccessLog},
		{"error log", s.ErrorLog},
		{"certificate", s.CertPath},
		{"certificate key", s.KeyPath},
	} {
		if named.path == "" {
			continue
		}
		if err := checkAbsolutePath(named.name, named.path); err != nil {
			problems = append(problems, err)
		}
	}
	if (s.CertPath == "") != (s.KeyPath == "") {
		problems = append(problems, errors.New("certificate and key must be given together"))
	}
	if s.SecretPath != "" {
		if !strings.HasPrefix(s.SecretPath, "/") || !strings.HasSuffix(s.SecretPath, "/") {
			problems = append(problems, fmt.Errorf("secret path %q must start and end with /", s.SecretPath))
		}
		if strings.ContainsAny(s.SecretPath, " \t\r\n;{}\"'") {
			problems = append(problems, fmt.Errorf("secret path %q contains a character nginx would misread", s.SecretPath))
		}
		if s.XrayAddr == "" {
			problems = append(problems, errors.New("a secret path without an xray address would forward nowhere"))
		}
	}
	if s.WebSocketPath != "" {
		if !strings.HasPrefix(s.WebSocketPath, "/") || !strings.HasSuffix(s.WebSocketPath, "/") {
			problems = append(problems, fmt.Errorf("websocket path %q must start and end with /", s.WebSocketPath))
		}
		if strings.ContainsAny(s.WebSocketPath, " \t\r\n;{}\"'") {
			problems = append(problems, fmt.Errorf("websocket path %q contains a character nginx would misread", s.WebSocketPath))
		}
		if s.WebSocketPath == s.SecretPath {
			problems = append(problems, errors.New("the websocket path must differ from the xhttp one"))
		}
		if s.WebSocketAddr == "" {
			problems = append(problems, errors.New("a websocket path without an address would forward nowhere"))
		}
	}
	if s.WebSocketAddr != "" {
		if !strings.HasPrefix(s.WebSocketAddr, "127.0.0.1:") && !strings.HasPrefix(s.WebSocketAddr, "[::1]:") {
			problems = append(problems, fmt.Errorf("websocket address %q is not loopback", s.WebSocketAddr))
		}
	}
	if s.XrayAddr != "" {
		if !strings.HasPrefix(s.XrayAddr, "127.0.0.1:") && !strings.HasPrefix(s.XrayAddr, "[::1]:") {
			problems = append(problems, fmt.Errorf("xray address %q is not loopback", s.XrayAddr))
		}
	}
	for _, port := range []struct {
		name string
		port int
	}{{"http port", s.HTTPPort}, {"https port", s.HTTPSPort}} {
		if port.port < 1 || port.port > 65535 {
			problems = append(problems, fmt.Errorf("%s %d is out of range", port.name, port.port))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid site spec: %w", errors.Join(problems...))
	}
	return nil
}

// checkAbsolutePath rejects relative paths and the characters that would end an nginx directive.
func checkAbsolutePath(name, path string) error {
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%s %q is not an absolute path", name, path)
	}
	if strings.ContainsAny(path, "\r\n\t;{}\"' ") {
		return fmt.Errorf("%s %q contains a character nginx would misread", name, path)
	}
	return nil
}

// siteHeader is the same for both shapes: a reader must know the file is generated.
const siteHeader = `# Generated by termit-uv. Do not edit: ` + "`termit-uv init`" + ` and ` + "`termit-uv renew-site`" + ` rewrite this file.

`

// httpOnlyTemplate is the vhost of a node that has no certificate yet. It has to serve the decoy
// (a node that answers nothing is a node with nothing to mask) and the ACME challenge.
var httpOnlyTemplate = template.Must(template.New("http").Parse(siteHeader + `server {
    listen       {{.HTTPPort}};
    listen       [::]:{{.HTTPPort}};
    server_name  {{.Domain}};
    server_tokens off;
    charset      utf-8;

    root  {{.SiteDir}};
    index index.html;

    location ^~ /.well-known/acme-challenge/ {
        root         {{.ACMEWebroot}};
        default_type text/plain;
        try_files    $uri =404;
    }

    location ^~ /assets/ {
        expires 24h;
        try_files $uri =404;
    }

    include {{.Fragment}};

    access_log {{.AccessLog}};
    error_log  {{.ErrorLog}};
}
`))

// tlsTemplate is the steady state: :80 keeps the ACME challenge and redirects, :443 is the site.
var tlsTemplate = template.Must(template.New("tls").Parse(siteHeader + `server {
    listen       {{.HTTPPort}};
    listen       [::]:{{.HTTPPort}};
    server_name  {{.Domain}};
    server_tokens off;
    charset      utf-8;

    location ^~ /.well-known/acme-challenge/ {
        root         {{.ACMEWebroot}};
        default_type text/plain;
        try_files    $uri =404;
    }

    location / {
        return 301 https://$host$request_uri;
    }

    access_log {{.AccessLog}};
    error_log  {{.ErrorLog}};
}

server {
{{if .HTTP2Directive}}    listen               {{.HTTPSPort}} ssl;
    listen               [::]:{{.HTTPSPort}} ssl;
    # nginx 1.25+ needs this line; without it a client that offers h2 gets its preface refused.
    http2                on;
{{else}}    # Before 1.25 there is no http2 directive, and h2 is a parameter of listen instead.
    listen               {{.HTTPSPort}} ssl http2;
    listen               [::]:{{.HTTPSPort}} ssl http2;
{{end}}    server_name          {{.Domain}};
    server_tokens        off;
    charset              utf-8;

    ssl_certificate      {{.CertPath}};
    ssl_certificate_key  {{.KeyPath}};
    ssl_protocols        TLSv1.2 TLSv1.3;
    ssl_session_cache    shared:termit:10m;
    ssl_session_timeout  1d;
    ssl_session_tickets  off;
    ssl_prefer_server_ciphers off;

    add_header Strict-Transport-Security "max-age=31536000" always;
{{- if .QUIC}}
    # The QUIC transport answers on the same port number over UDP. Alt-Svc is what makes it part of
    # this site: a browser learns about it from a response it already trusts.
    add_header Alt-Svc 'h3=":{{.HTTPSPort}}"; ma=86400' always;
{{- end}}

    # A response that changes with Accept-Encoding must say so. Without this line nginx compresses
    # and stays silent about it, which is a configuration tell no real site leaves behind.
    gzip_vary on;

    root  {{.SiteDir}};
    index index.html;

    location ^~ /assets/ {
        expires 24h;
        try_files $uri =404;
    }
{{if .SecretPath}}
    # The only path that reaches Xray. Everything else is the decoy tree or a 404 page.
    location ^~ {{.SecretPath}} {
        proxy_pass              http://{{.XrayAddr}};
        proxy_http_version      1.1;
        proxy_set_header        Host $host;
        proxy_set_header        X-Real-IP $remote_addr;
        proxy_set_header        X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header        X-Forwarded-Proto $scheme;
        proxy_buffering         off;
        proxy_request_buffering off;
        proxy_socket_keepalive  on;
        proxy_read_timeout      1h;
        proxy_send_timeout      1h;
        client_max_body_size    0;

        # A request that reaches this location without a valid credential is not a client. Xray
        # answers it with a bare 400 and an X-Padding header that names the transport, which is
        # more than a scanner needs. Answer with the site's own 404 instead: a real client never
        # gets a 4xx here, so nothing legitimate changes.
        proxy_intercept_errors on;
        error_page 400 502 504 =404 /404.html;
    }
{{end}}
    {{- if .WebSocketPath}}
    # The second rendering of the same profile: VLESS over WebSocket, for a client that cannot read
    # the XHTTP one. Same certificate, same client credential, its own path and its own inbound.
    location ^~ {{.WebSocketPath}} {
        proxy_pass              http://{{.WebSocketAddr}};
        proxy_http_version      1.1;
        proxy_set_header        Upgrade $http_upgrade;
        proxy_set_header        Connection "upgrade";
        proxy_set_header        Host $host;
        proxy_set_header        X-Real-IP $remote_addr;
        proxy_set_header        X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header        X-Forwarded-Proto $scheme;
        proxy_buffering         off;
        proxy_read_timeout      1h;
        proxy_send_timeout      1h;
        proxy_intercept_errors  on;
        error_page 400 502 504 =404 /404.html;
    }
    {{- end}}
    include {{.Fragment}};

    access_log {{.AccessLog}};
    error_log  {{.ErrorLog}};
}
`))

// GenerateSite renders the vhost file. A node without a certificate gets the plain-HTTP shape so
// that ACME can complete; the next run, once the certificate exists, writes the TLS shape.
func GenerateSite(spec SiteSpec) ([]byte, error) {
	if spec.HTTPPort == 0 {
		spec.HTTPPort = DefaultHTTPPort
	}
	if spec.HTTPSPort == 0 {
		spec.HTTPSPort = DefaultHTTPSPort
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	tmpl := httpOnlyTemplate
	if spec.TLS() {
		tmpl = tlsTemplate
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, spec); err != nil {
		return nil, fmt.Errorf("web: render vhost: %w", err)
	}
	return buf.Bytes(), nil
}
