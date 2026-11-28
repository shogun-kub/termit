package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/cred"
	"github.com/shogun-kub/termit/internal/mn/metrics"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/version"
)

// sync serves POST /api/v1/agent/sync: the complete desired peer set for one node.
//
// The response is a full set rather than a delta on purpose. A node that missed the previous response,
// or that restarted with an empty memory, must converge from this one answer alone.
func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	outcome := metrics.ResultRefused
	defer func() { h.observeSync(outcome) }()

	node, refused := h.authenticateAgent(w, r)
	if refused {
		return
	}

	var req proto.SyncRequest
	if r.ContentLength > 0 {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
				proto.CodeURLInvalid, "Bad Request", "the sync body is not valid: "+err.Error()))
			return
		}
	}

	now := h.opts.Now().UTC()
	own, mnID, err := h.homesNode(r.Context(), node)
	if err != nil {
		h.opts.Logger.Error("sync: read this server's identity", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read its identity"))
		return
	}
	if !own {
		// The node's home is elsewhere: this server answers from its copy and keeps the report for the
		// home. It never writes the row — a replica that moved one would be a second authority.
		response, err := h.replicaAnswer(r.Context(), node, req, sourceIP(r),
			r.Header.Get(proto.HeaderCertSHA256), now, mnID)
		if err != nil {
			h.opts.Logger.Error("sync: build the answer", "uv", node.ID, "err", err)
			writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
				proto.CodeInternal, "Internal Server Error", "the server could not build the answer"))
			return
		}
		h.opts.Logger.Info("sync served read-only", "uv", node.ID, "home", node.HomeMN,
			"peers", len(response.Peers))
		outcome = metrics.ResultOK
		writeJSON(w, http.StatusOK, response)
		return
	}

	h.recordReport(r.Context(), node, req, sourceIP(r), r.Header.Get(proto.HeaderCertSHA256), now)

	response, err := h.desiredState(r.Context(), node, now)
	if err != nil {
		h.opts.Logger.Error("sync: build the answer", "uv", node.ID, "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not build the answer"))
		return
	}
	h.opts.Logger.Info("sync served", "uv", node.ID, "peers", len(response.Peers))
	outcome = metrics.ResultOK
	writeJSON(w, http.StatusOK, response)
}

// desiredState builds what a node is told: its own record, the complete peer set and the policy. It reads
// and derives only, so a replica can answer with it for a row it does not home, and a write added here
// would silently give a replica authority it must not have.
func (h *Handler) desiredState(ctx context.Context, node store.UV, now time.Time) (proto.SyncResponse, error) {
	peers, err := h.opts.Store.ListUVs(ctx, store.UVFilter{
		States: []store.State{store.StateActive, store.StateUnavailable},
	})
	if err != nil {
		return proto.SyncResponse{}, fmt.Errorf("list peers: %w", err)
	}

	// The node is told what build it is talking to, so a menu can offer exactly this version as an update
	// target. It is the only authority in the mesh: there is nothing above it for the node to follow.
	response := proto.SyncResponse{APIVersion: proto.APIVersion, ServerVersion: version.Version,
		MinAgentVersion: h.opts.MinAgentVersion}
	response.Self.ID = node.ID
	response.Self.Domain = node.Domain
	response.Self.State = string(node.State)
	response.Self.ProbeUUID = node.ProbeUUID
	response.SubscriptionURL = h.subscriptionURLFor(node)
	// The digest is what this node remembers to notice a change between scheduled syncs; a registry it
	// cannot read is not a reason to refuse the answer.
	if mesh, err := h.meshGeneration(ctx); err != nil {
		h.opts.Logger.Error("sync: mesh generation", "err", err)
	} else {
		response.MeshGeneration = mesh
	}
	response.Policy = proto.SyncPolicy{
		CheckIntervalSeconds: int(h.opts.CheckInterval.Seconds()),
		SyncIntervalSeconds:  int(h.opts.SyncInterval.Seconds()),
		MinSyncDelaySeconds:  int(h.opts.MinSyncDelay.Seconds()),
		MeshPollSeconds:      int(h.opts.MeshPoll / time.Second),
	}
	// Jitter keeps a group that enrolled together from checking together for the rest of its life.
	interval := h.opts.CheckInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	response.NextSyncAfter = now.Add(interval)

	for _, peer := range peers {
		profile, err := profileOf(peer)
		if err != nil {
			h.opts.Logger.Warn("sync: skipping a peer with an unreadable profile", "uv", peer.ID, "err", err)
			continue
		}
		if peer.ID == node.ID {
			// The node's own entry is its self credential, which it already has; sending it back
			// would put the owner's credential in a second place for no reason.
			continue
		}
		// The pair credential depends on both epochs, and takes the lower one, so a node that has
		// rotated and one that has not still derive the same value.
		epoch := node.CredentialEpoch
		if peer.CredentialEpoch < epoch {
			epoch = peer.CredentialEpoch
		}
		pair, err := cred.Pair(h.opts.MasterKey, node.ID, peer.ID, epoch)
		if err != nil {
			return proto.SyncResponse{}, fmt.Errorf("derive the credential for %s: %w", peer.ID, err)
		}
		response.Peers = append(response.Peers, proto.SyncPeer{
			ID:       peer.ID,
			Domain:   peer.Domain,
			Label:    labelOf(peer, profile),
			UUID:     pair,
			Path:     profile.Path,
			Mode:     proto.Mode,
			State:    string(peer.State),
			Degraded: peer.State == store.StateUnavailable,
		})
	}
	return response, nil
}

