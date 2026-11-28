// Package proto: the second vantage's wire shape.
//
// A home server decides a node's state; it may ask a peer on another network what it sees and fold that
// answer into its own verdict (docs/IMPLEMENTATION_PLAN.md §5.2.4). The call carries one probe target and
// answers with the result the peer's own prober produced, so the home's transition table, its grace streak
// and its "unknown is not failed" rule stay exactly as they are.

package proto

import "time"

// PeerProbePath is the peer endpoint a vantage answers on, relative to the API prefix.
const PeerProbePath = "/peer/probe"

// ProbeTarget is one node as a peer needs it to make the same request the home would make: the probe
// credential, the secret path, the certificate pin and the transport. It is the ability to connect to a
// node the peer does not own, which is why the delegation is listed per domain in the configuration.
type ProbeTarget struct {
	ID               string `json:"id,omitempty"`
	Address          string `json:"address"`
	Port             int    `json:"port"`
	ServerName       string `json:"server_name"`
	Path             string `json:"path"`
	Mode             string `json:"mode,omitempty"`
	UUID             string `json:"uuid"`
	PinnedCertSHA256 string `json:"pinned_cert_sha256,omitempty"`
	Transport        string `json:"transport,omitempty"`
}

// ProbeRequest is what a home server posts to a vantage.
type ProbeRequest struct {
	Target ProbeTarget `json:"target"`
}

// ProbeAnswer is one observation: what the peer's prober concluded, and when. It is not a state, and a
// caller that finds it stale must treat it as no answer at all.
type ProbeAnswer struct {
	// OK is the verdict; Class names why not when it is false, in probe's own vocabulary.
	OK        bool      `json:"ok"`
	Class     string    `json:"class,omitempty"`
	Error     string    `json:"error,omitempty"`
	LatencyMS int       `json:"latency_ms,omitempty"`
	At        time.Time `json:"at"`
	// Vantage names the peer that answered, so a disagreement can be investigated afterwards.
	Vantage string `json:"vantage,omitempty"`
}
