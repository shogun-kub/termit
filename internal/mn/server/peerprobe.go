package server

import (
	"encoding/json"
	"net/http"

	"github.com/shogun-kub/termit/internal/mn/probe"
	"github.com/shogun-kub/termit/internal/proto"
)

// peerProbe answers another management server's question about one node: it runs this server's own prober
// against the target and returns what it saw, with the time it saw it (docs/IMPLEMENTATION_PLAN.md §5.2.4).
//
// It never reads or writes the registry: the asking server decides and owns the row, and this one only
// observes. An answer that cannot be produced is an empty one — 503 — so the asking side reads "no verdict"
// rather than a failure of the node.
func (h *Handler) peerProbe(w http.ResponseWriter, r *http.Request) {
	if !h.peerAllowed(w, r) {
		return
	}
	if h.opts.Prober == nil {
		writeProblem(w, http.StatusServiceUnavailable, proto.NewProblem(http.StatusServiceUnavailable,
			proto.CodeInternal, "Service Unavailable", "this server has no prober to answer with"))
		return
	}
	var request proto.ProbeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, proto.NewProblem(http.StatusBadRequest, "", "Bad Request",
			"the probe request is not valid JSON"))
		return
	}
	target := probe.Target{
		ID:               request.Target.ID,
		Address:          request.Target.Address,
		Port:             request.Target.Port,
		ServerName:       request.Target.ServerName,
		Path:             request.Target.Path,
		Mode:             request.Target.Mode,
		UUID:             request.Target.UUID,
		PinnedCertSHA256: request.Target.PinnedCertSHA256,
		Transport:        request.Target.Transport,
	}
	result, err := h.opts.Prober(r.Context(), target)
	if err != nil {
		// The prober itself failed, which is this server's problem, not the node's.
		writeProblem(w, http.StatusServiceUnavailable, proto.NewProblem(http.StatusServiceUnavailable,
			proto.CodeInternal, "Service Unavailable", "this server could not run the check"))
		return
	}
	writeJSON(w, http.StatusOK, proto.ProbeAnswer{
		OK:        result.OK,
		Class:     string(result.Class),
		Error:     errorText(result.Err),
		LatencyMS: int(result.Latency.Milliseconds()),
		At:        h.opts.Now().UTC(),
		Vantage:   h.opts.Domain,
	})
}

// errorText is the message without the error type, and empty when there is none.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
