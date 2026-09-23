package store

import (
	"context"
	"database/sql"
	"errors"
)

// LatestCompletedRound returns one owner's latest completed round for a supported result view.
// Timestamp ties are resolved by creation time and then stable round ID.
func (s *Store) LatestCompletedRound(ctx context.Context, actor Actor, outcome string) (Round, error) {
	if !ownerRoundActor(actor) || outcome != "discover" && outcome != "compare_offers" {
		return Round{}, ErrInvalid
	}
	round, err := scanRound(s.db.QueryRowContext(ctx, `SELECT `+roundColumns+`
  FROM rounds WHERE actor_kind=? AND actor_id=? AND outcome=? AND state='completed'
  ORDER BY completed_at DESC, created_at DESC, id DESC LIMIT 1`, actor.Kind, actor.ID, outcome))
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, ErrNotFound
	}
	return round, err
}
