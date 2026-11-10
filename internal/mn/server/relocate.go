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

// relocate serves POST /api/v1/agent/relocate: Scenario C. The same node presents itself at a new
// host: the request has to come from an address the new domain resolves to, and the box has to serve
// the claimed identity, or the record is left exactly as it was.
//
// The node may be blocked: it is a valid agent key that is asking to prove it moved, and the checks
// are the same ones an unblock runs, against the new address. A node that has not been admitted yet
// cannot move itself in, and a removed node cannot come back this way.
func (h *Handler) relocate(w http.ResponseWriter, r *http.Request) {
	node, refused := h.authenticateAgentWith(w, r, true)
	if refused {
		return
	}

	var req proto.RelocateRequest
	if r.ContentLength > 0 {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
				proto.CodeURLInvalid, "Bad Request", "the relocate body is not valid: "+err.Error()))
			return
		}
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	req.BasicVLESSURL = strings.TrimSpace(req.BasicVLESSURL)
	if req.Domain == "" || req.BasicVLESSURL == "" {
		writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
			proto.CodeURLInvalid, "Bad Request", "domain and basic_vless_url are required"))
		return
	}

	profile, err := proto.ParseVLESSUrl(req.BasicVLESSURL)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
			proto.CodeURLInvalid, "Bad Request", err.Error()))
		return
	}
	if !strings.EqualFold(profile.Domain, req.Domain) {
		writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest,
			proto.CodeURLInvalid, "Bad Request",
			fmt.Sprintf("the URL names %q, the request names %q", profile.Domain, req.Domain)))
		return
	}
	if strings.EqualFold(req.Domain, node.Domain) {
		// A new identity on the same host is Scenario B: the box there is a rebuild, not a move, and
		// `termit-uv unblock` already updates the endpoint.
		writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict,
			proto.CodeConflict, "Conflict",
			fmt.Sprintf("this node already answers on %s; use `termit-uv unblock` to refresh its identity", node.Domain)))
		return
	}

	// The domain is unique across the mesh, and the check has to happen before anything is verified:
	// moving a node onto another node's name would break both.
	if other, err := h.opts.Store.UVByDomain(r.Context(), req.Domain); err == nil && other.ID != node.ID {
		writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict,
			proto.CodeConflict, "Conflict", fmt.Sprintf("the domain %s belongs to another node", req.Domain)))
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.opts.Logger.Error("relocate: look up the new domain", "uv", node.ID, "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not check the new domain"))
		return
	}

	outcome := sched.Transition(sched.State(node.State), sched.EventRelocate)
	if node.State != store.StateActive && !outcome.Changed {
		writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict,
			proto.CodeConflict, "Conflict", fmt.Sprintf("this node is %s; %s", node.State, outcome.Reason)))
		return
	}

	now := h.opts.Now().UTC()
	// The claimed identity is what gets verified: a blocked node has no probe credential in its config
	// until it syncs, and the unblock path uses the self credential for the same reason. DNS, the
	// decoy and the tunnel must all work from the address that sent this request.
	pin, err := h.opts.Enrollment.Verify(r.Context(), profile, sourceIP(r))
	if err != nil {
		h.recordRelocateFailure(r, node, err, now)
		var refusal *enroll.Error
		if errors.As(err, &refusal) {
			writeProblem(w, refusal.Status, proto.NewProblem(refusal.Status, refusal.Code, titleFor(refusal.Status), refusal.Detail))
			return
		}
		h.opts.Logger.Error("relocate: verification failed", "uv", node.ID, "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not verify this node"))
		return
	}

	profileJSON, err := json.Marshal(profile)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not store the new profile"))
		return
	}
	if err := h.opts.Store.RelocateUV(r.Context(), node.ID, req.Domain, profile.UUID, string(profileJSON), pin); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeProblem(w, http.StatusConflict, proto.NewProblem(http.StatusConflict,
				proto.CodeConflict, "Conflict", fmt.Sprintf("the domain %s belongs to another node", req.Domain)))
			return
		}
		h.opts.Logger.Error("relocate: store the new endpoint", "uv", node.ID, "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not store the new endpoint"))
		return
	}

	if outcome.Changed {
		if _, err := h.opts.Store.SetUVState(r.Context(), node.ID, store.StateActive, outcome.Reason, "agent", now); err != nil {
			h.opts.Logger.Error("relocate: change state", "uv", node.ID, "err", err)
			writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
				proto.CodeInternal, "Internal Server Error", "the server could not update the node"))
			return
		}
		if err := h.opts.Store.SetFailedCycles(r.Context(), node.ID, 0); err != nil {
			h.opts.Logger.Warn("relocate: reset the failed cycles", "uv", node.ID, "err", err)
		}
	}
	if err := h.opts.Store.AppendAudit(r.Context(), store.AuditRecord{
		At: now, Actor: "agent", Action: "uv.relocate", Target: node.ID,
		DetailJSON: fmt.Sprintf(`{"from":%q,"to":%q,"state":%q}`, node.Domain, req.Domain, string(outcome.Next)),
	}); err != nil {
		h.opts.Logger.Warn("relocate: audit", "uv", node.ID, "err", err)
	}

	h.opts.Logger.Info("node relocated", "uv", node.ID, "from", node.Domain, "to", req.Domain, "ip", sourceIP(r))
	writeJSON(w, http.StatusOK, proto.RelocateResponse{
		APIVersion: proto.APIVersion,
		Node:       proto.Node{ID: node.ID, Domain: req.Domain, State: string(outcome.Next)},
		Notice:     fmt.Sprintf("this node now answers on %s; peers pick it up at their next sync", req.Domain),
	})
}

// recordRelocateFailure keeps the evidence of a refused move. The record is untouched: the node is
// still where it was, with its old profile and pin.
func (h *Handler) recordRelocateFailure(r *http.Request, node store.UV, err error, now time.Time) {
	code := proto.CodeCheckFailed
	var refusal *enroll.Error
	if errors.As(err, &refusal) {
		code = refusal.Code
	}
	if auditErr := h.opts.Store.AppendAudit(r.Context(), store.AuditRecord{
		At: now.UTC(), Actor: "agent", Action: "uv.relocate_failed", Target: node.ID,
		DetailJSON: fmt.Sprintf(`{"code":%q}`, code),
	}); auditErr != nil {
		h.opts.Logger.Warn("relocate: audit the failure", "uv", node.ID, "err", auditErr)
	}
}
