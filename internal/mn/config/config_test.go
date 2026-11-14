package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/mn/config"
)

func TestParseAppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader("domain: mn.example.com\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Listen != ":443" {
		t.Errorf("listen = %q, want :443", cfg.Listen)
	}
	if cfg.DataDir != "/var/lib/termit/mn" {
		t.Errorf("data_dir = %q", cfg.DataDir)
	}
	if cfg.DBPath != "/var/lib/termit/mn/termit.db" {
		t.Errorf("db_path = %q", cfg.DBPath)
	}
	if cfg.Enrollment.Mode != config.ModeInviteApproval {
		t.Errorf("enrollment mode = %q, want the safe default", cfg.Enrollment.Mode)
	}
	if cfg.Policy.BlockAfterFailedCycles != 2 || cfg.Policy.ProbeRetries != 3 {
		t.Errorf("policy defaults = %+v", cfg.Policy)
	}
	if cfg.Policy.CheckInterval != 24*time.Hour {
		t.Errorf("check interval = %v, want 24h", cfg.Policy.CheckInterval)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	_, err := config.Parse(strings.NewReader("domain: mn.example.com\nnonsense: 1\n"))
	if err == nil {
		t.Fatal("Parse accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("error %q does not name the unknown key", err)
	}
}

func TestParseRejectsANewerSchema(t *testing.T) {
	t.Parallel()
	_, err := config.Parse(strings.NewReader("schema_version: 2\ndomain: mn.example.com\n"))
	if err == nil {
		t.Fatal("Parse accepted a newer schema")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error %q does not mention the schema", err)
	}
}

func TestParseRejectsTwoDocuments(t *testing.T) {
	t.Parallel()
	_, err := config.Parse(strings.NewReader("domain: mn.example.com\n---\ndomain: other.example.com\n"))
	if err == nil {
		t.Fatal("Parse accepted two documents")
	}
}

func TestValidateCollectsProblems(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "not a domain"
	cfg.Listen = "443"
	cfg.Enrollment.Mode = "whatever"
	cfg.Policy.ProbeRetries = 0
	cfg.ACME.Email = "not-an-address"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted a broken configuration")
	}
	for _, want := range []string{"domain", "listen", "enrollment.mode", "probe_retries", "email"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []string
		want    string
	}{
		{name: "an address", entries: []string{"192.0.2.10"}},
		{name: "a block", entries: []string{"10.0.0.0/8"}},
		{name: "several", entries: []string{"192.0.2.10", "10.0.0.0/8", "2001:db8::/32"}},
		{name: "empty means nobody is trusted", entries: nil},
		{name: "a name is refused", entries: []string{"proxy.example"}, want: "neither an address nor a CIDR"},
		{name: "a broken block is refused", entries: []string{"10.0.0.0/99"}, want: "not a CIDR block"},
		{name: "an empty entry is refused", entries: []string{"  "}, want: "empty entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.Domain = "mn.example.com"
			cfg.TrustedProxies = tc.entries
			err := cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate(%v): %v", tc.entries, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate(%v) = %v, want one containing %q", tc.entries, err, tc.want)
			}
		})
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := config.Default()
	cfg.Domain = "mn.example.com"
	cfg.Enrollment.Mode = config.ModeInvite
	cfg.Policy.CheckInterval = 12 * time.Hour
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Domain != cfg.Domain || loaded.Enrollment.Mode != cfg.Enrollment.Mode {
		t.Errorf("round trip changed the configuration: %+v", loaded)
	}
	if loaded.Policy.CheckInterval != 12*time.Hour {
		t.Errorf("check interval = %v, want 12h", loaded.Policy.CheckInterval)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o400 == 0 {
		t.Errorf("configuration is not readable by its owner: %04o", info.Mode().Perm())
	}
}

func TestCertDir(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.DataDir = "/srv/termit"
	if got, want := cfg.CertDir(), "/srv/termit/certs"; got != want {
		t.Errorf("CertDir = %q, want %q", got, want)
	}
	if got := cfg.ListenPort(); got != 443 {
		t.Errorf("ListenPort = %d, want 443", got)
	}
}

func TestAPIPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prefix string
		ok     bool
	}{
		{"random", "/k3f9x2q7m1zp", true},
		{"versioned", "/api/v1", true},
		{"nested", "/a/b/c", true},
		{"no slash", "api", false},
		{"trailing slash", "/api/", false},
		{"bare slash", "/", false},
		{"space", "/api v1", false},
		{"empty", "", false},
		{"query-ish", "/api?v=1", false},
		{"too long", "/" + strings.Repeat("a", 128), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Default()
			c.Domain = "mn.example.net"
			c.APIPrefix = tc.prefix
			err := c.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate(%q) = %v", tc.prefix, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Validate(%q) accepted an unusable prefix", tc.prefix)
			}
		})
	}
}

// A configuration written before the option existed has no api_prefix, and must keep working on the path
// it has always served.
func TestAPIPrefixDefaultsForAnOldConfiguration(t *testing.T) {
	t.Parallel()
	c, err := config.Parse(strings.NewReader("schema_version: 1\ndomain: mn.example.net\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.APIPrefix != config.DefaultAPIPrefix {
		t.Errorf("api_prefix = %q, want %q", c.APIPrefix, config.DefaultAPIPrefix)
	}
}

func TestAPIPrefixSurvivesARoundTrip(t *testing.T) {
	t.Parallel()
	c := config.Default()
	c.Domain = "mn.example.net"
	c.APIPrefix = "/k3f9x2q7m1zp"
	data, err := c.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	again, err := config.Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if again.APIPrefix != "/k3f9x2q7m1zp" {
		t.Errorf("api_prefix = %q after a round trip", again.APIPrefix)
	}
}

func TestDecoyTemplate(t *testing.T) {
	t.Parallel()
	c := config.Default()
	c.Domain = "mn.example.net"
	c.Decoy.Template = "blog"
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted a template this build cannot generate")
	}
	c.Decoy.Template = config.DefaultDecoyTemplate
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate(%q) = %v", config.DefaultDecoyTemplate, err)
	}
	// A configuration written before the decoy existed gets the default, not an empty archetype.
	old, err := config.Parse(strings.NewReader("schema_version: 1\ndomain: mn.example.net\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if old.Decoy.Template != config.DefaultDecoyTemplate {
		t.Errorf("decoy.template = %q, want %q", old.Decoy.Template, config.DefaultDecoyTemplate)
	}
}
