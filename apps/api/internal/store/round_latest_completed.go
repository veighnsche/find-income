package store

import (
	"context"
	"database/sql"
	"errors"
)

// LatestCompletedRound returns one owner's latest saved supported result,
// including delivery work that recorded a partial result before failing.
// Timestamp ties are resolved by creation time and then stable round ID.
func (s *Store) LatestCompletedRound(ctx context.Context, actor Actor, outcome string) (Round, error) {
	if !ownerRoundActor(actor) || !supportedLatestOutcome(outcome) {
		return Round{}, ErrInvalid
	}
	query := `SELECT ` + roundColumns + ` FROM rounds WHERE actor_kind=? AND actor_id=? AND
		(state='completed' OR (state='failed' AND (outcome='research_run' OR (outcome='deliver' AND deliverable_status='submission_unverified' AND report_json IS NOT NULL))))`
	args := []any{actor.Kind, actor.ID}
	if outcome == "all" {
		query += ` AND outcome IN ('process_input','prepare','compare_offers','deliver','interview_prepare','interview_debrief')`
	} else {
		query += ` AND outcome=?`
		args = append(args, outcome)
	}
	query += ` ORDER BY completed_at DESC, created_at DESC, id DESC LIMIT 1`
	round, err := scanRound(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, ErrNotFound
	}
	return round, err
}

func supportedLatestOutcome(outcome string) bool {
	switch outcome {
	case "all", "process_input", "prepare", "compare_offers", "deliver", "interview_prepare", "interview_debrief", "process_replies", "research_run":
		return true
	default:
		return false
	}
}
