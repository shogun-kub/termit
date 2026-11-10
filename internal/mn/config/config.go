// Package config is the management server's configuration: strict YAML, one schema version, and
// defaults that make a fresh file self-explanatory.
//
// The file holds decisions only. Everything generated once — the master key, the database, tokens,
// invite codes — lives elsewhere: the key in its own 0600 file, the rest in the database.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/atomicfile"
	"github.com/shogun-kub/termit/internal/decoy"
)

// SchemaVersion is the configuration schema this build understands.
const SchemaVersion = 1

// Enrollment modes, from docs/DESCRIPTION.md §7.6.
const (
	// ModeInvite activates a node as soon as checks pass.
	ModeInvite = "invite"
	// ModeInviteApproval parks a node in pending until an operator approves it.
	ModeInviteApproval = "invite+approval"
	// ModeOpen needs no invite. Only for a private group that knows what it is doing.
	ModeOpen = "open"
)

// DefaultDecoyTemplate is the masking site a management server serves unless the operator picks another
// archetype. It is the same generator the nodes use, so the server does not look like a different
// product from its own nodes.
const DefaultDecoyTemplate = "portal"

// DefaultAPIPrefix is what a configuration written before the prefix existed, and every test, expects.
// `termit-mn init` replaces it with a random one unless the operator passes --api-prefix.
const DefaultAPIPrefix = "/api/v1"

// Config is the whole file.
type Config struct {
	SchemaVersion int    `yaml:"schema_version"`
	Domain        string `yaml:"domain"`
	Listen        string `yaml:"listen"`
	// APIPrefix is the path the node API is served under. It is random on a fresh install so a
	// scanner cannot find the API by trying the usual names; the node learns it as part of its MN URL.
	APIPrefix     string     `yaml:"api_prefix"`
	AdminSocket   string     `yaml:"admin_socket"`
	DataDir       string     `yaml:"data_dir"`
	DBPath        string     `yaml:"db_path"`
	MasterKeyFile string     `yaml:"master_key_file"`
	ACME          ACME       `yaml:"acme"`
	Decoy         Decoy      `yaml:"decoy"`
	Enrollment    Enrollment `yaml:"enrollment"`
	Policy        Policy     `yaml:"policy"`
	DNS           DNS        `yaml:"dns"`
	Prober        Prober     `yaml:"prober"`
}

// Decoy is the site the server answers a prober with: an ordinary-looking page instead of a bare 404
// that announces a machine nobody intended to be found.
type Decoy struct {
	// Template selects the archetype. Empty means DefaultDecoyTemplate.
	Template string `yaml:"template"`
}

// DNS is the second resolver enrollment asks beside the system one. Empty means the system resolver
// alone, which is what a machine without outbound DNS wants.
type DNS struct {
	Secondary string `yaml:"secondary"`
}

// Prober configures the xray-core worker the server uses to check nodes.
type Prober struct {
	// XrayBinary is the xray-core executable. Empty means no prober: every check is then "unknown",
	// and an unknown check never blocks a node.
	XrayBinary string `yaml:"xray_binary"`
	// URL is fetched through each tunnel to prove the tunnel carries traffic.
	URL string `yaml:"url"`
	// CAFile is a PEM bundle the prober trusts instead of the system roots.
	CAFile string `yaml:"ca_file"`
}

// ACME is the certificate authority this server uses for its own TLS.
type ACME struct {
	Directory string `yaml:"directory"`
	Email     string `yaml:"email"`
	// ChallengeListen is where HTTP-01 challenges are answered while an order is in flight. The
	// listener exists only for the duration of the order.
	ChallengeListen string `yaml:"challenge_listen"`
	SkipTLSVerify   bool   `yaml:"skip_tls_verify,omitempty"`
	RenewBeforeDays int    `yaml:"renew_before_days"`
}

// Enrollment decides what happens to a node that registers.
type Enrollment struct {
	Mode string `yaml:"mode"`
	// Attempts and WindowSeconds rate-limit enrollment per source address.
	Attempts      int `yaml:"attempts"`
	WindowSeconds int `yaml:"window_seconds"`
}

