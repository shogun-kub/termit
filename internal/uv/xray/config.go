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
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shogun-kub/termit/internal/proto"
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
	// HysteriaInboundTag names the loopback SOCKS inbound the QUIC transport's traffic enters through.
	HysteriaInboundTag = "hysteria-in"
	// DefaultHysteriaSocksPort is where that inbound listens on loopback.
	DefaultHysteriaSocksPort = 10801
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
	// Warp routes proxied traffic through the WARP sidecar. The zero value keeps direct egress.
	Warp WarpSpec
	// BlockBitTorrent sniffs the first bytes of every connection and blackholes the ones the core
	// recognises as BitTorrent. It is off by default: sniffing costs a little CPU on every connection.
	BlockBitTorrent bool
	// Hysteria is the QUIC transport's loopback hop. When it is on, the document carries a SOCKS inbound
	// on loopback and the transport dials through it, so its traffic takes the routing decisions below
	// instead of leaving the node on its own.
	Hysteria HysteriaHop
}

// HysteriaHop is the loopback inbound the QUIC transport's traffic enters the core through. It is a
// local socket: the transport is the only client, and it runs on this host.
type HysteriaHop struct {
	Enabled bool
	// Port is the loopback port. Zero means DefaultHysteriaSocksPort.
	Port int
}

// PortOrDefault is the loopback port the hop listens on.
func (h HysteriaHop) PortOrDefault() int {
	if h.Port == 0 {
		return DefaultHysteriaSocksPort
	}
	return h.Port
}

// WarpSpec is the WARP egress the generator must wire in. It is a plain description of the intent;
// whether the sidecar is actually up is the runtime's business, and the fail-open balancer is what
// keeps a dead sidecar from taking the node's traffic with it.
type WarpSpec struct {
	// Mode is "all" or "selected"; anything else means WARP is off.
	Mode string
	// Selected lists the destinations that use WARP in selected mode.
	Selected []string
	// FailOpen falls back to direct egress when WARP does not answer its probe.
	FailOpen bool
	// Socks is the sidecar's loopback address; empty means DefaultWarpSocks.
	Socks string
	// Transport says how proxied traffic reaches WARP. Empty means WarpTransportProxy, which is what a
	// configuration written before the key existed expects. WarpTransportWireGuard puts the tunnel inside
	// the core: no sidecar, no SOCKS5 hop, and UDP goes through WARP like TCP.
	Transport string
	// WireGuard is the identity the tunnel needs. It is ignored on the proxy transport and required on
	// the wireguard one.
	WireGuard *WireGuardIdentity
	// Family is the address family the tunnel carries: "ipv4" or "ipv6". Empty means ipv4. Destinations
	// of the other family are routed direct: Cloudflare's IPv6 egress is the one large services block,
	// and which family a node uses is the owner's decision, not the generator's.
	Family string
}

// WireGuardIdentity is everything a WireGuard outbound needs: the node's own key, the address Cloudflare
// assigned inside the tunnel, and the peer to reach. The three reserved bytes are part of the identity,
// not a constant: Cloudflare derives them from the device registration and rejects a handshake that
// carries the wrong ones.
type WireGuardIdentity struct {
	SecretKey string
	Addresses []string
	PublicKey string
	Endpoint  string
	Reserved  []int
	MTU       int
}

// Enabled reports whether traffic is supposed to leave through WARP.
func (w WarpSpec) Enabled() bool { return w.Mode == "all" || w.Mode == "selected" }

// UsesWireGuard reports whether the tunnel lives inside the core rather than in the warp-cli sidecar.
func (w WarpSpec) UsesWireGuard() bool { return w.Transport == WarpTransportWireGuard }

// UsesUsque reports whether the sidecar is the MASQUE client, whose SOCKS5 listener carries UDP.
func (w WarpSpec) UsesUsque() bool { return w.Transport == WarpTransportUsque }

