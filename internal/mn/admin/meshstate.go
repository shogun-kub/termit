package admin

import (
	"context"
	"errors"
	"strconv"

	"github.com/shogun-kub/termit/internal/mn/mesh"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// MeshState is the replication block of `termit-mn status`: this server's identity in the journal, whether
// it holds the master key, and how far behind each configured peer it is.
type MeshState struct {
	MNID string `json:"mn_id"`
	// HasMasterKey is whether the process holds a key at all. A replica that is not expected to take over
	// runs without one, a standby keeps it, and this line is how an operator tells which one they have.
	HasMasterKey bool        `json:"has_master_key"`
	Peers        []PeerState `json:"peers"`
}

// PeerState is one peer as of the last pull. Known is false until a snapshot has named the peer's mesh id;
// Behind is how many of that peer's entries this server has not applied.
type PeerState struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Known   bool   `json:"known"`
	MNID    string `json:"mn_id,omitempty"`
	Cursor  int64  `json:"cursor"`
	LastSeq int64  `json:"last_seq"`
	Behind  int64  `json:"behind"`
}

// meshState answers with the replication block. A peer that was never reached is reported as unknown rather
// than as zero lag: "we have not talked to it" and "we are up to date" are different answers.
func (s *Service) meshState(ctx context.Context) (any, *Error) {
	mnID, err := s.Store.MeshID(ctx)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Detail: err.Error()}
	}
	state := MeshState{MNID: mnID, HasMasterKey: len(s.Master) > 0}
	for _, peer := range s.Mesh.Peers {
		entry := PeerState{Name: peer.Name, URL: peer.URL}
		id, err := s.Store.GetSetting(ctx, mesh.PeerIDKey(peer.Name))
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return nil, &Error{Code: CodeInternal, Detail: err.Error()}
		case id != "":
			entry.Known, entry.MNID = true, id
			if entry.Cursor, err = s.Store.Cursor(ctx, id); err != nil {
				return nil, &Error{Code: CodeInternal, Detail: err.Error()}
			}
			if raw, err := s.Store.GetSetting(ctx, mesh.PeerSeqKey(peer.Name)); err == nil {
				if seq, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
					entry.LastSeq = seq
				}
			}
			if entry.LastSeq > entry.Cursor {
				entry.Behind = entry.LastSeq - entry.Cursor
			}
		}
		state.Peers = append(state.Peers, entry)
	}
	return state, nil
}