// Policy holds the scheduler thresholds an operator may tune without a rebuild.
type Policy struct {
	// BlockAfterFailedCycles is how many consecutive failed cycles move a node to blocked.
	BlockAfterFailedCycles int `yaml:"block_after_failed_cycles"`
	// ProbeRetries is how many attempts one cycle makes before it counts as failed.
	ProbeRetries int `yaml:"probe_retries"`
	// CheckInterval is the daily check period; the scheduler jitters it per node.
	CheckInterval time.Duration `yaml:"check_interval"`
	// SyncInterval is how often a node is asked to sync.
	SyncInterval time.Duration `yaml:"sync_interval"`
}

// Default returns a configuration with every default applied and no domain.
func Default() *Config {
	c := &Config{SchemaVersion: SchemaVersion}
	c.applyDefaults()
	return c
}

// Load reads, defaults and validates the configuration at filename.
func Load(filename string) (*Config, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- the operator chooses the path.
	if err != nil {
		return nil, err
	}
	c, err := Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", filename, err)
	}
	return c, nil
}

// Parse decodes a single YAML document, rejecting unknown keys and a newer schema.
func Parse(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	c := &Config{}
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("decode: the file must hold exactly one document")
	}
	switch {
	case c.SchemaVersion == 0:
		c.SchemaVersion = SchemaVersion
	case c.SchemaVersion > SchemaVersion:
		return nil, fmt.Errorf("schema_version %d is newer than this build understands (%d)", c.SchemaVersion, SchemaVersion)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Marshal renders the file deterministically.
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("config: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("config: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// Save writes the configuration with public permissions: it holds no secret.
func (c *Config) Save(filename string) error {
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	return atomicfile.Write(filename, data, 0o644)
}

// Validate collects every problem instead of stopping at the first one, because the operator fixes
// them in one pass.
func (c *Config) Validate() error {
	var problems []error
	if err := validateDomain(c.Domain); err != nil {
		problems = append(problems, err)
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		problems = append(problems, fmt.Errorf("listen %q is not host:port: %w", c.Listen, err))
	}
	if _, _, err := net.SplitHostPort(c.ACME.ChallengeListen); err != nil {
		problems = append(problems, fmt.Errorf("acme.challenge_listen %q is not host:port: %w", c.ACME.ChallengeListen, err))
	}
	switch c.Enrollment.Mode {
	case ModeInvite, ModeInviteApproval, ModeOpen:
	default:
		problems = append(problems, fmt.Errorf("enrollment.mode %q is not one of %s, %s, %s",
			c.Enrollment.Mode, ModeInvite, ModeInviteApproval, ModeOpen))
	}
	if c.Policy.BlockAfterFailedCycles < 1 {
		problems = append(problems, errors.New("policy.block_after_failed_cycles must be at least 1"))
	}
	if c.Policy.ProbeRetries < 1 {
		problems = append(problems, errors.New("policy.probe_retries must be at least 1"))
	}
	if c.Policy.CheckInterval <= 0 {
		problems = append(problems, errors.New("policy.check_interval must be positive"))
	}
	if c.Policy.SyncInterval <= 0 {
		problems = append(problems, errors.New("policy.sync_interval must be positive"))
	}
	if c.Enrollment.Attempts < 1 {
		problems = append(problems, errors.New("enrollment.attempts must be at least 1"))
	}
	if c.Enrollment.WindowSeconds < 1 {
		problems = append(problems, errors.New("enrollment.window_seconds must be positive"))
	}
	if c.Prober.URL != "" {
		parsed, err := url.Parse(c.Prober.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			problems = append(problems, fmt.Errorf("prober.url %q is not an http(s) URL with a host", c.Prober.URL))
		}
	}
	if err := validateAPIPrefix(c.APIPrefix); err != nil {
		problems = append(problems, err)
	}
	if !decoy.KnownTemplate(c.Decoy.Template) {
		problems = append(problems, fmt.Errorf("decoy.template %q is not one this build can generate (%s)",
			c.Decoy.Template, strings.Join(decoy.Templates(), ", ")))
	}
	if c.ACME.Directory == "" {
		problems = append(problems, errors.New("acme.directory is required"))
	}
	if c.ACME.RenewBeforeDays < 0 {
		problems = append(problems, errors.New("acme.renew_before_days cannot be negative"))
	}
	if c.ACME.Email != "" && !strings.Contains(c.ACME.Email, "@") {
		problems = append(problems, fmt.Errorf("acme.email %q is not an address", c.ACME.Email))
	}
	for name, value := range map[string]string{
		"data_dir":        c.DataDir,
		"db_path":         c.DBPath,
		"master_key_file": c.MasterKeyFile,
		"admin_socket":    c.AdminSocket,
	} {
		if !filepath.IsAbs(value) && !strings.HasPrefix(value, "/") {
			problems = append(problems, fmt.Errorf("%s must be an absolute path, got %q", name, value))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":443"
	}
	if c.APIPrefix == "" {
		c.APIPrefix = DefaultAPIPrefix
	}
	if c.Decoy.Template == "" {
		c.Decoy.Template = DefaultDecoyTemplate
	}
	if c.DataDir == "" {
		c.DataDir = "/var/lib/termit/mn"
	}
	if c.DBPath == "" {
		c.DBPath = path.Join(c.DataDir, "termit.db")
	}
	if c.MasterKeyFile == "" {
		c.MasterKeyFile = "/etc/termit/mn/master.key"
	}
	if c.AdminSocket == "" {
		c.AdminSocket = "/run/termit/mn/admin.sock"
	}
	if c.ACME.Directory == "" {
		c.ACME.Directory = acme.DefaultDirectory
	}
	if c.ACME.ChallengeListen == "" {
		c.ACME.ChallengeListen = ":80"
	}
	if c.ACME.RenewBeforeDays == 0 {
		c.ACME.RenewBeforeDays = int(acme.DefaultRenewBefore.Hours() / 24)
	}
	if c.Enrollment.Mode == "" {
		c.Enrollment.Mode = ModeInviteApproval
	}
	if c.Enrollment.Attempts == 0 {
		c.Enrollment.Attempts = 10
	}
	if c.Enrollment.WindowSeconds == 0 {
		c.Enrollment.WindowSeconds = 900
	}
	if c.Prober.URL == "" {
		c.Prober.URL = "https://example.com/"
	}
	if c.Policy.BlockAfterFailedCycles == 0 {
		c.Policy.BlockAfterFailedCycles = 2
	}
	if c.Policy.ProbeRetries == 0 {
		c.Policy.ProbeRetries = 3
	}
	if c.Policy.CheckInterval == 0 {
		c.Policy.CheckInterval = 24 * time.Hour
	}
	if c.Policy.SyncInterval == 0 {
		c.Policy.SyncInterval = 24 * time.Hour
	}
}

// CertDir is where the server's own certificate lives.
func (c *Config) CertDir() string { return path.Join(c.DataDir, "certs") }

// ListenPort is the numeric port from Listen, for logs.
func (c *Config) ListenPort() int {
	_, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return 0
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return number
}

// validateAPIPrefix keeps the prefix to what an URL path segment may contain, because it is pasted into
// every node's configuration and every documented example.
func validateAPIPrefix(prefix string) error {
	switch {
	case prefix == "":
		return errors.New("api_prefix cannot be empty")
	case !strings.HasPrefix(prefix, "/"):
		return fmt.Errorf("api_prefix %q must start with /", prefix)
	case len(prefix) < 2:
		return fmt.Errorf("api_prefix %q is too short to be a path", prefix)
	case strings.HasSuffix(prefix, "/"):
		return fmt.Errorf("api_prefix %q must not end with /", prefix)
	case len(prefix) > 128:
		return fmt.Errorf("api_prefix %q is longer than 128 characters", prefix)
	}
	for _, r := range prefix[1:] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '~' || r == '/':
		default:
			return fmt.Errorf("api_prefix %q contains %q, which an URL path cannot carry here", prefix, r)
		}
	}
	return nil
}

func validateDomain(domain string) error {
	switch {
	case domain == "":
		return errors.New("domain is required")
	case len(domain) > 253:
		return fmt.Errorf("domain %q is longer than 253 characters", domain)
	case net.ParseIP(domain) != nil:
		return fmt.Errorf("domain %q is an IP address, not a host name", domain)
	case !strings.Contains(domain, "."):
		return fmt.Errorf("domain %q has no dot", domain)
	case strings.ContainsAny(domain, " /:@?#"):
		return fmt.Errorf("domain %q contains a character a host name cannot carry", domain)
	}
	return nil
}
