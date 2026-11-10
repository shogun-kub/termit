// Package config defines the agent's configuration: the YAML schema, its defaults, validation and
// the strict loader that refuses unknown fields.
//
// The split between this file and the state file is deliberate. The configuration holds what the
// owner decides (domain, ACME account, pinned Xray release); the state holds what the agent
// generates once (identity, decoy seed, certificate). Re-running `uv init` or editing the config
// never regenerates a credential, and no secret is ever written here.
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
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/shogun-kub/termit/internal/acme"
	"github.com/shogun-kub/termit/internal/uv/state"
)

const (
	// SchemaVersion is the only configuration schema this build understands.
	SchemaVersion = 1

	// DefaultPath is where the agent reads its configuration when nothing overrides it.
	DefaultPath = "/etc/termit/uv/config.yaml"

	// PathEnv overrides DefaultPath. The command line flag wins over it.
	PathEnv = "TERMIT_UV_CONFIG"

	// DefaultXrayVersion is the Xray-core release this build was tested against and whose archive
	// digest is pinned in package install. Changing it means adding the new digests there.
	DefaultXrayVersion = "26.11.9"

	// DefaultXrayDownloadBase is the upstream release location. A mirror sets another base; the
	// digest of the archive does not change, because a mirror must serve the same bytes.
	DefaultXrayDownloadBase = "https://github.com/XTLS/Xray-core/releases/download"

	// DefaultDecoyTemplate is the decoy archetype used when the owner does not pick one.
	DefaultDecoyTemplate = "portal"

	// publicRoot holds what nginx serves: the decoy tree and the ACME challenge directory. It sits
	// outside /var/lib because nginx runs unprivileged and must be able to traverse into it, while
	// the state directory stays 0750.
	publicRoot = "/var/www/termit"

	// DefaultRenewBeforeDays renews a 90-day certificate around day 60.
	DefaultRenewBeforeDays = 30
)

var (
	versionPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	templatePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	emailLocalMaxLen = 320
)

// Config is the whole of config.yaml.
type Config struct {
	SchemaVersion int    `yaml:"schema_version"`
	Domain        string `yaml:"domain"`
	Email         string `yaml:"email,omitempty"`
	ACME          ACME   `yaml:"acme"`
	MN            MN     `yaml:"mn"`
	Xray          Xray   `yaml:"xray"`
	Decoy         Decoy  `yaml:"decoy"`
	Warp          Warp   `yaml:"warp"`
	Paths         Paths  `yaml:"paths"`
}

// MN is the management server this node belongs to. Empty means the node stands alone.
type MN struct {
	URL string `yaml:"url"`
	// CAFile lets a lab or a private installation trust its own CA without touching the host store.
	CAFile string `yaml:"ca_file,omitempty"`
}

// ACME describes the certificate authority and the renewal window.
type ACME struct {
	// Directory is the ACME directory URL.
	Directory string `yaml:"directory"`
	// SkipTLSVerify disables verification of the CA's own certificate. Lab only; the agent warns
	// while it is on.
	SkipTLSVerify bool `yaml:"skip_tls_verify,omitempty"`
	// RenewBeforeDays renews the certificate this many days before it expires.
	RenewBeforeDays int `yaml:"renew_before_days"`
}

// Xray pins the proxy core.
type Xray struct {
	Version string `yaml:"version"`
	// DownloadBase is a release directory: an HTTP(S) URL, a file:// URL, or an absolute local path
	// holding the archives. The layout below the base stays that of the release:
	// <base>/v<version>/<asset>.
	DownloadBase string `yaml:"download_base"`
	// SHA256 overrides the pinned archive digest per GOARCH, for a mirror that re-packed the
	// release or a self-built core. The pinned table is the default.
	SHA256 map[string]string `yaml:"sha256,omitempty"`
}

// Decoy describes the generated masking site.
type Decoy struct {
	// Template selects the archetype. The seed is generated at install and kept in the state file.
	Template string `yaml:"template"`
}

