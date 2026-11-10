package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/uv/config"
)

const minimal = "schema_version: 1\ndomain: vpn.example.com\n"

func TestParseAppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SchemaVersion != config.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, config.SchemaVersion)
	}
	if cfg.ACME.Directory != acme.DefaultDirectory {
		t.Errorf("ACME.Directory = %q, want %q", cfg.ACME.Directory, acme.DefaultDirectory)
	}
	if cfg.ACME.RenewBeforeDays != config.DefaultRenewBeforeDays {
		t.Errorf("RenewBeforeDays = %d, want %d", cfg.ACME.RenewBeforeDays, config.DefaultRenewBeforeDays)
	}
	if cfg.Xray.Version != config.DefaultXrayVersion {
		t.Errorf("Xray.Version = %q, want %q", cfg.Xray.Version, config.DefaultXrayVersion)
	}
	if cfg.Xray.DownloadBase != config.DefaultXrayDownloadBase {
		t.Errorf("Xray.DownloadBase = %q, want %q", cfg.Xray.DownloadBase, config.DefaultXrayDownloadBase)
	}
	if cfg.Decoy.Template != config.DefaultDecoyTemplate {
		t.Errorf("Decoy.Template = %q, want %q", cfg.Decoy.Template, config.DefaultDecoyTemplate)
	}
	if cfg.Paths.StateDir != "/var/lib/termit/uv" {
		t.Errorf("Paths.StateDir = %q", cfg.Paths.StateDir)
	}
	if got, want := cfg.Paths.StatePath(), "/var/lib/termit/uv/state.json"; got != want {
		t.Errorf("StatePath = %q, want %q", got, want)
	}
	if got, want := cfg.Paths.XrayBinary(), "/usr/lib/termit/bin/xray"; got != want {
		t.Errorf("XrayBinary = %q, want %q", got, want)
	}
}

func TestParseDefaultsSchemaVersion(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader("domain: vpn.example.com\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SchemaVersion != config.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, config.SchemaVersion)
	}
}

func TestParseNormalisesDomain(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader("domain: \"  VPN.Example.COM. \"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Domain != "vpn.example.com" {
		t.Errorf("Domain = %q, want vpn.example.com", cfg.Domain)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	t.Parallel()
	_, err := config.Parse(strings.NewReader(minimal + "surprise: true\n"))
	if err == nil {
		t.Fatal("Parse accepted an unknown field")
	}
	if !strings.Contains(err.Error(), "surprise") {
		t.Errorf("error %q does not name the unknown field", err)
	}
}

func TestParseRejectsSecondDocument(t *testing.T) {
	t.Parallel()
	_, err := config.Parse(strings.NewReader(minimal + "---\ndomain: other.example.com\n"))
	if err == nil {
		t.Fatal("Parse accepted two documents")
	}
}

func TestParseRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	_, err := config.Parse(strings.NewReader("schema_version: 99\ndomain: vpn.example.com\n"))
	if err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("Parse error = %v, want a schema version rejection", err)
	}
}

func TestValidateDomain(t *testing.T) {
	t.Parallel()
	valid := []string{"vpn.example.com", "a.b", "x1.example", "termit-uv1.lan"}
	for _, domain := range valid {
		cfg := config.Default()
		cfg.Domain = domain
		if err := cfg.Validate(); err != nil {
			t.Errorf("domain %q rejected: %v", domain, err)
		}
	}

	invalid := map[string]string{
		"":                               "required",
		"localhost":                      "fully qualified",
		"192.0.2.10":                     "IP address",
		"-bad.example.com":               "hyphen",
		"bad-.example.com":               "hyphen",
		"double..example.com":            "empty label",
		"spaces in.example.com":          "not allowed",
		strings.Repeat("a", 64) + ".com": "longer than 63",
	}
	for domain, want := range invalid {
		cfg := config.Default()
		cfg.Domain = domain
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("domain %q: error = %v, want one containing %q", domain, err, want)
		}
	}
}

func TestValidateRenewBeforeDays(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg.ACME.RenewBeforeDays = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("renew_before_days 0 accepted")
	}
	cfg.ACME.RenewBeforeDays = 90
	if err := cfg.Validate(); err == nil {
		t.Fatal("renew_before_days 90 accepted")
	}
	cfg.ACME.RenewBeforeDays = 30
	if err := cfg.Validate(); err != nil {
		t.Errorf("renew_before_days 30 rejected: %v", err)
	}
}

