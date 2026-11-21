// Package hysteria generates the configuration of the QUIC transport: a second process beside the core,
// pinned by version and checksum, whose traffic enters the core through a loopback SOCKS inbound so that
// WARP, the blocklist and the direct rules stay the core's decisions.
//
// Two choices are deliberate and show up in every generated file. There is no Brutal congestion control:
// `congestion.type: bbr` with `ignoreClientBandwidth` is a transport that queues like everyone else
// instead of fighting the network for bandwidth, which is exactly what gets noticed. And there is no
// obfuscation and no port hopping: both are fingerprints of their own, and neither is needed here.
package hysteria

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultListen is the UDP address the transport serves on. 443 is where a browser's QUIC would be, and
// the vhost advertises it with Alt-Svc.
const DefaultListen = ":443"

// Spec is everything the generator is allowed to look at.
type Spec struct {
	// CertFile and KeyFile are the node's own leaf. Hysteria re-reads them on every handshake, so a
	// renewal needs no restart.
	CertFile string
	KeyFile  string
	// Listen is the UDP address; empty means DefaultListen.
	Listen string
	// Users maps a client name to the password that client presents. One entry per peer, and at least
	// one: an empty map would accept nobody and is a configuration mistake, not a transport.
	Users map[string]string
	// HopAddress is the core's loopback SOCKS inbound, host:port. Every request goes there; the core
	// decides where it leaves.
	HopAddress string
	// MasqueradeURL is the node's own vhost, so a visitor that is not speaking hysteria sees the site the
	// node already serves, headers and 404s included.
	MasqueradeURL string
}

// Generate renders the configuration `hysteria server -c` reads. The output is deterministic: users are
// written in sorted order, so the same spec is the same bytes.
func Generate(spec Spec) ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	config := file{
		Listen: spec.ListenOrDefault(),
		TLS:    tlsBlock{Cert: spec.CertFile, Key: spec.KeyFile},
		Auth: authBlock{
			Type:     "userpass",
			UserPass: spec.Users,
		},
		Congestion:            congestionBlock{Type: "bbr"},
		IgnoreClientBandwidth: true,
		Masquerade: masqueradeBlock{
			Type: "proxy",
			Proxy: proxyBlock{
				URL:         spec.MasqueradeURL,
				RewriteHost: true,
				Insecure:    true,
			},
		},
		Outbounds: []outboundBlock{{
			Name: "core",
			Type: "socks5",
			Socks5: socks5Block{
				Addr: spec.HopAddress,
			},
		}},
		ACL: aclBlock{Rules: []aclRule{{Outbound: "core"}}},
	}
	data, err := marshal(config)
	if err != nil {
		return nil, fmt.Errorf("hysteria: encode config: %w", err)
	}
	return data, nil
}

