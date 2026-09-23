package store

import (
	"context"
	"database/sql"
	"errors"
)

// OwnerInstruction reads one saved instruction for its owner, including a
// revoked record so a caller can distinguish history from current authority.
func (s *Store) OwnerInstruction(ctx context.Context, owner Actor, id string) (OwnerInstruction, error) {
	if !ownerRoundActor(owner) || id == "" {
		return OwnerInstruction{}, ErrInvalid
	}
	value, err := scanOwnerInstruction(s.db.QueryRowContext(ctx,
		`SELECT `+ownerInstructionColumns+` FROM owner_instructions WHERE id=? AND actor_id=?`, id, owner.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerInstruction{}, ErrNotFound
	}
	return value, err
}
