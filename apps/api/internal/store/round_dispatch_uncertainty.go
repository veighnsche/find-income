package store

import (
	"context"
	"strings"
)

// MarkRoundDispatchUncertain fences all further work when a remote dispatch
// may have started but its identity or terminal outcome cannot be established.
// The attempt stays reconcilable; no allowance is returned.
func (s *Store) MarkRoundDispatchUncertain(ctx context.Context, roundID, attemptID string,
	generation int64, code string) (Round, error) {
	if roundID == "" || attemptID == "" || generation < 1 || code == "" || len(code) > 100 ||
		strings.TrimSpace(code) != code {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State != RoundRunning || r.Generation != generation {
		return Round{}, ErrFenced
	}
	if err := remoteAttemptTx(ctx, tx, roundID, attemptID, generation); err != nil {
		return Round{}, err
	}
	now := utcNow()
	res, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='uncertain',error_code=?,updated_at=?
	  WHERE id=? AND round_id=? AND generation=? AND state='dispatched' AND operation=?`,
		code, now, attemptID, roundID, generation, RoundCodexTurn)
	if err != nil {
		return Round{}, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return Round{}, err
	}
	if count != 1 {
		return Round{}, ErrFenced
	}
	if _, err = tx.ExecContext(ctx, `UPDATE rounds SET state='paused',generation=generation+1,
	  revision=revision+1,reconciliation_required=1,stop_reason=?,updated_at=?
	  WHERE id=? AND state='running' AND generation=?`, code, now, roundID, generation); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "round-executor"}, "round.dispatch_uncertain", roundID); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}