// marshal renders the document with the two-space indentation every other file this project writes uses.
func marshal(config file) ([]byte, error) {
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Decode reads a generated document back with unknown fields refused, and checks the three things the
// transport cannot run without: an address, at least one user, and one outbound the catch-all ACL names.
// The binary has no `-test`, so reading our own file strictly is the validation we can do before a
// trial start.
func Decode(data []byte) error {
	var parsed file
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	switch {
	case parsed.Listen == "":
		return errors.New("the document has no listen address")
	case parsed.Auth.Type != "userpass" || len(parsed.Auth.UserPass) == 0:
		return errors.New("the document does not authenticate anybody")
	case len(parsed.Outbounds) == 0:
		return errors.New("the document has no outbound")
	case len(parsed.ACL.Rules) == 0 || parsed.ACL.Rules[0].Outbound != parsed.Outbounds[0].Name:
		return errors.New("the document's ACL does not name its first outbound")
	}
	return nil
}

// Validate reports every reason the spec could not produce a working transport.
func (s Spec) Validate() error {
	var problems []error
	for name, path := range map[string]string{"certificate": s.CertFile, "key": s.KeyFile} {
		switch {
		case path == "":
			problems = append(problems, fmt.Errorf("%s is required", name))
		case !filepath.IsAbs(path) && !strings.HasPrefix(path, "/"):
			problems = append(problems, fmt.Errorf("%s %q is not an absolute path", name, path))
		}
	}
	if s.ListenOrDefault() != DefaultListen {
		if _, _, err := net.SplitHostPort(s.ListenOrDefault()); err != nil {
			problems = append(problems, fmt.Errorf("listen address %q is not host:port", s.Listen))
		}
	}
	if len(s.Users) == 0 {
		problems = append(problems, errors.New("at least one user is required"))
	}
	for name, password := range s.Users {
		switch {
		case name == "":
			problems = append(problems, errors.New("a user has an empty name"))
		case name != strings.ToLower(name):
			// The server lowercases user names before comparing them, so an upper-case name would be
			// accepted once and then never match again: the operator would see a client that works and
			// then stops.
			problems = append(problems, fmt.Errorf("user %q must be lower case, the server lowercases it", name))
		case strings.Contains(name, ":"):
			// The auth string the client sends is "user:password", split on the first colon.
			problems = append(problems, fmt.Errorf("user %q must not contain a colon", name))
		case len(password) < minPasswordLength:
			problems = append(problems, fmt.Errorf("user %q has a password shorter than %d characters", name, minPasswordLength))
		}
	}
	if _, _, err := net.SplitHostPort(s.HopAddress); err != nil {
		problems = append(problems, fmt.Errorf("hop address %q is not host:port", s.HopAddress))
	}
	parsed, err := url.Parse(s.MasqueradeURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		problems = append(problems, fmt.Errorf("masquerade URL %q is not an https URL", s.MasqueradeURL))
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid hysteria spec: %w", errors.Join(problems...))
	}
	return nil
}

// ListenOrDefault is the address the transport serves on.
func (s Spec) ListenOrDefault() string {
	if s.Listen == "" {
		return DefaultListen
	}
	return s.Listen
}

// minPasswordLength keeps a typo from producing a one-character password. The real passwords are 256-bit
// derivations; this is a floor, not the rule.
const minPasswordLength = 8

// file is the document `hysteria server` reads. Keys are the ones the server's own mapstructure tags use.
type file struct {
	Listen string    `yaml:"listen"`
	TLS    tlsBlock  `yaml:"tls"`
	Auth   authBlock `yaml:"auth"`
	// Congestion and IgnoreClientBandwidth are what keep this transport off Brutal: the server adapts
	// like any BBR sender instead of shaping to a bandwidth it was told to assume.
	Congestion            congestionBlock `yaml:"congestion"`
	IgnoreClientBandwidth bool            `yaml:"ignoreClientBandwidth"`
	Masquerade            masqueradeBlock `yaml:"masquerade"`
	Outbounds             []outboundBlock `yaml:"outbounds"`
	ACL                   aclBlock        `yaml:"acl"`
}

type tlsBlock struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type authBlock struct {
	Type     string            `yaml:"type"`
	UserPass map[string]string `yaml:"userpass"`
}

type congestionBlock struct {
	Type string `yaml:"type"`
}

type masqueradeBlock struct {
	Type  string     `yaml:"type"`
	Proxy proxyBlock `yaml:"proxy"`
}

type proxyBlock struct {
	URL         string `yaml:"url"`
	RewriteHost bool   `yaml:"rewriteHost"`
	Insecure    bool   `yaml:"insecure"`
}

type outboundBlock struct {
	Name   string      `yaml:"name"`
	Type   string      `yaml:"type"`
	Socks5 socks5Block `yaml:"socks5"`
}

type socks5Block struct {
	Addr string `yaml:"addr"`
}

type aclBlock struct {
	Rules []aclRule `yaml:"rules"`
}

type aclRule struct {
	Outbound string `yaml:"outbound"`
}
