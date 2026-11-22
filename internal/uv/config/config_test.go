package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/i18n"
	"github.com/shogun-kub/termit/internal/uv/config"
)

const minimal = "schema_version: 1\ndomain: node.example.com\n"

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
	cfg, err := config.Parse(strings.NewReader("domain: node.example.com\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SchemaVersion != config.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, config.SchemaVersion)
	}
}

func TestParseNormalisesDomain(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse(strings.NewReader("domain: \"  NODE.Example.COM. \"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Domain != "node.example.com" {
		t.Errorf("Domain = %q, want node.example.com", cfg.Domain)
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
	_, err := config.Parse(strings.NewReader("schema_version: 99\ndomain: node.example.com\n"))
	if err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("Parse error = %v, want a schema version rejection", err)
	}
}

func TestValidateDomain(t *testing.T) {
	t.Parallel()
	valid := []string{"node.example.com", "a.b", "x1.example", "termit-uv1.lan"}
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
		cfg.Domain = "node.example.com"
		cfg.Xray.DownloadBase = base
		if err := cfg.Validate(); err != nil {
			t.Errorf("download_base %q rejected: %v", base, err)
		}
	}
	for _, base := range []string{"ftp://mirror/x", "relative/dir", "https://"} {
		cfg := config.Default()
		cfg.Domain = "node.example.com"
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
	cfg.Domain = "node.example.com"
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
	cfg.Domain = "node.example.com"
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

func TestLocaleDefaultsToRussian(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if cfg.Locale != "ru" {
		t.Errorf("a fresh configuration speaks %q, want Russian", cfg.Locale)
	}
	if cfg.Lang() != i18n.RU {
		t.Errorf("Lang() = %q", cfg.Lang())
	}

	cfg.Domain = "node.example.com"
	cfg.Locale = "en"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Lang() != i18n.EN {
		t.Errorf("the chosen language did not survive the round trip: %q", reloaded.Locale)
	}
}

func TestLocaleRejectsALanguageTheBuildDoesNotSpeak(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "node.example.com"
	cfg.Locale = "de"
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted an unknown language")
	}
	// A configuration written before the key existed has no locale at all, and must gain the default
	// rather than fail to load.
	cfg.Locale = ""
	if _, ok := i18n.Parse(cfg.Locale); ok {
		t.Error("an empty locale parsed as a language; the default is applied on load instead")
	}
}

// TestWarpTransportDefaultsAndMigration is the switch between the two ways WARP carries traffic. The
// rules it pins: a file written before the key existed keeps the proxy it was installed with, a fresh
// one writes wireguard, and flipping the key changes nothing else about the file.
func TestWarpTransportDefaultsAndMigration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		document  string
		want      string
		wireguard bool
	}{
		{
			name:     "a configuration written before the key existed",
			document: "schema_version: 1\ndomain: node.example.com\nemail: a@example.com\nwarp:\n  mode: all\n",
			want:     config.WarpTransportProxy,
		},
		{
			name:      "an explicit wireguard",
			document:  "schema_version: 1\ndomain: node.example.com\nemail: a@example.com\nwarp:\n  mode: all\n  transport: wireguard\n",
			want:      config.WarpTransportWireGuard,
			wireguard: true,
		},
		{
			name:     "an explicit proxy",
			document: "schema_version: 1\ndomain: node.example.com\nemail: a@example.com\nwarp:\n  mode: off\n  transport: proxy\n",
			want:     config.WarpTransportProxy,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := config.Parse(strings.NewReader(tc.document))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := cfg.Warp.TransportOrDefault(); got != tc.want {
				t.Errorf("transport = %q, want %q", got, tc.want)
			}
			if got := cfg.Warp.UsesWireGuard(); got != tc.wireguard {
				t.Errorf("UsesWireGuard = %v, want %v", got, tc.wireguard)
			}
			// The old file must not gain a key it did not have: a rewrite by an operator's editor is the
			// only thing that should add one.
			if strings.Contains(tc.document, "transport:") && cfg.Warp.Transport != tc.want {
				t.Errorf("an explicit value was lost: %q", cfg.Warp.Transport)
			}
			if !strings.Contains(tc.document, "transport:") && cfg.Warp.Transport != "" {
				t.Errorf("an absent key became %q", cfg.Warp.Transport)
			}
		})
	}

	// A fresh configuration starts on the transport that carries UDP, and refuses a value that is
	// neither of the two.
	fresh := config.Default()
	if got := fresh.Warp.TransportOrDefault(); got != config.WarpTransportWireGuard {
		t.Errorf("a fresh configuration uses %q, want wireguard", got)
	}
	broken := "schema_version: 1\ndomain: node.example.com\nemail: a@example.com\nwarp:\n  mode: all\n  transport: masque\n"
	if _, err := config.Parse(strings.NewReader(broken)); err == nil ||
		!strings.Contains(err.Error(), "must be proxy, wireguard or usque") {
		t.Fatalf("Parse = %v, want a refusal naming the transports", err)
	}
	// Every transport this build knows must load, and usque is the one that carries UDP without the
	// core: a typo in its name would silently fall back to the documented behaviour of an old file.
	for _, transport := range []string{"proxy", "wireguard", "usque"} {
		body := "schema_version: 1\ndomain: node.example.com\nemail: a@example.com\nwarp:\n  mode: all\n  transport: " + transport + "\n"
		parsed, err := config.Parse(strings.NewReader(body))
		if err != nil {
			t.Fatalf("transport %q: %v", transport, err)
		}
		if parsed.Warp.TransportOrDefault() != transport {
			t.Errorf("transport %q parsed as %q", transport, parsed.Warp.TransportOrDefault())
		}
	}
}

