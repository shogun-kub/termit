package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/shogun-kub/termit/internal/mn/store"
)

// meshShape is the digest of who is in the mesh and in what shape it is. Anything that changes what a
// peer receives must enter it — a join, a block, a removal, a move to another domain, a new credential
// epoch — and nothing else may, or every node would reconcile on every poll.
func meshShape(nodes []store.UV) string {
	sorted := make([]store.UV, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	digest := sha256.New()
	for _, n := range sorted {
		fmt.Fprintf(digest, "%s|%s|%s|%s|%d\n", n.ID, n.Domain, n.State, n.ProfileJSON, n.CredentialEpoch)
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

// meshGeneration is meshShape over the whole registry.
func (h *Handler) meshGeneration(ctx context.Context) (string, error) {
	nodes, err := h.opts.Store.ListUVs(ctx, store.UVFilter{})
	if err != nil {
		return "", err
	}
	return meshShape(nodes), nil
}
