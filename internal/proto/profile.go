// Package proto holds what every component must agree on: the proxy profile and the client URI
// format. A change here is a protocol change — installed clients carry URIs built by an older build.
package proto

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	// Port is the public TLS port. nginx owns it; Xray never sees TLS.
	Port = 443

	// Network is the transport Xray speaks inside TLS.
	Network = "xhttp"

	// Security is the transport security a client must use.
	Security = "tls"

	// Mode is the XHTTP mode. packet-up is the one that survives an HTTP/1.1 hop through nginx
	// (SPIKES.md S3); the HTTP/2-shaped modes do not.
	Mode = "packet-up"
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// Profile is one node as a client needs to know it.
type Profile struct {
	// Domain is the node's name: the TLS SNI and the HTTP Host.
	Domain string
	// UUID is the VLESS credential.
	UUID string
	// Path is the secret path nginx forwards to Xray. It always has a leading and a trailing slash.
	Path string
	// Label is the client-side display name. Optional.
	Label string
}

// Validate reports whether the profile can be turned into a working client.
func (p Profile) Validate() error {
	var problems []error
	switch {
	case p.Domain == "":
		problems = append(problems, errors.New("domain is required"))
	case !hostPattern.MatchString(strings.ToLower(p.Domain)):
		problems = append(problems, fmt.Errorf("domain %q is not a host name", p.Domain))
	}
	if !uuidPattern.MatchString(strings.ToLower(p.UUID)) {
		problems = append(problems, fmt.Errorf("uuid %q is not a UUID", p.UUID))
	}
	switch {
	case p.Path == "":
		problems = append(problems, errors.New("path is required"))
	case !strings.HasPrefix(p.Path, "/") || !strings.HasSuffix(p.Path, "/"):
		problems = append(problems, fmt.Errorf("path %q must start and end with /", p.Path))
	case strings.ContainsAny(p.Path, " \t?#"):
		problems = append(problems, fmt.Errorf("path %q contains a character a URL cannot carry", p.Path))
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid profile: %w", errors.Join(problems...))
	}
	return nil
}

// BuildVLESSUrl renders the client URI. The result never contains allowInsecure: Xray 26 removed it
// and the profile verifies the certificate properly (SPIKES.md S4).
func BuildVLESSUrl(p Profile) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("encryption", "none")
	query.Set("security", Security)
	query.Set("sni", p.Domain)
	query.Set("type", Network)
	query.Set("mode", Mode)
	query.Set("path", p.Path)

	label := p.Label
	if label == "" {
		label = Network
	}
	link := url.URL{
		Scheme:   "vless",
		User:     url.User(strings.ToLower(p.UUID)),
		Host:     net.JoinHostPort(strings.ToLower(p.Domain), strconv.Itoa(Port)),
		RawQuery: query.Encode(),
		Fragment: label,
	}
	return link.String(), nil
}

// ParseVLESSUrl reads a URI built by BuildVLESSUrl, or one written by hand. It refuses anything the
// profile does not support, including the removed allowInsecure switch.
func ParseVLESSUrl(raw string) (Profile, error) {
	link, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Profile{}, fmt.Errorf("vless url: %w", err)
	}
	if link.Scheme != "vless" {
		return Profile{}, fmt.Errorf("vless url: scheme %q is not vless", link.Scheme)
	}
	if link.User == nil {
		return Profile{}, errors.New("vless url: no user id")
	}
	port := link.Port()
	if port == "" {
		return Profile{}, errors.New("vless url: no port")
	}
	if port != strconv.Itoa(Port) {
		return Profile{}, fmt.Errorf("vless url: port %s is not the profile port %d", port, Port)
	}

	query := link.Query()
	if query.Has("allowInsecure") || query.Has("insecure") {
		return Profile{}, errors.New("vless url: allowInsecure is not part of the profile")
	}
	if got := query.Get("security"); got != Security {
		return Profile{}, fmt.Errorf("vless url: security %q is not %q", got, Security)
	}
	if got := query.Get("type"); got != Network {
		return Profile{}, fmt.Errorf("vless url: type %q is not %q", got, Network)
	}
	if encryption := query.Get("encryption"); encryption != "" && encryption != "none" {
		return Profile{}, fmt.Errorf("vless url: encryption %q is not supported", encryption)
	}
	if mode := query.Get("mode"); mode != "" && mode != Mode {
		return Profile{}, fmt.Errorf("vless url: mode %q is not %q", mode, Mode)
	}

	profile := Profile{
		Domain: link.Hostname(),
		UUID:   link.User.Username(),
		Path:   query.Get("path"),
		Label:  link.Fragment,
	}
	if profile.Path == "" {
		profile.Path = "/"
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}