// recordReport stores what a node said about itself: that it was seen, what it serves, which certificate it
// presents and how its own self-check went. It never moves a state — only a probe does.
func (h *Handler) recordReport(ctx context.Context, node store.UV, req proto.SyncRequest, ip, cert string,
	now time.Time) {
	if err := h.opts.Store.TouchUV(ctx, node.ID, ip, strings.TrimSpace(req.AgentVersion), now); err != nil {
		h.opts.Logger.Error("sync: touch node", "uv", node.ID, "err", err)
	}
	// What the node serves decides what the subscription can render for it. A kind this server does not
	// know is dropped rather than refused: a newer node must still be usable, and a link this server
	// cannot write correctly is worse than no link.
	//
	// The record is written on every sync, including the empty report: "nothing announced" means the TLS
	// profile alone, and a node that stopped serving the transport must lose its second link. Writing only
	// a non-empty report would leave the old row in place for ever — the one case where a node retires a
	// transport and the mesh keeps advertising it.
	kinds := proto.ServedKinds(req.Transports)
	if len(kinds) == 0 {
		kinds = []string{proto.KindVLESS}
	}
	// The node's intention about its egress, kept next to the measurement the probe makes. A mismatch is
	// the whole point: it is what a dead tunnel with fail-open looks like from here.
	if wants := strings.TrimSpace(req.Egress); wants != "" {
		if err := h.opts.Store.SetUVEgressWant(ctx, node.ID, wants); err != nil {
			h.opts.Logger.Error("sync: record the wanted egress", "uv", node.ID, "err", err)
		}
	}
	if err := h.opts.Store.SetUVTransports(ctx, node.ID, kinds,
		proto.ServedRenderings(req.Transports, proto.KindVLESS)); err != nil {
		h.opts.Logger.Error("sync: record the transports", "uv", node.ID, "err", err)
	}

	// The node reports the certificate it serves and the prober pins it. A renewal replaces that
	// leaf; a server still pinning the old one would close every later check on a mismatch, so the pin
	// follows the report. It is authenticated by the agent key and the fingerprint is public.
	if pin := normalizePin(cert); pin != "" && pin != node.CertSHA256 {
		if err := h.opts.Store.SetUVCertPin(ctx, node.ID, pin); err != nil {
			h.opts.Logger.Error("sync: record the certificate pin", "uv", node.ID, "err", err)
		} else {
			h.opts.Logger.Info("sync: certificate pin updated", "uv", node.ID)
		}
	}
	// The self-report is kept next to the server's own probes. It never moves a node's state — only
	// the probe does — but it lets an operator tell "the box is broken" from "the path is broken".
	if req.Check != nil {
		if err := h.opts.Store.AppendCheck(ctx, store.Check{
			UVID:       node.ID,
			At:         now,
			Kind:       "self",
			OK:         req.Check.OK,
			LatencyMS:  req.Check.LatencyMS,
			ErrorClass: req.Check.ErrorClass,
		}); err != nil {
			h.opts.Logger.Error("sync: record the self-check", "uv", node.ID, "err", err)
		}
	}
}

// homesNode reports whether this server is the node's home. A row with an empty home is this server's:
// every node enrolled here, and every row that existed before a mesh did. The mesh id is read only for a
// replicated row, so a single-server install pays nothing for the check.
func (h *Handler) homesNode(ctx context.Context, node store.UV) (bool, string, error) {
	if node.HomeMN == "" {
		return true, "", nil
	}
	mnID, err := h.opts.Store.MeshID(ctx)
	if err != nil {
		return false, "", err
	}
	return node.HomeMN == mnID, mnID, nil
}

