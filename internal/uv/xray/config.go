// Package xray owns the proxy core's configuration: a typed model and a deterministic generator.
//
// Xray is generated, never hand-edited, and never sees TLS: nginx terminates TLS on :443 and forwards
// one secret path to 127.0.0.1:8443 in plaintext (SPIKES.md S5). The API inbound is loopback-only and
// exists so a client can be added later without rewriting the file by hand.
package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	// DefaultListen is the loopback address Xray binds. It must never be public: nginx fronts it.
	DefaultListen = "127.0.0.1"
	// DefaultPort is the loopback HTTP port nginx proxies to.
	DefaultPort = 8443
	// DefaultAPIPort is the loopback port of the management API.
	DefaultAPIPort = 10085
	// InboundTag names the VLESS inbound, and the tag used by `xray api` calls.
	InboundTag = "proxy"
	// APITag names the API's own inbound and its virtual outbound.
	APITag = "api"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Client is one VLESS user. Email is the identity the API and the logs use; the UUID is the secret.
type Client struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// Spec is everything the generator is allowed to look at.
type Spec struct {
	// Path is the secret path nginx forwards. It must match the nginx location exactly.
	Path string
	// Clients are the users of this inbound; at least one, never empty.
	Clients []Client
	// Listen, Port and APIPort default to the constants above.
	Listen  string
	Port    int
	APIPort int
	// LogLevel is an Xray log level: debug, info, warning, error.
	LogLevel string
}

// DefaultSpec returns a spec with the ports and log level filled in.
func DefaultSpec() Spec {
	return Spec{Listen: DefaultListen, Port: DefaultPort, APIPort: DefaultAPIPort, LogLevel: "warning"}
}

// Validate reports every reason the spec could not produce a working node.
func (s Spec) Validate() error {
	var problems []error
	switch {
	case s.Path == "":
		problems = append(problems, errors.New("path is required"))
	case !strings.HasPrefix(s.Path, "/") || !strings.HasSuffix(s.Path, "/"):
		problems = append(problems, fmt.Errorf("path %q must start and end with /", s.Path))
	case strings.ContainsAny(s.Path, " \t\"'\\"):
		problems = append(problems, fmt.Errorf("path %q contains a character a path cannot carry", s.Path))
	}
	if s.Listen == "" {
		problems = append(problems, errors.New("listen address is required"))
	}
	for name, port := range map[string]int{"port": s.Port, "api port": s.APIPort} {
		if port < 1 || port > 65535 {
			problems = append(problems, fmt.Errorf("%s %d is out of range", name, port))
		}
	}
	switch s.LogLevel {
	case "debug", "info", "warning", "error":
	default:
		problems = append(problems, fmt.Errorf("log level %q is not one of debug, info, warning, error", s.LogLevel))
	}
	if len(s.Clients) == 0 {
		problems = append(problems, errors.New("at least one client is required"))
	}
	seen := map[string]bool{}
	for _, client := range s.Clients {
		if !uuidPattern.MatchString(strings.ToLower(client.ID)) {
			problems = append(problems, fmt.Errorf("client %q has a uuid that is not a UUID", client.Email))
		}
		if client.Email == "" {
			problems = append(problems, fmt.Errorf("client %s has no email", client.ID))
		} else if seen[client.Email] {
			problems = append(problems, fmt.Errorf("client email %q is used twice", client.Email))
		}
		seen[client.Email] = true
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid xray spec: %w", errors.Join(problems...))
	}
	return nil
}

