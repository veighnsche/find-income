package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type RoundReconciliationCheck struct {
	ID         string
	RoundID    string
	AttemptID  string
	Generation int64
}

// BeginRoundReconciliation charges one request and one tool operation before
// an external read. Each new check is charged, including after a crash.
func (s *Store) BeginRoundReconciliation(ctx context.Context, roundID, attemptID string, expectedGeneration int64) (RoundReconciliationCheck, error) {
	if expectedGeneration < 1 {
		return RoundReconciliationCheck{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return RoundReconciliationCheck{}, err
	}
	defer tx.Rollback()
	if r.State != RoundPaused || r.Generation != expectedGeneration {
		return RoundReconciliationCheck{}, ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		return RoundReconciliationCheck{}, ErrExpired
	}
	var state RoundAttemptState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM round_attempts WHERE id=? AND round_id=?`,
		attemptID, roundID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RoundReconciliationCheck{}, ErrNotFound
		}
		return RoundReconciliationCheck{}, err
	}
	if state != AttemptUncertain {
		return RoundReconciliationCheck{}, ErrFenced
	}
	updated, err := tx.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used+1,
  tools_used=tools_used+1,revision=revision+1,updated_at=? WHERE id=?
  AND requests_used+1<=request_limit AND tools_used+1<=tool_limit`, utcNow(), roundID)
	if err != nil {
		return RoundReconciliationCheck{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return RoundReconciliationCheck{}, err
	}
	if count != 1 {
		return RoundReconciliationCheck{}, ErrAllowance
	}
	id, err := randomID()
	if err != nil {
		return RoundReconciliationCheck{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO round_reconciliation_checks
  (id,round_id,attempt_id,control_generation,state,created_at)
  VALUES (?,?,?,?,'pending',?)`, id, roundID, attemptID, r.Generation, utcNow()); err != nil {
		return RoundReconciliationCheck{}, err
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "round-reconciliation"}, "round.reconcile.reserve", roundID); err != nil {
		return RoundReconciliationCheck{}, err
	}
	return RoundReconciliationCheck{ID: id, RoundID: roundID, AttemptID: attemptID, Generation: r.Generation}, tx.Commit()
}

// observed_success and observed_failure record the remote event. They do not
// imply that a stopped or stale attempt mutated current domain records.
func (s *Store) FinishRoundReconciliation(ctx context.Context, check RoundReconciliationCheck, observed RoundAttemptState, evidence json.RawMessage) (RoundAttempt, error) {
	if check.ID == "" || check.Generation < 1 || !validRoundJSON(evidence, 256*1024) ||
		observed != AttemptObservedSuccess && observed != AttemptObservedFailure && observed != AttemptUncertain {
		return RoundAttempt{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, check.RoundID)
	if err != nil {
		return RoundAttempt{}, err
	}
	defer tx.Rollback()
	if r.State != RoundPaused || r.Generation != check.Generation {
		return RoundAttempt{}, ErrFenced
	}
	checkState := "resolved"
	if observed == AttemptUncertain {
		checkState = "unknown"
	}
	updated, err := tx.ExecContext(ctx, `UPDATE round_reconciliation_checks SET state=?,evidence_json=?,finished_at=?
  WHERE id=? AND round_id=? AND attempt_id=? AND control_generation=? AND state='pending'`, checkState,
		string(evidence), utcNow(), check.ID, check.RoundID, check.AttemptID, check.Generation)
	if err != nil {
		return RoundAttempt{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return RoundAttempt{}, err
	}
	if count != 1 {
		return RoundAttempt{}, ErrFenced
	}
	if observed != AttemptUncertain {
		updated, err = tx.ExecContext(ctx, `UPDATE round_attempts SET state=?,updated_at=?,finished_at=?
  WHERE id=? AND round_id=? AND state='uncertain'`, observed, utcNow(), utcNow(), check.AttemptID, check.RoundID)
		if err != nil {
			return RoundAttempt{}, err
		}
		count, err = updated.RowsAffected()
		if err != nil {
			return RoundAttempt{}, err
		}
		if count != 1 {
			return RoundAttempt{}, ErrFenced
		}
	}
	a, err := scanRoundAttempt(tx.QueryRowContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts WHERE id=?`, check.AttemptID))
	if err != nil {
		return RoundAttempt{}, err
	}
	return a, tx.Commit()
}