// replicaAnswer is the read-only path: keep the report for the home, then answer with the last desired
// state this copy holds. A node reading the other half of a mesh therefore loses nothing — its report is
// applied once, by the server whose job it is, when that server is reachable again.
func (h *Handler) replicaAnswer(ctx context.Context, node store.UV, req proto.SyncRequest, ip, cert string,
	now time.Time, mnID string) (proto.SyncResponse, error) {
	report := proto.Report{UV: node.ID, At: now, From: mnID, IP: ip, Cert: normalizePin(cert), Sync: req}
	payload, err := json.Marshal(report)
	if err != nil {
		return proto.SyncResponse{}, fmt.Errorf("build the report: %w", err)
	}
	if err := h.opts.Store.QueueReport(ctx, node.ID, now, payload); err != nil {
		// The node still needs its answer: a lost queue entry costs it one more report at the next sync,
		// while a refused answer costs it its peers. Log it and serve.
		h.opts.Logger.Error("sync: queue the report", "uv", node.ID, "err", err)
	}
	response, err := h.desiredState(ctx, node, now)
	if err != nil {
		return proto.SyncResponse{}, err
	}
	response.ReadOnly = true
	response.Home = node.HomeMN
	return response, nil
}

// observeSync records one agent sync, tolerating a handler built without a registry.
func (h *Handler) observeSync(outcome string) {
	if h.opts.Metrics != nil {
		h.opts.Metrics.ObserveSync(outcome)
	}
}

// authenticateAgent resolves the bearer token to a node. A blocked or removed node gets the same
// answer as an unknown key: it has no business here.
func (h *Handler) authenticateAgent(w http.ResponseWriter, r *http.Request) (store.UV, bool) {
	return h.authenticateAgentWith(w, r, false)
}

// authenticateAgentWith is the same lookup, optionally admitting a blocked node. Only the unblock
// endpoint asks for that: a blocked node has to be able to prove it is healthy again, and every
// other endpoint would let it back into the mesh without an explicit decision.
func (h *Handler) authenticateAgentWith(w http.ResponseWriter, r *http.Request, allowBlocked bool) (store.UV, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		writeProblem(w, http.StatusUnauthorized, proto.NewProblem(http.StatusUnauthorized,
			proto.CodeUnauthorized, "Unauthorized", "a bearer agent key is required"))
		return store.UV{}, true
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		writeProblem(w, http.StatusUnauthorized, proto.NewProblem(http.StatusUnauthorized,
			proto.CodeUnauthorized, "Unauthorized", "a bearer agent key is required"))
		return store.UV{}, true
	}
	node, err := h.opts.Store.UVByAgentKeyHash(r.Context(), secret.Digest(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// One answer for a wrong key and for a node that is gone: the difference is not a client's
			// business, and saying it would confirm that a guess was close.
			writeProblem(w, http.StatusUnauthorized, proto.NewProblem(http.StatusUnauthorized,
				proto.CodeUnauthorized, "Unauthorized", "the agent key is not valid"))
			return store.UV{}, true
		}
		h.opts.Logger.Error("sync: agent lookup", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read the node"))
		return store.UV{}, true
	}
	if node.State == store.StateRemoved || (node.State == store.StateBlocked && !allowBlocked) {
		writeProblem(w, http.StatusForbidden, proto.NewProblem(http.StatusForbidden,
			proto.CodeNodeInactive, "Forbidden", "this node is not in the mesh"))
		return store.UV{}, true
	}
	return node, false
}

// normalizePin accepts a leaf fingerprint only in the one shape the prober pins: 64 hex characters.
// Anything else is ignored instead of stored, so a malformed report cannot disable a node's checks.
func normalizePin(raw string) string {
	pin := strings.ToLower(strings.TrimSpace(raw))
	if len(pin) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(pin); err != nil {
		return ""
	}
	return pin
}

// subscriptionURLFor rebuilds the URL from the sealed copy of the token: the digest answers lookups,
// and only the master key turns the sealed copy back into something a client can use.
func (h *Handler) subscriptionURLFor(node store.UV) string {
	if node.SubTokenSealed == "" || h.opts.Domain == "" {
		return ""
	}
	token, err := secret.Open(h.opts.MasterKey, node.SubTokenSealed)
	if err != nil {
		h.opts.Logger.Error("sync: cannot open the sealed subscription token", "uv", node.ID, "err", err)
		return ""
	}
	return "https://" + h.opts.Domain + "/sub/" + token
}