// CarriesUDP reports whether the egress can carry a UDP flow. The warp-cli sidecar cannot, and the
// rules below split UDP to direct for it; everything else takes both protocols the same way.
func (w WarpSpec) CarriesUDP() bool { return w.UsesWireGuard() || w.UsesUsque() }

// FamilyOrDefault is the address family the tunnel carries; the default is IPv4.
func (w WarpSpec) FamilyOrDefault() string {
	if w.Family == "" {
		return WarpFamilyIPv4
	}
	return w.Family
}

// familyRoute is the default route of the family the tunnel carries.
func familyRoute(family string) string {
	if family == WarpFamilyIPv6 {
		return "::/0"
	}
	return "0.0.0.0/0"
}

// otherFamilyRoute is the default route of the family it does not.
func otherFamilyRoute(family string) string {
	if family == WarpFamilyIPv6 {
		return "0.0.0.0/0"
	}
	return "::/0"
}

// familyAddresses keeps the tunnel addresses that belong to the family. Cloudflare hands out one of
// each; the core needs only the chosen one, because the peer carries only that family's default route.
func familyAddresses(addresses []string, family string) []string {
	out := make([]string, 0, 1)
	for _, address := range addresses {
		isV6 := strings.Contains(address, ":")
		if (family == WarpFamilyIPv6) == isV6 {
			out = append(out, address)
		}
	}
	return out
}

// Constants for the WARP sidecar. They are exported because the agent, the installer and the
// `termit-uv warp` command all have to agree on the same address and probe.
const (
	// DefaultWarpSocks is warp-cli's proxy-mode SOCKS5 listener on loopback.
	DefaultWarpSocks = "127.0.0.1:40000"
	// DefaultUsqueSocks is the address the core hands proxied traffic to on the MASQUE transport. It is
	// the relay's listener, not the sidecar's: usque keys a UDP association on the TCP connection that
	// asked for it, so a relay sits in front and sends the flow from that connection's own port.
	DefaultUsqueSocks = "127.0.0.1:40001"
	// DefaultUsqueSidecar is where the MASQUE sidecar itself listens, behind the relay.
	DefaultUsqueSidecar = "127.0.0.1:40002"
	// WarpTransportProxy is the sidecar path: the core hands proxied traffic to warp-cli's SOCKS5
	// listener. It carries TCP only, which is why UDP is routed direct on this transport.
	WarpTransportProxy = "proxy"
	// WarpTransportUsque is the MASQUE sidecar, whose SOCKS5 listener carries UDP behind the relay.
	WarpTransportUsque = "usque"
	// WarpTransportWireGuard keeps the tunnel inside the core. UDP and TCP take the same path, and no
	// package, daemon or loopback port is involved.
	WarpTransportWireGuard = "wireguard"
	// DefaultWarpMTU is what the WireGuard tunnel uses unless configured. 1280 is the IPv6 minimum and
	// leaves room for the WireGuard header and the outer UDP/IP headers inside a 1500-byte path.
	DefaultWarpMTU = 1280
	// WarpFamilyIPv4 and WarpFamilyIPv6 are the two address families the tunnel can carry. The default is
	// IPv4: Cloudflare's IPv6 egress is what large services tend to mark as unsafe.
	WarpFamilyIPv4 = "ipv4"
	WarpFamilyIPv6 = "ipv6"
	// WarpTag names the WARP outbound; the balancer selects it by tag.
	WarpTag = "warp"
	// WarpBalancerTag names the fail-open balancer.
	WarpBalancerTag = "warp-egress"
	// WarpProbeURL is what the observatory fetches through WARP. It is a plain, cheap endpoint that
	// answers only when the tunnel actually works, and its trace names the address Cloudflare saw. An
	// address literal, not a name: the tunnel carries one family, and a dual-stack name resolves to
	// whichever family the resolver prefers — IPv6 almost everywhere.
	WarpProbeURL = "https://1.1.1.1/cdn-cgi/trace"
	// warpProbeURLv6 is the same trace over IPv6, for a tunnel that carries only that family.
	warpProbeURLv6 = "https://[2606:4700:4700::1111]/cdn-cgi/trace"
	// WarpProbeInterval is how often the observatory checks the sidecar. It is short on purpose: the
	// interval is also the worst-case window during which a tunnel that died mid-life still takes new
	// connections, and a half-minute of retries is a far better failure mode than five minutes of them.
	WarpProbeInterval = "30s"
)

