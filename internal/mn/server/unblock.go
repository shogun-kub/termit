package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shogun-kub/termit/internal/mn/enroll"
	"github.com/shogun-kub/termit/internal/mn/sched"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/proto"
)

// unblock serves POST /api/v1/agent/unblock: Scenario B. A blocked node asks to be let back in, and
// the server repeats the enrollment checks against it — DNS must point at the requesting box, and the
// tunnel must carry traffic. Only a pass moves the node; a failure keeps it blocked and records why.
//
// This is the only agent endpoint that admits a blocked node: a node that has no other way to ask is
// the whole point of Scenario B.
func (h *Handler) unblock(w http.ResponseWriter, r *http.Request) {
	node, refused := h.authenticateAgentWith(w, r, true)
	if refused {
		return
	}

	var req proto.UnblockRequest
	if r.ContentLength > 0 {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
				proto.CodeURLInvalid, "Bad Request", "the unblock body is not valid: "+err.Error()))
			return
		}
	}
	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	req.BasicVLESSURL = strings.TrimSpace(req.BasicVLESSURL)

	now := h.opts.Now().UTC()
	profile, err := profileOf(node)
	if err != nil {
		h.opts.Logger.Error("unblock: stored profile is unreadable", "uv", node.ID, "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the stored profile of this node is unreadable"))
		return
	}
	if req.Domain != "" && !strings.EqualFold(req.Domain, node.Domain) {
		// A different host is Scenario C: relocating a node is not the same promise as unblocking it.
		returnRelocateRequired(w, node.Domain, req.Domain)
		return
	}
	if req.BasicVLESSURL != "" {
		updated, err := proto.ParseVLESSUrl(req.BasicVLESSURL)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
				proto.CodeURLInvalid, "Bad Request", err.Error()))
			return
		}
		if !strings.EqualFold(updated.Domain, node.Domain) {
			returnRelocateRequired(w, node.Domain, updated.Domain)
			return
		}
		profile = updated
	}

	switch node.State {
	case store.StateActive:
		// Idempotent: an owner who runs the command twice does not get an error for it.
		h.writeUnblocked(w, node, "already active; nothing changed")
		return
	case store.StateBlocked:
		// The only state an unblock may move.
	default:
		writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict,
			proto.CodeConflict, "Conflict",
			fmt.Sprintf("this node is %s; only a blocked node can be unblocked", node.State)))
		return
	}

	pin, err := h.opts.Enrollment.Verify(r.Context(), profile, sourceIP(r))
	if err != nil {
		h.recordUnblockFailure(r, node, err, now)
		var refusal *enroll.Error
		if errors.As(err, &refusal) {
			writeProblem(w, refusal.Status, proto.NewProblem(refusal.Status, refusal.Code, titleFor(refusal.Status), refusal.Detail))
			return
		}
		h.opts.Logger.Error("unblock: verification failed", "uv", node.ID, "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not verify this node"))
		return
	}

	// A rebuilt box presents a new self UUID and certificate. The node id, the probe credential and
	// every pair credential are keyed by the record, so peers need no change.
	if req.BasicVLESSURL != "" {
		profileJSON, err := json.Marshal(profile)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
				proto.CodeInternal, "Internal Server Error", "the server could not store the new profile"))
			return
		}
		if err := h.opts.Store.SetUVEndpoint(r.Context(), node.ID, profile.UUID, string(profileJSON), pin); err != nil {
			h.opts.Logger.Error("unblock: store the new endpoint", "uv", node.ID, "err", err)
			writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
				proto.CodeInternal, "Internal Server Error", "the server could not store the new profile"))
			return
		}
	} else if pin != "" && pin != node.CertSHA256 {
		if err := h.opts.Store.SetUVCertPin(r.Context(), node.ID, pin); err != nil {
			h.opts.Logger.Error("unblock: refresh the certificate pin", "uv", node.ID, "err", err)
		}
	}

	outcome := sched.Transition(sched.State(node.State), sched.EventUnblock)
	if outcome.Changed {
		if _, err := h.opts.Store.SetUVState(r.Context(), node.ID, store.StateActive,
			"unblocked after a successful check", "agent", now); err != nil {
			h.opts.Logger.Error("unblock: change state", "uv", node.ID, "err", err)
			writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
				proto.CodeInternal, "Internal Server Error", "the server could not update the node"))
			return
		}
	}
	if err := h.opts.Store.SetFailedCycles(r.Context(), node.ID, 0); err != nil {
		h.opts.Logger.Error("unblock: reset the failed cycles", "uv", node.ID, "err", err)
	}
	if err := h.opts.Store.AppendAudit(r.Context(), store.AuditRecord{
		At: now, Actor: "agent", Action: "uv.unblock", Target: node.ID,
		DetailJSON: fmt.Sprintf(`{"from":%q,"to":%q,"domain":%q,"new_url":%t}`,
			node.State, store.StateActive, node.Domain, req.BasicVLESSURL != ""),
	}); err != nil {
		h.opts.Logger.Warn("unblock: audit", "uv", node.ID, "err", err)
	}

	h.opts.Logger.Info("node unblocked", "uv", node.ID, "domain", node.Domain, "ip", sourceIP(r))
	h.writeUnblocked(w, node, "the checks passed and the node is active again")
}

// recordUnblockFailure keeps the evidence of a refused unblock. The state table is explicit that a
// failed request changes nothing, so there is no transition to apply — only a record to append.
func (h *Handler) recordUnblockFailure(r *http.Request, node store.UV, err error, now time.Time) {
	code := proto.CodeCheckFailed
	var refusal *enroll.Error
	if errors.As(err, &refusal) {
		code = refusal.Code
	}
	if auditErr := h.opts.Store.AppendAudit(r.Context(), store.AuditRecord{
		At: now.UTC(), Actor: "agent", Action: "uv.unblock_failed", Target: node.ID,
		DetailJSON: fmt.Sprintf(`{"code":%q}`, code),
	}); auditErr != nil {
		h.opts.Logger.Warn("unblock: audit the failure", "uv", node.ID, "err", auditErr)
	}
}

func (h *Handler) writeUnblocked(w http.ResponseWriter, node store.UV, notice string) {
	writeJSON(w, http.StatusOK, proto.UnblockResponse{
		APIVersion: proto.APIVersion,
		Node:       proto.Node{ID: node.ID, State: string(store.StateActive), Domain: node.Domain},
		Notice:     notice,
	})
}

func returnRelocateRequired(w http.ResponseWriter, storedDomain, requestedDomain string) {
	writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict,
		proto.CodeRelocateRequired, "Conflict",
		fmt.Sprintf("this node is registered as %s, the request names %s; use `uv migrate --relocate`",
			storedDomain, requestedDomain)))
}
