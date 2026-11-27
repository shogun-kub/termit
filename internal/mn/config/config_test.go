package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shogun-kub/termit/internal/i18n"
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
	if got := cfg.Policy.MeshPollInterval(); got != config.DefaultMeshPoll {
		t.Errorf("mesh poll = %v, want the default %v", got, config.DefaultMeshPoll)
	}
}

func TestMeshPollZeroIsADecision(t *testing.T) {
	t.Parallel()
	// An absent key means the default; an explicit 0 means the operator does not want the poll, and
	// the loader must keep the two apart — that is why the field is a pointer.
	absent, err := config.Parse(strings.NewReader("domain: mn.example.com\n"))
	if err != nil {
		t.Fatalf("Parse without the key: %v", err)
	}
	if got := absent.Policy.MeshPollInterval(); got != config.DefaultMeshPoll {
		t.Errorf("without the key the poll is %v, want %v", got, config.DefaultMeshPoll)
	}

	off, err := config.Parse(strings.NewReader("domain: mn.example.com\npolicy:\n  mesh_poll: 0\n"))
	if err != nil {
		t.Fatalf("Parse with 0: %v", err)
	}
	if off.Policy.MeshPoll != nil && *off.Policy.MeshPoll != 0 {
		t.Errorf("mesh_poll was read as %v", *off.Policy.MeshPoll)
	}
	if got := off.Policy.MeshPollInterval(); got != 0 {
		t.Errorf("mesh_poll: 0 became %v, want the poll turned off", got)
	}

	negative := config.PollInterval(-time.Minute)
	bad := config.Default()
	bad.Policy.MeshPoll = &negative
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "mesh_poll") {
		t.Errorf("a negative mesh_poll = %v, want a refusal naming it", err)
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

// A mesh block is what an operator writes by hand while the mesh is new, so each mistake is refused where
// it is written: a name a log line cannot use, a URL the puller would have to guess about, a pin that is
// not a hash, and two peers under one name (their learned mesh ids would share one cursor).
func TestMeshPeersAreValidated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		peer config.Peer
		want string
	}{
		{name: "good", peer: config.Peer{Name: "mn2", URL: "https://mn2.example.com/p9x"}},
		{name: "loopback", peer: config.Peer{Name: "mn2", URL: "http://127.0.0.1:8080/p9x"}},
		{name: "pinned",
			peer: config.Peer{Name: "mn2", URL: "https://mn2.example.com/p9x", LeafSHA256: "sha256:00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"}},
		{name: "no name", peer: config.Peer{URL: "https://mn2.example.com/p9x"}, want: "name is empty"},
		{name: "no url", peer: config.Peer{Name: "mn2"}, want: "url is empty"},
		{name: "shorthand", peer: config.Peer{Name: "mn2", URL: "mn2.example.com/p9x"}, want: "https://"},
		{name: "query", peer: config.Peer{Name: "mn2", URL: "https://mn2.example.com/p9x?x=1"}, want: "query"},
		{name: "bad pin", peer: config.Peer{Name: "mn2", URL: "https://mn2.example.com/p9x", LeafSHA256: "zz"}, want: "not hex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Default()
			cfg.Domain = "mn.example.com"
			cfg.Mesh.Peers = []config.Peer{tc.peer}
			err := cfg.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Validate refused a sound peer: %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("Validate accepted %+v, want %q", tc.peer, tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("Validate said %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestTwoPeersCannotShareAName(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Domain = "mn.example.com"
	cfg.Mesh.Peers = []config.Peer{
		{Name: "mn2", URL: "https://mn2.example.com/p9x"},
		{Name: "mn2", URL: "https://mn3.example.com/p9x"},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Fatalf("Validate = %v, want it to refuse the repeated name", err)
	}
}

// The floor is declared, never enforced, and a value nothing can be ordered against is refused where it
// is written: every node would otherwise read "cannot compare" and the operator would believe a rule is
// in force.
func TestMinAgentVersionMustBeARelease(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value string
		want  string
	}{
		{value: "", want: ""},
		{value: "v0.7.0", want: ""},
		{value: "v0.7.0p4", want: "not a release"},
		{value: "0.7.0", want: "not a release"},
		{value: "latest", want: "not a release"},
	}
	for _, tc := range cases {
		cfg := config.Default()
		cfg.Domain = "mn.example.com"
		cfg.MinAgentVersion = tc.value
		err := cfg.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%q was refused: %v", tc.value, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%q = %v, want an error mentioning %q", tc.value, err, tc.want)
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

// TestSubscriptionFragmentationRoundTrips pins the two client-side switches: off by default, carried
// through a save/load cycle when set, and unknown keys under the section are still refused.
func TestHappFragmentIsValidatedBeforeItReachesALink(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"100-200,1-3,tlshello,6", "300-500,1-5,tlshello", "1-3,1-3,1-2"} {
		cfg, err := config.Parse(strings.NewReader("domain: mn.example.com\nsubscription:\n  happ_fragment: \"" + value + "\"\n"))
		if err != nil {
			t.Fatalf("Parse rejected %q: %v", value, err)
		}
		if cfg.Subscription.HappFragment != value {
			t.Errorf("happ_fragment came back as %q", cfg.Subscription.HappFragment)
		}
	}
	// An ampersand or a hash would end the parameter and start something else in the URI, so the
	// value is matched against the shape Happ documents rather than escaped and hoped for.
	for _, value := range []string{"100-200&fragment=1", "tlshello", "100-200,1-3,", "100-200,1-3,tlshello#x"} {
		_, err := config.Parse(strings.NewReader("domain: mn.example.com\nsubscription:\n  happ_fragment: \"" + value + "\"\n"))
		if err == nil {
			t.Errorf("Parse accepted happ_fragment %q", value)
		}
	}
}

func TestSubscriptionFragmentationRoundTrips(t *testing.T) {
	t.Parallel()
	// A fresh server starts with fragmentation on, and says so in the file it writes: the defaults
	// live there, not in the loader, so a configuration that predates the keys keeps its behaviour.
	fresh := config.Default()
	if !fresh.Subscription.RecordFragment || fresh.Subscription.HappFragment != config.DefaultHappFragment {
		t.Errorf("a fresh server does not default to fragmentation: %+v", fresh.Subscription)
	}
	if fresh.Subscription.Fragment {
		t.Errorf("the segment-splitting option is on by default: %+v", fresh.Subscription)
	}
	// A file without the keys is a file that decided nothing: it stays off rather than acquiring a
	// behaviour on upgrade.
	cfg, err := config.Parse(strings.NewReader("domain: mn.example.com\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Subscription.Fragment || cfg.Subscription.RecordFragment || cfg.Subscription.HappFragment != "" {
		t.Errorf("a file without the keys gained fragmentation: %+v", cfg.Subscription)
	}

	cfg.Subscription = config.Subscription{Fragment: true, RecordFragment: true, HappFragment: "300-500,1-5,tlshello"}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reloaded.Subscription.Fragment || !reloaded.Subscription.RecordFragment ||
		reloaded.Subscription.HappFragment != "300-500,1-5,tlshello" {
		t.Errorf("subscription options did not survive the round trip: %+v", reloaded.Subscription)
	}

	if _, err := config.Parse(strings.NewReader("domain: mn.example.com\nsubscription:\n  nonsense: 1\n")); err == nil {
		t.Error("Parse accepted an unknown key under subscription")
	}
}

func TestLocaleIsRussianByDefaultAndValidated(t *testing.T) {
	t.Parallel()
	if cfg := config.Default(); cfg.Locale != "ru" || cfg.Lang() != i18n.RU {
		t.Errorf("a fresh server speaks %q", cfg.Locale)
	}
	cfg, err := config.Parse(strings.NewReader("domain: mn.example.com\nlocale: en\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Lang() != i18n.EN {
		t.Errorf("locale en parsed as %q", cfg.Lang())
	}
	if _, err := config.Parse(strings.NewReader("domain: mn.example.com\nlocale: de\n")); err == nil {
		t.Error("Parse accepted an unknown language")
	}
}
