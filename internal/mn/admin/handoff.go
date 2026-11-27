package admin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/mesh"
	"github.com/shogun-kub/termit/internal/mn/secret"
)

// HandoffOutcome is what `home handoff` reports: the node, the server that took it, the term that now orders
// the row, and the agent key the operator has to install on the node.
type HandoffOutcome struct {
	UV   string `json:"uv"`
	Peer string `json:"peer"`
	// Home is the mesh id of the server that owns the node now.
	Home string `json:"home"`
	Term int64  `json:"term"`
	// AgentKey is shown once. The node's old key stops working at the moment the new home took the row.
	AgentKey string `json:"agent_key"`
}

type handoffArgs struct {
	UV    string `json:"uv"`
	Peer  string `json:"peer"`
	Force bool   `json:"force"`
}

// handoff moves one node to a peer in the mesh (IMPLEMENTATION_PLAN.md §5.2.1 decisions 5–7). The node's id
// and pair credentials survive; only its agent key changes, and the node is told out of band. The target
// must be fresh enough to take it — within mesh.lag_max journal entries of this server — unless the operator
// passes force, which is the one case where a node enrolled since the target's last pull has to re-enroll,
// because nothing ever taught the target that node's key.
func (s *Service) handoff(ctx context.Context, raw json.RawMessage) (any, *Error) {
	var args handoffArgs
	if err := json.Unmarshal(raw, &args); err != nil || args.UV == "" || args.Peer == "" {
		return nil, &Error{Code: CodeBadRequest, Detail: "a node and a peer are required"}
	}
	node, refused := s.resolveUV(ctx, args.UV)
	if refused != nil {
		return nil, refused
	}
	mnID, err := s.Store.MeshID(ctx)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	cfg, ok := s.peer(args.Peer)
	if !ok {
		names := make([]string, 0, len(s.Mesh.Peers))
		for _, peer := range s.Mesh.Peers {
			names = append(names, peer.Name)
		}
		return nil, &Error{Code: CodeBadRequest,
			Detail: fmt.Sprintf("no peer %q in mesh.peers (configured: %v)", args.Peer, names)}
	}
	peer, err := mesh.NewPeer(cfg, s.Master, 0)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}

	// Decision 7: a target that has not caught up with this server cannot take a node — it does not have
	// the row, so it could not take it over. The distance is what the target says it applied.
	state, err := peer.State(ctx)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: fmt.Sprintf("cannot reach %s: %v", cfg.Name, err)}
	}
	if state.MNID == "" {
		return nil, &Error{Code: CodeInternal, Detail: fmt.Sprintf("%s did not name itself", cfg.Name)}
	}
	last, err := s.Store.LastSeq(ctx, mnID)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	if behind := last - state.Applied[mnID]; behind > int64(s.Mesh.LagLimit()) && !args.Force {
		return nil, &Error{Code: CodeConflict, Detail: fmt.Sprintf(
			"%s is %d entries behind (%d of %d); wait for its next pull, or pass --force and accept that a node "+
				"enrolled since %d has to re-enroll", cfg.Name, behind, state.Applied[mnID], last, state.Applied[mnID])}
	}

	// The target is the authority for the new term and the new key: it writes the row it is about to own.
	answer, err := peer.Handoff(ctx, node.ID, mnID)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: fmt.Sprintf("%s refused the handoff: %v", cfg.Name, err)}
	}
	sealed, err := secret.Seal(s.Master, answer.AgentKey)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	// This server gives the row up: from here it is a replica of a node it does not home, and the entry that
	// says so is the target's, not this one's.
	if err := s.Store.HandoffOut(ctx, node.ID, state.MNID, answer.Term,
		secret.Digest(answer.AgentKey), sealed); err != nil {
		return nil, &Error{Code: CodeInternal, Detail: fmt.Sprintf(
			"%s took the node but this server could not record it: %v", cfg.Name, err)}
	}
	return HandoffOutcome{
		UV: node.ID, Peer: cfg.Name, Home: state.MNID, Term: answer.Term, AgentKey: answer.AgentKey,
	}, nil
}

// peer finds a configured peer by name.
func (s *Service) peer(name string) (config.Peer, bool) {
	for _, peer := range s.Mesh.Peers {
		if peer.Name == name {
			return peer, true
		}
	}
	return config.Peer{}, false
}
