package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/shogun-kub/termit/internal/mn/mesh"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// maxEventBatch caps one journal answer: a restoring peer asks for a page, and a page it cannot hold in
// memory is not a page.
const maxEventBatch = 1000

// peerSnapshot answers the whole registry, for a peer that has no cursor yet: the journal alone cannot
// say what happened before the journal existed.
func (h *Handler) peerSnapshot(w http.ResponseWriter, r *http.Request) {
	if !h.peerAllowed(w, r) {
		return
	}
	snap, err := h.opts.Store.Snapshot(r.Context())
	if err != nil {
		h.opts.Logger.Error("peer snapshot", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read its registry"))
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// peerEvents answers the journal after a cursor: seq > after, oldest first, at most limit entries.
func (h *Handler) peerEvents(w http.ResponseWriter, r *http.Request) {
	if !h.peerAllowed(w, r) {
		return
	}
	query := r.URL.Query()
	after := int64(0)
	if raw := query.Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest, "",
				"Bad Request", "after is not a sequence number"))
			return
		}
		after = n
	}
	limit := maxEventBatch
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest, "",
				"Bad Request", "limit is not a positive number"))
			return
		}
		if n < limit {
			limit = n
		}
	}
	mnID, err := h.opts.Store.MeshID(r.Context())
	if err != nil {
		h.opts.Logger.Error("peer events: mesh id", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read its identity"))
		return
	}
	events, err := h.opts.Store.EventsSince(r.Context(), mnID, after, limit)
	if err != nil {
		if errors.Is(err, store.ErrReplicationGap) {
			writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict,
				proto.CodeConflict, "Conflict", "the journal no longer holds that cursor; take a snapshot"))
			return
		}
		h.opts.Logger.Error("peer events", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read its journal"))
		return
	}
	last, err := h.opts.Store.LastSeq(r.Context(), mnID)
	if err != nil {
		h.opts.Logger.Error("peer events: last seq", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read its journal"))
		return
	}
	writeJSON(w, http.StatusOK, mesh.Events{MNID: mnID, LastSeq: last, Events: events})
}

// peerReport accepts one node's self-report from the replica that took it while this server was away
// (IMPLEMENTATION_PLAN.md §5.2.1 decision 3). It is the only peer endpoint that writes, and it writes only
// what a direct sync would have written: the row is the home's, so the state machine still runs here once.
func (h *Handler) peerReport(w http.ResponseWriter, r *http.Request) {
	if !h.peerAllowed(w, r) {
		return
	}
	var report proto.Report
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest, "",
			"Bad Request", "the report body is not valid: "+err.Error()))
		return
	}
	if report.UV == "" || report.At.IsZero() {
		writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest, "",
			"Bad Request", "a report needs a node and a time"))
		return
	}
	node, err := h.opts.Store.UVByID(r.Context(), report.UV)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// A node enrolled after the replica's snapshot is unknown here too; the replica cannot have
			// served it, so this is a stale queue entry rather than a report to apply.
			writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict, proto.CodeConflict,
				"Conflict", "this server does not know that node"))
			return
		}
		h.opts.Logger.Error("peer report: read the node", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read the node"))
		return
	}
	// The fence: a replica may hand back reports only for rows this server homes. An empty home means
	// this server — every row enrolled here.
	if node.HomeMN != "" {
		mnID, err := h.opts.Store.MeshID(r.Context())
		if err != nil {
			h.opts.Logger.Error("peer report: mesh id", "err", err)
			writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
				proto.CodeInternal, "Internal Server Error", "the server could not read its identity"))
			return
		}
		if node.HomeMN != mnID {
			writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict, proto.CodeConflict,
				"Conflict", "that node is homed by another server"))
			return
		}
	}
	// A report older than the row it describes loses: the replica may hold a queue from an outage, and
	// one that arrives after the node already synced here must not rewind what this server knows.
	if node.LastSeenAt != nil && report.At.Before(*node.LastSeenAt) {
		writeJSON(w, http.StatusOK, map[string]bool{"applied": false})
		return
	}
	h.recordReport(r.Context(), node, report.Sync, report.IP, report.Cert, report.At.UTC())
	writeJSON(w, http.StatusOK, map[string]bool{"applied": true})
}

// peerAllowed checks the mesh credential: an HMAC of the master key under a label of its own, so it can
// only be used for this and only by a server that holds that key. An unset key fails closed rather than
// accepting the token of the empty key.
func (h *Handler) peerAllowed(w http.ResponseWriter, r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if len(h.opts.MasterKey) == 0 || !strings.HasPrefix(header, "Bearer ") {
		h.refusePeer(w)
		return false
	}
	if !mesh.CheckPullToken(h.opts.MasterKey, strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))) {
		h.refusePeer(w)
		return false
	}
	return true
}

// refusePeer gives one answer for a missing, wrong or unusable credential: which of the three it was is
// not the caller's business.
func (h *Handler) refusePeer(w http.ResponseWriter) {
	writeProblem(w, http.StatusUnauthorized, proto.NewProblem(http.StatusUnauthorized,
		proto.CodeUnauthorized, "Unauthorized", "a mesh credential is required"))
}