func TestValidateDownloadBase(t *testing.T) {
	t.Parallel()
	valid := []string{
		"https://github.com/XTLS/Xray-core/releases/download",
		"http://mirror.lan/xray",
		"file:///srv/xray",
		"/srv/xray",
	}
	for _, base := range valid {
		cfg := config.Default()
		cfg.Domain = "vpn.example.com"
		cfg.Xray.DownloadBase = base
		if err := cfg.Validate(); err != nil {
			t.Errorf("download_base %q rejected: %v", base, err)
		}
	}
	for _, base := range []string{"ftp://mirror/x", "relative/dir", "https://"} {
		cfg := config.Default()
		cfg.Domain = "vpn.example.com"
		cfg.Xray.DownloadBase = base
		if err := cfg.Validate(); err == nil {
			t.Errorf("download_base %q accepted", base)
		}
	}
}

func TestValidateRejectsBadDigest(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg.Xray.SHA256 = map[string]string{"amd64": "not-a-digest"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid digest accepted")
	}
	cfg.Xray.SHA256 = map[string]string{"amd64": strings.Repeat("a", 64)}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid digest rejected: %v", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = ""
	cfg.ACME.RenewBeforeDays = 200
	cfg.Decoy.Template = "Not A Template"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate accepted an invalid configuration")
	}
	for _, want := range []string{"domain is required", "renew_before_days", "decoy.template"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filename := filepath.Join(dir, "config.yaml")

	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	cfg.Email = "owner@example.com"
	cfg.ACME.SkipTLSVerify = true
	cfg.Xray.SHA256 = map[string]string{"amd64": strings.Repeat("b", 64)}
	if err := cfg.Save(filename); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := config.Load(filename)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Domain != cfg.Domain || loaded.Email != cfg.Email || !loaded.ACME.SkipTLSVerify {
		t.Errorf("round trip changed fields: %+v", loaded)
	}
	if loaded.Xray.SHA256["amd64"] != cfg.Xray.SHA256["amd64"] {
		t.Errorf("digest override lost: %v", loaded.Xray.SHA256)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("mode = %o, want 644", perm)
		}
	}
}

func TestMarshalIsDeterministic(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "vpn.example.com"
	first, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	second, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("Marshal is not deterministic")
	}
	if !strings.HasPrefix(string(first), "# Termit node configuration") {
		t.Errorf("Marshal lost its header:\n%s", first)
	}
}

// WARP is on by default: an application on a client device that asks what address it is coming from
// must not see the node's address. The old enabled switch survives as a migration.
func TestWarpDefaultsToAll(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Warp.Mode != config.WarpAll {
		t.Errorf("warp.mode = %q, want %q", cfg.Warp.Mode, config.WarpAll)
	}
	if !cfg.Warp.FailOpenEnabled() {
		t.Error("warp fails closed by default")
	}
	if !cfg.Warp.UsesWarp() {
		t.Error("the default configuration does not use WARP")
	}
}

func TestWarpMigratesTheOldEnabledSwitch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{"the old off switch", "warp:\n  enabled: false\n", config.WarpOff},
		{"the old on switch", "warp:\n  enabled: true\n", config.WarpAll},
		{"no switch at all", "", config.WarpAll},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := config.Parse(strings.NewReader(minimal + tc.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg.Warp.Mode != tc.want {
				t.Errorf("mode = %q, want %q", cfg.Warp.Mode, tc.want)
			}
			// Saving must not write the key it was migrated from.
			out, err := cfg.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if strings.Contains(string(out), "\n  enabled:") {
				t.Errorf("the old switch survived a save:\n%s", out)
			}
		})
	}
}

func TestWarpSelectedMode(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader(minimal +
		"warp:\n  mode: selected\n  selected:\n    - Example.COM.\n    - b.example.com\n    - example.com\n  fail_open: false\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(cfg.Warp.Selected, ","); got != "b.example.com,example.com" {
		t.Errorf("selected = %q, want the list normalised and sorted", got)
	}
	if cfg.Warp.FailOpenEnabled() {
		t.Error("fail_open: false was ignored")
	}

	if _, err := config.Parse(strings.NewReader(minimal + "warp:\n  mode: selected\n")); err == nil {
		t.Error("selected mode without a list was accepted")
	}
	if _, err := config.Parse(strings.NewReader(minimal + "warp:\n  mode: somewhere\n")); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestValidateMNURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"empty", "", true},
		{"plain", "https://mn.example.com", true},
		{"with prefix", "https://mn.example.com/6rfjdqfn5nno", true},
		{"port", "https://mn.example.com:8443/api", true},
		{"http", "http://127.0.0.1:8181/p", true},
		{"no scheme", "mn.example.com", false},
		{"wrong scheme", "ftp://mn.example.com", false},
		{"no host", "https://", false},
		{"query", "https://mn.example.com/x?a=1", false},
		{"fragment", "https://mn.example.com/x#f", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Default()
			c.Domain = "node.example.com"
			c.MN.URL = tc.url
			err := c.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate(%q) = %v", tc.url, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Validate(%q) accepted an unusable URL", tc.url)
			}
		})
	}
}
