package proto

import "time"

// CheckReport is what a node says about its own last self-check.
type CheckReport struct {
	OK         bool      `json:"ok"`
	ErrorClass string    `json:"error_class,omitempty"`
	LatencyMS  int       `json:"latency_ms,omitempty"`
	At         time.Time `json:"at"`
}

// SyncRequest is what `POST /api/v1/agent/sync` accepts.
type SyncRequest struct {
	AgentVersion string       `json:"agent_version"`
	Check        *CheckReport `json:"check,omitempty"`
	// Transports is what this node serves. A node that predates the field sends nothing, which every
	// server reads as the TLS profile alone — the only thing a node could serve before it existed.
	Transports []Transport `json:"transports,omitempty"`
	// Egress is what this node's configuration asks its proxy traffic to leave through: "warp" or
	// "direct". It is the intention, not the fact — the server measures the fact through the tunnel
	// and flags the two apart, which is how a dead tunnel with fail-open shows up.
	Egress string `json:"egress,omitempty"`
}

// HeaderCertSHA256 carries the leaf certificate the node currently serves, lowercase hex, so the
// server can pin its probes to what the node actually presents. It is a header rather than a body
// field: an older server ignores a header it does not know instead of rejecting the whole body, and
// the fingerprint is public data.
const HeaderCertSHA256 = "X-Territ-Cert-SHA256"

// SyncPeer is one node as the asking node must configure it.
type SyncPeer struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
	Label  string `json:"label"`
	// UUID is the *pair* credential for this relationship, valid on this pair and nowhere else.
	UUID  string `json:"uuid"`
	Path  string `json:"path"`
	Mode  string `json:"mode"`
	State string `json:"state"`
	// Degraded means the peer is still usable but currently failing its checks.
	Degraded bool `json:"degraded,omitempty"`
}

// SyncPolicy carries the timing decisions so a node does not have to guess them.
type SyncPolicy struct {
	CheckIntervalSeconds int `json:"check_interval_seconds"`
	SyncIntervalSeconds  int `json:"sync_interval_seconds"`
	// MinSyncDelaySeconds is how long a node waits after enrollment before its first own cycle.
	MinSyncDelaySeconds int `json:"min_sync_delay_seconds"`
	// MeshPollSeconds is how often the node may ask /healthz whether the mesh changed; 0 turns the
	// poll off, and the node then learns a join at its next scheduled sync.
	MeshPollSeconds int `json:"mesh_poll_seconds"`
}

// SyncResponse is the complete desired state. It is a full set, not a delta: a node that missed a
// response must still converge on the next one.
type SyncResponse struct {
	APIVersion string `json:"api_version"`
	Self       struct {
		ID        string `json:"id"`
		Domain    string `json:"domain"`
		State     string `json:"state"`
		ProbeUUID string `json:"probe_uuid"`
	} `json:"self"`
	Peers           []SyncPeer `json:"peers"`
	SubscriptionURL string     `json:"subscription_url"`
	Policy          SyncPolicy `json:"policy"`
	NextSyncAfter   time.Time  `json:"next_sync_after"`
	// MeshGeneration identifies the mesh this answer describes. The node remembers it and compares it
	// with what /healthz reports, so a join reaches the other nodes within a poll instead of at their
	// next scheduled sync.
	MeshGeneration string `json:"mesh_generation,omitempty"`
	// ServerVersion is the build the server runs. The node remembers it so a menu can offer exactly the
	// version the mesh was proved against — never "latest", never a prerelease. Older servers omit it and
	// older nodes ignore it: the field is additive in both directions.
	ServerVersion string `json:"server_version,omitempty"`
	// MinAgentVersion is the oldest node build the server still supports, when its operator named one.
	// It is a declaration, not a gate: the node reports that it is below it and keeps serving, because
	// a refusal keyed on the server's word can strand a node that is working (docs/DESCRIPTION.md §8).
	MinAgentVersion string `json:"min_agent_version,omitempty"`
	// ReadOnly is set when this answer comes from a replica of the node's home: the node gets the last
	// desired state that replica holds, its report waits there for the home, and nothing on the replica
	// moves a state. A stale answer, never a wrong one (docs/IMPLEMENTATION_PLAN.md §5.2.1 decision 3).
	ReadOnly bool `json:"read_only,omitempty"`
	// Home names the server that owns this node when ReadOnly is set, so the operator knows which box to
	// look at and the node can say whose outage it is riding out.
	Home string `json:"home,omitempty"`
}

// Report is what a replica hands back to a node's home: the self-report the node sent, unchanged, plus
// where and when it arrived. The home applies it through the same path a direct sync would, so the state
// machine runs once, on the server whose job it is.
type Report struct {
	UV   string      `json:"uv"`
	At   time.Time   `json:"at"`
	From string      `json:"from,omitempty"`
	IP   string      `json:"ip,omitempty"`
	Cert string      `json:"cert_sha256,omitempty"`
	Sync SyncRequest `json:"sync"`
}