// Generate renders the config file. The output is deterministic: same spec, same bytes, no
// timestamps and no map iteration order.
func Generate(spec Spec) ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	clients := make([]Client, 0, len(spec.Clients))
	for _, client := range spec.Clients {
		clients = append(clients, Client{ID: strings.ToLower(client.ID), Email: client.Email})
	}
	// The caller's order must not reach the file. Two code paths generate this config — the installer
	// and the daily reconcile — and a different order in the same set would look like drift and cost
	// a restart every day.
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].ID != clients[j].ID {
			return clients[i].ID < clients[j].ID
		}
		return clients[i].Email < clients[j].Email
	})

	config := config{
		Log: logConfig{LogLevel: spec.LogLevel},
		API: apiConfig{Tag: APITag, Services: []string{"HandlerService", "LoggerService"}},
		Inbounds: []inbound{
			{
				Tag:      APITag,
				Listen:   DefaultListen,
				Port:     spec.APIPort,
				Protocol: "dokodemo-door",
				Settings: dokodemoSettings{Address: DefaultListen},
			},
			{
				Tag:      InboundTag,
				Listen:   spec.Listen,
				Port:     spec.Port,
				Protocol: "vless",
				Settings: vlessSettings{Clients: clients, Decryption: "none"},
				Stream: &stream{
					Network:  "xhttp",
					Security: "none",
					XHTTP:    &xhttpSettings{Path: spec.Path, Mode: "packet-up"},
				},
				Sniffing: &sniffing{Enabled: true, DestOverride: []string{"http", "tls", "quic"}},
			},
		},
		Outbounds: []outbound{
			{
				Tag:      "direct",
				Protocol: "freedom",
				// The core's default safety policy blocks private and reserved destinations for a VLESS
				// inbound. The one exception is the node's own web tier on loopback: the canary proves
				// a client set carries traffic by fetching the decoy through the tunnel, and the decoy
				// on :80 is public anyway, so this exposes nothing that a stranger cannot already reach.
				Settings: freedomSettings{FinalRules: []finalRule{{
					Action: "allow", Network: "tcp", IP: []string{"127.0.0.0/8"}, Port: "80",
				}}},
			},
			{Tag: "block", Protocol: "blackhole"},
		},
		Routing: routing{
			DomainStrategy: "AsIs",
			Rules: []rule{
				{Type: "field", InboundTag: []string{APITag}, OutboundTag: APITag},
			},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("xray: encode config: %w", err)
	}
	return append(data, '\n'), nil
}

// APIAddress is the loopback address of the management API for this spec.
func (s Spec) APIAddress() string { return fmt.Sprintf("%s:%d", DefaultListen, s.APIPort) }

// ProxyAddress is the loopback address nginx proxies to.
func (s Spec) ProxyAddress() string { return fmt.Sprintf("%s:%d", s.Listen, s.Port) }

// --- the JSON document -------------------------------------------------------

type config struct {
	Log       logConfig  `json:"log"`
	API       apiConfig  `json:"api"`
	Inbounds  []inbound  `json:"inbounds"`
	Outbounds []outbound `json:"outbounds"`
	Routing   routing    `json:"routing"`
}

type logConfig struct {
	LogLevel string `json:"loglevel"`
}

type apiConfig struct {
	Tag      string   `json:"tag"`
	Services []string `json:"services"`
}

type inbound struct {
	Tag      string    `json:"tag"`
	Listen   string    `json:"listen"`
	Port     int       `json:"port"`
	Protocol string    `json:"protocol"`
	Settings any       `json:"settings"`
	Stream   *stream   `json:"streamSettings,omitempty"`
	Sniffing *sniffing `json:"sniffing,omitempty"`
}

type vlessSettings struct {
	Clients    []Client `json:"clients"`
	Decryption string   `json:"decryption"`
}

type dokodemoSettings struct {
	Address string `json:"address"`
}

type stream struct {
	Network  string         `json:"network"`
	Security string         `json:"security"`
	XHTTP    *xhttpSettings `json:"xhttpSettings,omitempty"`
}

type xhttpSettings struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

type sniffing struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride"`
}

type outbound struct {
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
	Settings any    `json:"settings,omitempty"`
}

// freedomSettings carries the only freedom option we set: an allow rule for the canary's loopback
// target. Everything else keeps the core's default safety policy.
type freedomSettings struct {
	FinalRules []finalRule `json:"finalRules,omitempty"`
}

type finalRule struct {
	Action  string   `json:"action"`
	Network string   `json:"network,omitempty"`
	IP      []string `json:"ip,omitempty"`
	Port    string   `json:"port,omitempty"`
}

type routing struct {
	DomainStrategy string `json:"domainStrategy"`
	Rules          []rule `json:"rules"`
}

type rule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag"`
	OutboundTag string   `json:"outboundTag"`
}