// Warp describes the Cloudflare WARP egress.
//
// WARP is on by default. An application on a client's device often asks the internet what address it
// is coming from; with direct egress the answer is the node's own address, which is exactly what a
// proxy should not reveal. WARP puts a Cloudflare address there instead. An owner who does not want
// that sets mode to off.
//
// The traffic path is always the same: Xray routes proxied traffic to warp-cli's loopback SOCKS5
// proxy. Nothing on the host is re-routed, so SSH, the decoy and the ACME challenge never touch WARP.
type Warp struct {
	// Mode is off, all or selected. Empty means all on a new configuration and, for a file written
	// before modes existed, whatever the old enabled switch said.
	Mode string `yaml:"mode"`
	// Selected lists the destinations that use WARP in selected mode. A domain here also covers its
	// subdomains; everything else exits directly.
	Selected []string `yaml:"selected,omitempty"`
	// FailOpen falls back to direct egress when WARP is unhealthy. The default is true: a node whose
	// WARP is down keeps carrying traffic, and uv status says that the exit is direct. An owner who
	// would rather lose connectivity than reveal the address sets it to false.
	FailOpen *bool `yaml:"fail_open,omitempty"`
	// Enabled is the switch this setting had before modes existed. It is read so an existing
	// configuration still loads, and never written again.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// WARP modes.
const (
	WarpOff      = "off"
	WarpAll      = "all"
	WarpSelected = "selected"
)

// FailOpenEnabled reports whether a failed WARP falls back to direct egress. A configuration that
// never set it fails open, which is the documented default.
func (w Warp) FailOpenEnabled() bool {
	return w.FailOpen == nil || *w.FailOpen
}

// UsesWarp reports whether proxied traffic is supposed to leave through WARP at all.
func (w Warp) UsesWarp() bool { return w.Mode == WarpAll || w.Mode == WarpSelected }

// Paths are the locations the agent writes to. Every field has a default derived from the standard
// layout; they are configurable for tests and for an owner who keeps state on another filesystem,
// not as a way to scatter files around the host.
type Paths struct {
	ConfigDir    string `yaml:"config_dir"`
	StateDir     string `yaml:"state_dir"`
	CertDir      string `yaml:"cert_dir"`
	ACMEWebroot  string `yaml:"acme_webroot"`
	SiteDir      string `yaml:"site_dir"`
	BinDir       string `yaml:"bin_dir"`
	XrayConfig   string `yaml:"xray_config"`
	NginxSite    string `yaml:"nginx_site"`
	SitesEnabled string `yaml:"sites_enabled"`
	UnitDir      string `yaml:"unit_dir"`
	ConfigFile   string `yaml:"config_file"`
	NginxDropIn  string `yaml:"nginx_drop_in"`
	DecoySnippet string `yaml:"decoy_snippet"`
	AccessLog    string `yaml:"access_log"`
	ErrorLog     string `yaml:"error_log"`
}

// Default returns a configuration with every default applied and no domain. It is what `uv doctor`
// uses on a host that has not been installed yet.
func Default() *Config {
	c := &Config{SchemaVersion: SchemaVersion}
	c.applyDefaults()
	return c
}

// Load reads, defaults and validates the configuration at filename.
func Load(filename string) (*Config, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- the operator chooses the config path.
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", filename, err)
	}
	c, err := Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", filename, err)
	}
	return c, nil
}

// Parse decodes a single YAML document, rejecting unknown keys, extra documents and unknown schema
// versions. It applies defaults and validates the result.
func Parse(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	c := &Config{}
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("decode: expected exactly one YAML document")
	}
	if c.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("schema_version %d is newer than this build supports (%d); upgrade termit-uv",
			c.SchemaVersion, SchemaVersion)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Save writes the configuration atomically. It holds no secret, so it is world-readable; the modes
// of the generated state are the ones that matter.
func (c *Config) Save(filename string) error {
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		return fmt.Errorf("config: create directory: %w", err)
	}
	return state.WriteFileAtomic(filename, data, 0o644)
}

// Marshal renders the configuration as the bytes stored on disk.
func (c *Config) Marshal() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# Termit node configuration, written by `uv init`.\n")
	b.WriteString("# The node's identity, certificate and decoy seed live in the state file, never here.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("config: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("config: encode: %w", err)
	}
	return b.Bytes(), nil
}