// WarpProbeURLFor is the probe URL in the family the tunnel carries. A name would be resolved to the
// other family on most hosts, the probe would fail, and the balancer would read that as "WARP is down"
// and send every proxied flow direct.
func WarpProbeURLFor(family string) string {
	if family == WarpFamilyIPv6 {
		return warpProbeURLv6
	}
	return WarpProbeURL
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
	if s.Hysteria.Enabled {
		hop := s.Hysteria.PortOrDefault()
		switch {
		case hop < 1 || hop > 65535:
			problems = append(problems, fmt.Errorf("hysteria hop port %d is out of range", hop))
		case hop == s.Port, hop == s.APIPort:
			problems = append(problems, fmt.Errorf("hysteria hop port %d collides with another inbound", hop))
		}
	}
	switch s.LogLevel {
	case "debug", "info", "warning", "error":
	default:
		problems = append(problems, fmt.Errorf("log level %q is not one of debug, info, warning, error", s.LogLevel))
	}
	switch s.Warp.Mode {
	case "", "off", "all":
	case "selected":
		if len(s.Warp.Selected) == 0 {
			problems = append(problems, errors.New("selected WARP mode needs at least one domain"))
		}
		for _, domain := range s.Warp.Selected {
			if domain == "" || strings.ContainsAny(domain, " \t/") {
				problems = append(problems, fmt.Errorf("warp domain %q is not a domain", domain))
			}
		}
	default:
		problems = append(problems, fmt.Errorf("warp mode %q must be off, all or selected", s.Warp.Mode))
	}
	switch s.Warp.Transport {
	case "", WarpTransportProxy:
		if s.Warp.Enabled() {
			socks := s.Warp.Socks
			if socks == "" {
				socks = DefaultWarpSocks
			}
			if _, _, err := net.SplitHostPort(socks); err != nil {
				problems = append(problems, fmt.Errorf("warp socks address %q is not host:port", socks))
			}
		}
	case WarpTransportWireGuard:
		switch s.Warp.Family {
		case "", WarpFamilyIPv4, WarpFamilyIPv6:
		default:
			problems = append(problems, fmt.Errorf("warp family %q must be ipv4 or ipv6", s.Warp.Family))
		}
		if !s.Warp.Enabled() {
			// A wireguard transport with WARP off is not a contradiction worth refusing: the identity
			// simply goes unused, and an operator who turns WARP back on does not have to re-register.
			break
		}
		identity := s.Warp.WireGuard
		switch {
		case identity == nil:
			problems = append(problems, errors.New("warp transport wireguard needs an identity"))
		case identity.SecretKey == "":
			problems = append(problems, errors.New("the wireguard identity has no secret key"))
		case identity.PublicKey == "":
			problems = append(problems, errors.New("the wireguard identity has no peer public key"))
		case identity.Endpoint == "":
			problems = append(problems, errors.New("the wireguard identity has no endpoint"))
		case len(identity.Addresses) == 0:
			problems = append(problems, errors.New("the wireguard identity has no tunnel address"))
		}
		if identity != nil {
			// Cloudflare's header is three bytes or absent; any other length is rejected by the core.
			if len(identity.Reserved) != 0 && len(identity.Reserved) != 3 {
				problems = append(problems, fmt.Errorf("the wireguard reserved bytes are %d long, want 3", len(identity.Reserved)))
			}
			if identity.MTU != 0 && (identity.MTU < 576 || identity.MTU > 1500) {
				problems = append(problems, fmt.Errorf("the wireguard mtu %d is out of range", identity.MTU))
			}
			if len(identity.Addresses) > 0 && len(familyAddresses(identity.Addresses, s.Warp.FamilyOrDefault())) == 0 {
				problems = append(problems, fmt.Errorf("the wireguard identity has no %s tunnel address", s.Warp.FamilyOrDefault()))
			}
		}
	case WarpTransportUsque:
		// The sidecar is a process, not a document: the spec only has to agree on where it listens.
	default:
		problems = append(problems, fmt.Errorf("warp transport %q must be proxy, wireguard or usque", s.Warp.Transport))
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
					// The transport and its mode are the client's profile, so both come from proto. The
					// core's security is "none" because nginx terminates TLS: Xray only ever sees loopback
					// plaintext.
					Network:  proto.Network,
					Security: "none",
					XHTTP:    &xhttpSettings{Path: spec.Path, Mode: proto.Mode},
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

	if spec.BlockBitTorrent {
		// The sniffer classifies BitTorrent without a protocol list, and routeOnly keeps the client's
		// destination: with it off, a sniffed HTTP Host replaces the address the client asked for and
		// the tunnel dials a name this node resolves instead.
		config.Inbounds[1].Sniffing = &sniffing{Enabled: true, DestOverride: []string{}, RouteOnly: true}
		config.Routing.Rules = append([]rule{
			{Type: "field", Protocol: []string{"bittorrent"}, OutboundTag: "block"},
		}, config.Routing.Rules...)
	}

	if spec.Warp.Enabled() {
		config.Outbounds = append(config.Outbounds, warpOutbound(spec.Warp))
		// The canary fetches the node's own decoy through the tunnel, and any owner-side tool may
		// address loopback. Cloudflare cannot reach either, so loopback stays direct in every mode.
		config.Routing.Rules = append(config.Routing.Rules, rule{
			Type: "field", IP: []string{"127.0.0.0/8"}, OutboundTag: "direct",
		})
		if spec.Warp.FailOpen {
			config.Observatory = &observatory{
				SubjectSelector:   []string{WarpTag},
				ProbeURL:          WarpProbeURLFor(spec.Warp.Family),
				ProbeInterval:     WarpProbeInterval,
				EnableConcurrency: true,
			}
			config.Routing.Balancers = []balancer{{
				Tag: WarpBalancerTag, Selector: []string{WarpTag},
				Strategy: &strategy{Type: "leastPing"}, FallbackTag: "direct",
			}}
			config.Routing.Rules = append(config.Routing.Rules, warpRules(spec.Warp, WarpBalancerTag)...)
		} else {
			config.Routing.Rules = append(config.Routing.Rules, warpRules(spec.Warp, "")...)
		}
	}

	if spec.Hysteria.Enabled {
		hop := inbound{
			Tag:      HysteriaInboundTag,
			Listen:   DefaultListen,
			Port:     spec.Hysteria.PortOrDefault(),
			Protocol: "socks",
			Settings: socksInboundSettings{Auth: "noauth", UDP: true},
		}
		if spec.BlockBitTorrent {
			// The transport's traffic is the one path the torrent rule would otherwise never see: that
			// rule matches a sniffed protocol, and sniffing is a per-inbound setting.
			hop.Sniffing = &sniffing{Enabled: true, DestOverride: []string{}, RouteOnly: true}
		}
		config.Inbounds = append(config.Inbounds, hop)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("xray: encode config: %w", err)
	}
	return append(data, '\n'), nil
}

// warpOutbound is the egress itself: a WireGuard tunnel inside the core, or the SOCKS5 outbound to
// warp-cli's proxy-mode listener. Domains are forwarded to the SOCKS sidecar, not resolved here: the
// request carries the name and the WARP client resolves it inside the tunnel, so a lookup does not leak
// the node's address to the node's resolver.
func warpOutbound(w WarpSpec) outbound {
	if w.UsesWireGuard() {
		return wireGuardOutbound(w.WireGuard, w.FamilyOrDefault())
	}
	host, port := warpSocks(w)
	return outbound{
		Tag: WarpTag, Protocol: "socks",
		Settings: socksSettings{Servers: []socksServer{{Address: host, Port: port}}},
	}
}

// wireGuardOutbound is the userspace WireGuard transport. noKernelTun is not tuning: it keeps the core
// from creating a TUN device and touching host routing, and it is what makes the outbound work in an
// unprivileged container. The measured cost is a third of the kernel path's throughput on a one-vCPU
// node, which is recorded in IMPLEMENTATION_PLAN.md §10.1.
//
// The tunnel carries one address family. Cloudflare supplies an address in each; the document keeps the
// chosen one and gives the peer that family's default route, so the other family cannot silently ride
// the same tunnel.
func wireGuardOutbound(identity *WireGuardIdentity, family string) outbound {
	mtu := identity.MTU
	if mtu == 0 {
		mtu = DefaultWarpMTU
	}
	peer := wireGuardPeer{
		PublicKey:  identity.PublicKey,
		Endpoint:   identity.Endpoint,
		AllowedIPs: []string{familyRoute(family)},
		Reserved:   identity.Reserved,
	}
	return outbound{
		Tag: WarpTag, Protocol: "wireguard",
		Settings: wireGuardSettings{
			SecretKey: identity.SecretKey, Address: familyAddresses(identity.Addresses, family),
			Peers: []wireGuardPeer{peer}, MTU: mtu, NoKernelTun: true,
		},
	}
}

// wireGuardSettings is the core's WireGuard outbound shape. The field names are the core's, not ours.
type wireGuardSettings struct {
	SecretKey   string          `json:"secretKey"`
	Address     []string        `json:"address"`
	Peers       []wireGuardPeer `json:"peers"`
	MTU         int             `json:"mtu,omitempty"`
	NoKernelTun bool            `json:"noKernelTun"`
}

type wireGuardPeer struct {
	PublicKey  string   `json:"publicKey"`
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowedIPs"`
	Reserved   []int    `json:"reserved,omitempty"`
}

func warpSocks(w WarpSpec) (string, int) {
	address := w.Socks
	if address == "" {
		address = DefaultWarpSocks
		if w.UsesUsque() {
			address = DefaultUsqueSocks
		}
	}
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		host, rawPort = "127.0.0.1", "40000"
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		port = 40000
		if w.UsesUsque() {
			port = 40001
		}
	}
	return host, port
}

