package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/cred"
	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// sync serves POST /api/v1/agent/sync: the complete desired peer set for one node.
//
// The response is a full set rather than a delta on purpose. A node that missed the previous response,
// or that restarted with an empty memory, must converge from this one answer alone.
func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
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
	if err := h.opts.Store.TouchUV(r.Context(), node.ID, sourceIP(r), strings.TrimSpace(req.AgentVersion), now); err != nil {
		h.opts.Logger.Error("sync: touch node", "uv", node.ID, "err", err)
	}
	// The node reports the certificate it serves and the prober pins it. A renewal replaces that
	// leaf; a server still pinning the old one would close every later check on a mismatch, so the pin
	// follows the report. It is authenticated by the agent key and the fingerprint is public.
	if pin := normalizePin(r.Header.Get(proto.HeaderCertSHA256)); pin != "" && pin != node.CertSHA256 {
		if err := h.opts.Store.SetUVCertPin(r.Context(), node.ID, pin); err != nil {
			h.opts.Logger.Error("sync: record the certificate pin", "uv", node.ID, "err", err)
		} else {
			h.opts.Logger.Info("sync: certificate pin updated", "uv", node.ID)
		}
	}
	// The self-report is kept next to the server's own probes. It never moves a node's state — only
	// the probe does — but it lets an operator tell "the box is broken" from "the path is broken".
	if req.Check != nil {
		if err := h.opts.Store.AppendCheck(r.Context(), store.Check{
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

	peers, err := h.opts.Store.ListUVs(r.Context(), store.UVFilter{
		States: []store.State{store.StateActive, store.StateUnavailable},
	})
	if err != nil {
		h.opts.Logger.Error("sync: list peers", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read the registry"))
		return
	}

	response := proto.SyncResponse{APIVersion: proto.APIVersion}
	response.Self.ID = node.ID
	response.Self.Domain = node.Domain
	response.Self.State = string(node.State)
	response.Self.ProbeUUID = node.ProbeUUID
	response.SubscriptionURL = h.subscriptionURLFor(node)
	response.Policy = proto.SyncPolicy{
		CheckIntervalSeconds: int(h.opts.CheckInterval.Seconds()),
		SyncIntervalSeconds:  int(h.opts.SyncInterval.Seconds()),
		MinSyncDelaySeconds:  int(h.opts.MinSyncDelay.Seconds()),
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
			h.opts.Logger.Error("sync: derive a pair credential", "uv", node.ID, "peer", peer.ID, "err", err)
			writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
				proto.CodeInternal, "Internal Server Error", "the server could not derive a credential"))
			return
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

	h.opts.Logger.Info("sync served", "uv", node.ID, "peers", len(response.Peers))
	writeJSON(w, http.StatusOK, response)
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
