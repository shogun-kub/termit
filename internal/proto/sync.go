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
}

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
}