// warpRules are the routing decisions for proxied traffic: TCP takes the WARP hop, UDP goes direct.
// warp-cli in proxy mode answers UDP ASSOCIATE with "command not supported", and a SOCKS5 proxy that
// cannot carry UDP does not fail loudly — the flow simply never returns, and the first thing a client
// sends is DNS. Splitting the rule keeps the address WARP is there to hide for TCP, and keeps UDP
// working at the price of the node's own address. See docs/OPERATIONS.md.
// warpRules are the routing decisions for proxied traffic. On the wireguard transport both protocols
// take the tunnel, because the tunnel carries both. On the proxy transport TCP takes the WARP hop and
// UDP goes direct: warp-cli in proxy mode answers UDP ASSOCIATE with "command not supported", and a
// SOCKS5 proxy that cannot carry UDP does not fail loudly — the flow simply never returns, and the
// first thing a client sends is DNS.
func warpRules(w WarpSpec, balancerTag string) []rule {
	if w.CarriesUDP() {
		tunnel := rule{Type: "field", Network: "tcp,udp"}
		if w.Mode == "selected" {
			domains := append([]string(nil), w.Selected...)
			sort.Strings(domains)
			tunnel.Domain = domains
		}
		if balancerTag != "" {
			tunnel.BalancerTag = balancerTag
		} else {
			tunnel.OutboundTag = WarpTag
		}
		if !w.UsesWireGuard() {
			// The MASQUE sidecar carries both families through its own stack, so there is no untaken
			// family to refuse and no default route to choose.
			return []rule{tunnel}
		}
		// The tunnel carries one family, so the other one is named explicitly rather than left to the
		// trailing default. It used to go direct — the way it went before WARP existed — which meant it
		// left the node on the node's own address, the identifier the tunnel exists to hide. It is refused
		// instead: a client sees a connection that fails and can say so, rather than an address it never
		// asked for. The cost is that a service reachable only over the untaken family does not work
		// through this node; the default family is IPv4, so that is IPv6-only services.
		other := rule{
			Type: "field", Network: "tcp,udp",
			IP: []string{otherFamilyRoute(w.FamilyOrDefault())}, OutboundTag: "block",
		}
		return []rule{other, tunnel}
	}
	proxied := rule{Type: "field", Network: "tcp"}
	direct := rule{Type: "field", Network: "udp", OutboundTag: "direct"}
	if w.Mode == "selected" {
		domains := append([]string(nil), w.Selected...)
		sort.Strings(domains)
		proxied.Domain = domains
		direct.Domain = domains
	}
	if balancerTag != "" {
		proxied.BalancerTag = balancerTag
	} else {
		proxied.OutboundTag = WarpTag
	}
	return []rule{proxied, direct}
}

