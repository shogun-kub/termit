package web_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/uv/web"
)

func baseSpec() web.SiteSpec {
	return web.SiteSpec{
		Domain:      "vpn.example.com",
		SiteDir:     "/var/www/termit/site",
		SecretPath:  "/a1b2c3d4e5f60718/",
		XrayAddr:    "127.0.0.1:8443",
		ACMEWebroot: "/var/www/termit/acme",
		Fragment:    "/var/lib/termit/uv/nginx/decoy.conf",
		AccessLog:   "/var/log/nginx/termit.access.log",
		ErrorLog:    "/var/log/nginx/termit.error.log",
	}
}

// TestGoldenSites pins both shapes of the vhost: before a certificate exists and after it does.
func TestGoldenSites(t *testing.T) {
	t.Parallel()
	tls := baseSpec()
	tls.CertPath = "/var/lib/termit/uv/certs/fullchain.pem"
	tls.KeyPath = "/var/lib/termit/uv/certs/privkey.pem"

	cases := []struct {
		golden string
		spec   web.SiteSpec
	}{
		{"site-http.conf", baseSpec()},
		{"site-tls.conf", tls},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			data, err := web.GenerateSite(tc.spec)
			if err != nil {
				t.Fatalf("GenerateSite: %v", err)
			}
			golden := filepath.Join("testdata", tc.golden)
			if *update {
				if err := os.WriteFile(golden, data, 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden) // #nosec G304 -- a fixed test path.
			if err != nil {
				t.Fatalf("read golden file: %v (run `go test ./internal/uv/web -update`)", err)
			}
			if !bytes.Equal(want, data) {
				t.Errorf("%s differs from the generated vhost; regenerate with -update and review the diff", golden)
			}
		})
	}
}

func TestGenerateSiteShape(t *testing.T) {
	t.Parallel()
	t.Run("no certificate", func(t *testing.T) {
		t.Parallel()
		data, err := web.GenerateSite(baseSpec())
		if err != nil {
			t.Fatalf("GenerateSite: %v", err)
		}
		page := string(data)
		for _, want := range []string{
			"listen       80;",
			"server_name  vpn.example.com;",
			"root  /var/www/termit/site;",
			"location ^~ /.well-known/acme-challenge/",
			"include /var/lib/termit/uv/nginx/decoy.conf;",
			"charset      utf-8;",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("the http vhost is missing %q", want)
			}
		}
		for _, forbidden := range []string{"ssl_certificate", ":443", "proxy_pass"} {
			if strings.Contains(page, forbidden) {
				t.Errorf("the http vhost contains %q, which needs a certificate first", forbidden)
			}
		}
	})

	t.Run("with a certificate", func(t *testing.T) {
		t.Parallel()
		spec := baseSpec()
		spec.CertPath = "/certs/fullchain.pem"
		spec.KeyPath = "/certs/privkey.pem"
		data, err := web.GenerateSite(spec)
		if err != nil {
			t.Fatalf("GenerateSite: %v", err)
		}
		page := string(data)
		for _, want := range []string{
			"listen               443 ssl;",
			"http2                on;",
			"ssl_certificate      /certs/fullchain.pem;",
			"ssl_certificate_key  /certs/privkey.pem;",
			"return 301 https://$host$request_uri;",
			"location ^~ /a1b2c3d4e5f60718/ {",
			"proxy_pass              http://127.0.0.1:8443;",
			"proxy_buffering         off;",
			"client_max_body_size    0;",
			"add_header Strict-Transport-Security",
			"gzip_vary on;",
			"proxy_intercept_errors on;",
			"error_page 400 502 504 =404 /404.html;",
			"expires 24h;",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("the tls vhost is missing %q", want)
			}
		}
		// The decoy must be reachable on :80 for ACME even after TLS is on.
		if !strings.Contains(page, "location ^~ /.well-known/acme-challenge/") {
			t.Error("the http vhost lost the ACME location, so renewal would fail")
		}
	})

	t.Run("no proxy before the path exists", func(t *testing.T) {
		t.Parallel()
		spec := baseSpec()
		spec.SecretPath = ""
		spec.XrayAddr = ""
		data, err := web.GenerateSite(spec)
		if err != nil {
			t.Fatalf("GenerateSite: %v", err)
		}
		if strings.Contains(string(data), "proxy_pass") {
			t.Error("a vhost without a secret path still proxies")
		}
	})
}

func TestGenerateSiteRefuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, want string
		mutate     func(*web.SiteSpec)
	}{
		{"bad domain", "host name", func(s *web.SiteSpec) { s.Domain = "not a domain" }},
		{"relative site dir", "absolute", func(s *web.SiteSpec) { s.SiteDir = "site" }},
		{"newline in a path", "nginx would misread", func(s *web.SiteSpec) { s.SiteDir = "/var/www/a\nb" }},
		{"cert without key", "given together", func(s *web.SiteSpec) { s.CertPath = "/certs/fullchain.pem" }},
		{"path without slashes", "start and end with /", func(s *web.SiteSpec) { s.SecretPath = "/abc" }},
		{"path with a semicolon", "nginx would misread", func(s *web.SiteSpec) { s.SecretPath = "/a;b/" }},
		{"public xray address", "loopback", func(s *web.SiteSpec) { s.XrayAddr = "0.0.0.0:8443" }},
		{"path without an address", "forward nowhere", func(s *web.SiteSpec) { s.XrayAddr = "" }},
		{"port out of range", "out of range", func(s *web.SiteSpec) { s.HTTPSPort = 70000 }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := baseSpec()
			tc.mutate(&spec)
			_, err := web.GenerateSite(spec)
			if err == nil {
				t.Fatal("GenerateSite accepted a spec it must refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
