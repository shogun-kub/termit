package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/shogun-kub/termit/internal/mn/secret"
	"github.com/shogun-kub/termit/internal/mn/store"
	"github.com/shogun-kub/termit/internal/mn/sub"
	"github.com/shogun-kub/termit/internal/proto"
)

// subscription serves GET /sub/{token} and GET /sub/{token}/json.
//
// The token in the path is a capability: it names one node's list and nothing else. A wrong or unknown
// token gets the same answer, so a guesser learns nothing from the difference.
func (h *Handler) subscription(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	jsonFormat := strings.HasSuffix(r.URL.Path, "/json")

	if token == "" || !h.subs.allow(token) {
		writeProblem(w, http.StatusTooManyRequests, proto.NewProblem(http.StatusTooManyRequests,
			proto.CodeRateLimited, "Too Many Requests", "too many subscription requests; wait and retry"))
		return
	}

	owner, err := h.opts.Store.UVBySubTokenHash(r.Context(), secret.Digest(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, proto.NewProblem(http.StatusNotFound,
				"subscription_not_found", "Not Found", "no subscription for this token"))
			return
		}
		h.opts.Logger.Error("subscription lookup", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not read the subscription"))
		return
	}

	peers, err := h.peers(r)
	if err != nil {
		h.opts.Logger.Error("subscription peers", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not build the peer list"))
		return
	}

	var (
		body []byte
		kind string
	)
	if jsonFormat {
		body, err = sub.RenderJSON(peers, sub.Options{SubscriptionURL: h.subscriptionURL(token)})
		kind = "application/json; charset=utf-8"
	} else {
		var list string
		list, err = sub.RenderList(peers)
		body, kind = []byte(list), "text/plain; charset=utf-8"
	}
	if err != nil {
		h.opts.Logger.Error("subscription render", "err", err)
		writeProblem(w, http.StatusInternalServerError, proto.NewProblem(http.StatusInternalServerError,
			proto.CodeInternal, "Internal Server Error", "the server could not render the peer list"))
		return
	}

	etag := sub.ETag(body)
	w.Header().Set("ETag", etag)
	// A subscription is a capability, not public content: no shared cache may keep it.
	w.Header().Set("Cache-Control", "no-store")
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)

	h.opts.Logger.Debug("subscription served",
		"uv", owner.ID, "format", map[bool]string{true: "json", false: "list"}[jsonFormat], "peers", len(peers))
}

// peers is the peer list: every node a client may dial. A pending or blocked node is absent on
// purpose — the state machine's decision is what a subscription reflects.
func (h *Handler) peers(r *http.Request) ([]sub.Peer, error) {
	nodes, err := h.opts.Store.ListUVs(r.Context(), store.UVFilter{
		States: []store.State{store.StateActive, store.StateUnavailable},
	})
	if err != nil {
		return nil, err
	}
	out := make([]sub.Peer, 0, len(nodes))
	for _, node := range nodes {
		profile, err := profileOf(node)
		if err != nil {
			// A node whose record cannot be read must not poison the whole list.
			h.opts.Logger.Warn("skipping a node with an unreadable profile", "uv", node.ID, "err", err)
			continue
		}
		out = append(out, sub.Peer{
			ID:       node.ID,
			Label:    labelOf(node, profile),
			Domain:   node.Domain,
			UUID:     profile.UUID,
			Path:     profile.Path,
			Degraded: node.State == store.StateUnavailable,
		})
	}
	return out, nil
}

// profileOf decodes the profile stored at enrollment. The UUID in it is the node's self credential:
// it is what a peer needs, and it never leaves MN except through a subscription that names its owner.
func profileOf(node store.UV) (proto.Profile, error) {
	var profile proto.Profile
	if err := json.Unmarshal([]byte(node.ProfileJSON), &profile); err != nil {
		return proto.Profile{}, err
	}
	if profile.Domain == "" || profile.UUID == "" || profile.Path == "" {
		return proto.Profile{}, errors.New("incomplete profile")
	}
	return profile, nil
}

func labelOf(node store.UV, profile proto.Profile) string {
	if node.Label != "" {
		return node.Label
	}
	if profile.Label != "" {
		return profile.Label
	}
	return node.Domain
}

// subscriptionURL is what a client can paste back to find the profile again.
func (h *Handler) subscriptionURL(token string) string {
	scheme := "https"
	if h.opts.Domain == "" {
		return ""
	}
	return scheme + "://" + h.opts.Domain + "/sub/" + token
}