// APIAddress is the loopback address of the management API for this spec.
func (s Spec) APIAddress() string { return fmt.Sprintf("%s:%d", DefaultListen, s.APIPort) }

// ProxyAddress is the loopback address nginx proxies to.
func (s Spec) ProxyAddress() string { return fmt.Sprintf("%s:%d", s.Listen, s.Port) }

// --- the JSON document -------------------------------------------------------

type config struct {
	Log         logConfig    `json:"log"`
	API         apiConfig    `json:"api"`
	Inbounds    []inbound    `json:"inbounds"`
	Outbounds   []outbound   `json:"outbounds"`
	Routing     routing      `json:"routing"`
	Observatory *observatory `json:"observatory,omitempty"`
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
	// RouteOnly is omitted when false so the default document keeps its shape: only the BitTorrent
	// rule has an opinion about it, and every other config would carry a line nobody chose.
	RouteOnly bool `json:"routeOnly,omitempty"`
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

type socksSettings struct {
	Servers []socksServer `json:"servers"`
}

// socksInboundSettings is the loopback inbound the QUIC transport enters through: no authentication
// because the socket is local and belongs to the transport, and UDP because half of its traffic is.
type socksInboundSettings struct {
	Auth string `json:"auth"`
	UDP  bool   `json:"udp"`
}

type socksServer struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type routing struct {
	DomainStrategy string     `json:"domainStrategy"`
	Rules          []rule     `json:"rules"`
	Balancers      []balancer `json:"balancers,omitempty"`
}

// rule is one routing decision. Most rules set OutboundTag; a WARP rule with fail-open sets
// BalancerTag instead, and the balancer falls back to direct when WARP is not answering.
type rule struct {
	Type        string   `json:"type"`
	InboundTag  []string `json:"inboundTag,omitempty"`
	Protocol    []string `json:"protocol,omitempty"`
	Domain      []string `json:"domain,omitempty"`
	IP          []string `json:"ip,omitempty"`
	Network     string   `json:"network,omitempty"`
	OutboundTag string   `json:"outboundTag,omitempty"`
	BalancerTag string   `json:"balancerTag,omitempty"`
}

// balancer picks one of the tagged outbounds. A single selector plus fallbackTag is the whole
// fail-open mechanism: while WARP answers the observatory's probe, traffic uses it; when it stops
// answering, the balancer moves to direct without a config reload or a restart.
type balancer struct {
	Tag         string    `json:"tag"`
	Selector    []string  `json:"selector"`
	Strategy    *strategy `json:"strategy,omitempty"`
	FallbackTag string    `json:"fallbackTag,omitempty"`
}

type strategy struct {
	Type string `json:"type"`
}

// observatory probes the outbounds a balancer may pick. Its probe goes through the outbound itself,
// so for WARP it is a real end-to-end test of the sidecar.
type observatory struct {
	SubjectSelector   []string `json:"subjectSelector"`
	ProbeURL          string   `json:"probeUrl"`
	ProbeInterval     string   `json:"probeInterval"`
	EnableConcurrency bool     `json:"enableConcurrency,omitempty"`
}