// Validate collects every problem instead of stopping at the first one, so one run tells the
// operator everything that needs fixing.
func (c *Config) Validate() error {
	var problems []error

	if err := validateDomain(c.Domain); err != nil {
		problems = append(problems, err)
	}
	if c.Email != "" && (strings.ContainsAny(c.Email, " \t") || !strings.Contains(c.Email, "@") || len(c.Email) > emailLocalMaxLen) {
		problems = append(problems, fmt.Errorf("email %q is not a usable address", c.Email))
	}
	if err := validateDirectory(c.ACME.Directory); err != nil {
		problems = append(problems, err)
	}
	if c.ACME.RenewBeforeDays < 1 || c.ACME.RenewBeforeDays > 89 {
		problems = append(problems, fmt.Errorf("acme.renew_before_days must be between 1 and 89, got %d",
			c.ACME.RenewBeforeDays))
	}
	if !versionPattern.MatchString(c.Xray.Version) {
		problems = append(problems, fmt.Errorf("xray.version %q must look like 26.11.9", c.Xray.Version))
	}
	if err := validateDownloadBase(c.Xray.DownloadBase); err != nil {
		problems = append(problems, err)
	}
	for arch, digest := range c.Xray.SHA256 {
		if !digestPattern.MatchString(digest) {
			problems = append(problems, fmt.Errorf("xray.sha256.%s must be 64 lowercase hex characters", arch))
		}
	}
	if !templatePattern.MatchString(c.Decoy.Template) {
		problems = append(problems, fmt.Errorf("decoy.template %q must be a short lower-case name", c.Decoy.Template))
	}
	switch c.Warp.Mode {
	case WarpOff, WarpAll:
	case WarpSelected:
		if len(c.Warp.Selected) == 0 {
			problems = append(problems, errors.New("warp.mode is selected but warp.selected is empty"))
		}
	default:
		problems = append(problems, fmt.Errorf("warp.mode %q must be off, all or selected", c.Warp.Mode))
	}
	for _, domain := range c.Warp.Selected {
		if err := validateDomain(domain); err != nil {
			problems = append(problems, fmt.Errorf("warp.selected: %w", err))
		}
	}
	problems = append(problems, c.Paths.validate()...)

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return nil
}

// CertRenewBefore is the renewal window as a duration.
func (c *Config) CertRenewBefore() time.Duration {
	return time.Duration(c.ACME.RenewBeforeDays) * 24 * time.Hour
}

// XrayArchiveDigest returns the pinned digest for arch, honouring the config override.
func (c *Config) XrayArchiveDigest(arch string) (string, bool) {
	if digest, ok := c.Xray.SHA256[arch]; ok {
		return digest, true
	}
	return "", false
}

func (c *Config) applyDefaults() {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	c.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Domain)), ".")
	c.Email = strings.TrimSpace(c.Email)
	if c.ACME.Directory == "" {
		c.ACME.Directory = acme.DefaultDirectory
	}
	if c.ACME.RenewBeforeDays == 0 {
		c.ACME.RenewBeforeDays = DefaultRenewBeforeDays
	}
	if c.Xray.Version == "" {
		c.Xray.Version = DefaultXrayVersion
	}
	if c.Xray.DownloadBase == "" {
		c.Xray.DownloadBase = DefaultXrayDownloadBase
	}
	if c.Decoy.Template == "" {
		c.Decoy.Template = DefaultDecoyTemplate
	}
	c.Warp.applyDefaults()
	c.Paths.applyDefaults()
}

// applyDefaults migrates the old enabled switch to a mode and normalises the selected list. It runs
// on every load, so a file written by an older build keeps the behaviour the owner chose.
func (w *Warp) applyDefaults() {
	if strings.TrimSpace(w.Mode) == "" {
		switch {
		case w.Enabled == nil:
			// A configuration that never mentioned WARP gets it: that is the point of the default.
			w.Mode = WarpAll
		case *w.Enabled:
			w.Mode = WarpAll
		default:
			w.Mode = WarpOff
		}
	}
	w.Mode = strings.ToLower(strings.TrimSpace(w.Mode))
	// The old key is read once and dropped: saving the configuration must not resurrect it beside
	// the mode it was migrated into.
	w.Enabled = nil
	if w.FailOpen == nil {
		open := true
		w.FailOpen = &open
	}
	if len(w.Selected) > 0 {
		seen := make(map[string]bool, len(w.Selected))
		kept := make([]string, 0, len(w.Selected))
		for _, domain := range w.Selected {
			domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
			if domain == "" || seen[domain] {
				continue
			}
			seen[domain] = true
			kept = append(kept, domain)
		}
		sort.Strings(kept)
		w.Selected = kept
	}
}

