package admin

import (
	"context"
	"fmt"

	"github.com/shogun-kub/termit/internal/mn/mesh"
)

// EpochOutcome is what `epoch rotate` reports: the epoch the mesh left, the one it moved to, how many rows
// were re-keyed, and the peers that had to answer before it could happen.
type EpochOutcome struct {
	From    int      `json:"from"`
	To      int      `json:"to"`
	Nodes   int64    `json:"nodes"`
	Peers   []string `json:"peers,omitempty"`
	Journal int64    `json:"journal_seq"`
}

// rotateEpoch moves every pair credential in the mesh to the next epoch. It is the one operation that re-keys
// everything at once, so it is gated on the mesh being whole: every configured peer has to answer with the
// epoch this server is at. A peer that cannot be reached — or one already ahead — stops the rotation with
// nothing changed, because a bump on half the mesh leaves nodes talking to peers that derive other values
// while every check still passes (IMPLEMENTATION_PLAN.md §5.2.1).
func (s *Service) rotateEpoch(ctx context.Context) (any, *Error) {
	from, err := s.Store.Epoch(ctx)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	if from < 1 {
		return nil, &Error{Code: CodeConflict, Detail: "nothing is enrolled here, so there is nothing to re-key"}
	}
	mnID, err := s.Store.MeshID(ctx)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	answered := make([]string, 0, len(s.Mesh.Peers))
	for _, cfg := range s.Mesh.Peers {
		peer, err := mesh.NewPeer(cfg, s.Master, mesh.AdminRequestTimeout)
		if err != nil {
			return nil, &Error{Code: CodeInternal, Detail: err.Error()}
		}
		state, err := peer.State(ctx)
		if err != nil {
			return nil, &Error{Code: CodeConflict, Detail: fmt.Sprintf(
				"%s did not answer (%v); a rotation needs the whole mesh, so nothing was changed", cfg.Name, err)}
		}
		switch {
		case state.Epoch > from:
			return nil, &Error{Code: CodeConflict, Detail: fmt.Sprintf(
				"%s is at epoch %d and this server at %d; pull from it or rotate there, and nothing was changed",
				cfg.Name, state.Epoch, from)}
		case state.Epoch < from:
			// Behind is fine: the entry travels on the journal like every other change, and until it lands
			// the pair credential takes the lower of the two epochs, which is the number already in use.
			answered = append(answered, fmt.Sprintf("%s (at %d)", cfg.Name, state.Epoch))
		default:
			answered = append(answered, cfg.Name)
		}
	}
	to := from + 1
	changed, err := s.Store.RotateEpoch(ctx, to, s.Now().UTC())
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	seq, err := s.Store.LastSeq(ctx, mnID)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	return EpochOutcome{From: from, To: to, Nodes: changed, Peers: answered, Journal: seq}, nil
}
