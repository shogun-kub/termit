package store

import (
	"context"
	"fmt"
)

// SetUVCertPin remembers the leaf certificate a node presents, so every later probe pins the same one
// instead of trusting a root store the prober does not use.
func (s *Store) SetUVCertPin(ctx context.Context, id, fingerprint string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE uv SET cert_sha256 = ? WHERE id = ?`, fingerprint, id); err != nil {
		return fmt.Errorf("store: set certificate pin for %s: %w", id, err)
	}
	return nil
}