// The family is the owner's choice, and the default is IPv4: Cloudflare's IPv6 egress is what large
// services block, and a provider's own IPv6 is not the node's to fix.
func TestWarpFamilyDefaultsToIPv4(t *testing.T) {
	t.Parallel()
	if got := config.Default().Warp.FamilyOrDefault(); got != config.WarpFamilyIPv4 {
		t.Errorf("a fresh configuration uses %q, want ipv4", got)
	}
	if got := (config.Warp{}).FamilyOrDefault(); got != config.WarpFamilyIPv4 {
		t.Errorf("an absent key means %q, want ipv4", got)
	}
	document := "schema_version: 1\ndomain: node.example.com\nemail: a@example.com\nwarp:\n  mode: all\n  family: ipv6\n"
	cfg, err := config.Parse(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Warp.FamilyOrDefault() != config.WarpFamilyIPv6 {
		t.Errorf("family = %q, want the configured ipv6", cfg.Warp.Family)
	}
	broken := "schema_version: 1\ndomain: node.example.com\nemail: a@example.com\nwarp:\n  mode: all\n  family: ipv5\n"
	if _, err := config.Parse(strings.NewReader(broken)); err == nil ||
		!strings.Contains(err.Error(), "must be ipv4 or ipv6") {
		t.Fatalf("Parse = %v, want a refusal naming the two families", err)
	}
}

// TestHysteriaNeedsAnOpenUDPPort: the users are derived from the pairs' UUIDs, so there is no password in
// the configuration - but a table that does not publish UDP/443 counts the transport's own traffic as
// scanning, and the guard bans for what the meter counted.
func TestHysteriaNeedsAnOpenUDPPort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no block is off", "", ""},
		{"enabled without the port", "hysteria: {}\n" +
			"security:\n  firewall:\n    enabled: true\n    udp_ports: []\n", "UDP/443"},
		{"enabled with the port", "hysteria: {}\n" +
			"security:\n  firewall:\n    enabled: true\n    udp_ports: [443]\n", ""},
		{"bad hop port", "hysteria:\n  socks_port: 70000\n", "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Parse(strings.NewReader(minimal + tc.body))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Parse refused a configuration it should accept: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("Parse accepted a configuration it should refuse")
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