func (p *Paths) applyDefaults() {
	if p.ConfigDir == "" {
		p.ConfigDir = "/etc/termit/uv"
	}
	if p.StateDir == "" {
		p.StateDir = "/var/lib/termit/uv"
	}
	if p.CertDir == "" {
		p.CertDir = path.Join(p.StateDir, "certs")
	}
	if p.ACMEWebroot == "" {
		p.ACMEWebroot = path.Join(publicRoot, "acme")
	}
	if p.SiteDir == "" {
		p.SiteDir = path.Join(publicRoot, "site")
	}
	if p.BinDir == "" {
		p.BinDir = "/usr/lib/termit/bin"
	}
	if p.XrayConfig == "" {
		p.XrayConfig = path.Join(p.ConfigDir, "xray.json")
	}
	if p.NginxSite == "" {
		p.NginxSite = "/etc/nginx/sites-available/termit.conf"
	}
	if p.SitesEnabled == "" {
		p.SitesEnabled = "/etc/nginx/sites-enabled/termit.conf"
	}
	if p.UnitDir == "" {
		p.UnitDir = "/etc/systemd/system"
	}
	if p.ConfigFile == "" {
		p.ConfigFile = path.Join(p.ConfigDir, "config.yaml")
	}
	if p.NginxDropIn == "" {
		p.NginxDropIn = path.Join(p.UnitDir, "nginx.service.d", "termit.conf")
	}
	if p.DecoySnippet == "" {
		p.DecoySnippet = path.Join(p.StateDir, "nginx", "decoy.conf")
	}
	if p.AccessLog == "" {
		p.AccessLog = "/var/log/nginx/termit.access.log"
	}
	if p.ErrorLog == "" {
		p.ErrorLog = "/var/log/nginx/termit.error.log"
	}
}

// StatePath is where the agent keeps generated state.
func (p Paths) StatePath() string { return path.Join(p.StateDir, "state.json") }

// JournalPath is where the installer keeps the run journal.
func (p Paths) JournalPath() string { return path.Join(p.StateDir, "install-journal.json") }

// XrayBinary is the pinned core's installed location.
func (p Paths) XrayBinary() string { return path.Join(p.BinDir, "xray") }

func (p Paths) validate() []error {
	var problems []error
	required := map[string]string{
		"paths.config_dir":    p.ConfigDir,
		"paths.state_dir":     p.StateDir,
		"paths.cert_dir":      p.CertDir,
		"paths.acme_webroot":  p.ACMEWebroot,
		"paths.site_dir":      p.SiteDir,
		"paths.bin_dir":       p.BinDir,
		"paths.xray_config":   p.XrayConfig,
		"paths.nginx_site":    p.NginxSite,
		"paths.sites_enabled": p.SitesEnabled,
		"paths.unit_dir":      p.UnitDir,
		"paths.config_file":   p.ConfigFile,
		"paths.nginx_drop_in": p.NginxDropIn,
		"paths.decoy_snippet": p.DecoySnippet,
		"paths.access_log":    p.AccessLog,
		"paths.error_log":     p.ErrorLog,
	}
	for name, value := range required {
		if !filepath.IsAbs(value) && !strings.HasPrefix(value, "/") {
			problems = append(problems, fmt.Errorf("%s must be an absolute path, got %q", name, value))
		}
	}
	return problems
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
		return fmt.Errorf("domain %q must be fully qualified", domain)
	}
	for _, label := range strings.Split(domain, ".") {
		switch {
		case label == "":
			return fmt.Errorf("domain %q has an empty label", domain)
		case len(label) > 63:
			return fmt.Errorf("domain %q has a label longer than 63 characters", domain)
		case strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-"):
			return fmt.Errorf("domain %q has a label starting or ending with a hyphen", domain)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("domain %q contains %q, which is not allowed in a host name", domain, r)
			}
		}
	}
	return nil
}

func validateDirectory(directory string) error {
	u, err := url.Parse(directory)
	if err != nil {
		return fmt.Errorf("acme.directory %q is not a URL: %w", directory, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("acme.directory %q must be an http or https URL", directory)
	}
	if u.Host == "" {
		return fmt.Errorf("acme.directory %q has no host", directory)
	}
	return nil
}

func validateDownloadBase(base string) error {
	if filepath.IsAbs(base) || strings.HasPrefix(base, "/") {
		return nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("xray.download_base %q is not a URL or absolute path: %w", base, err)
	}
	switch u.Scheme {
	case "https", "http":
		if u.Host == "" {
			return fmt.Errorf("xray.download_base %q has no host", base)
		}
	case "file":
		if u.Path == "" {
			return fmt.Errorf("xray.download_base %q has no path", base)
		}
	default:
		return fmt.Errorf("xray.download_base %q must be an http(s) URL, a file URL or an absolute path", base)
	}
	return nil
}
