package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (s *Store) roundWriter(ctx context.Context, id string) (*sql.Tx, Round, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, Round{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE rounds SET revision=revision WHERE id=?`, id)
	if err != nil {
		tx.Rollback()
		return nil, Round{}, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		tx.Rollback()
		if err != nil {
			return nil, Round{}, err
		}
		return nil, Round{}, ErrNotFound
	}
	r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		tx.Rollback()
		return nil, Round{}, err
	}
	return tx, r, nil
}

func (s *Store) ActivateRound(ctx context.Context, actor Actor, id string) (Round, error) {
	if !ownerRoundActor(actor) {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State != RoundQueued {
		return Round{}, ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		return Round{}, ErrExpired
	}
	_, err = tx.ExecContext(ctx, `UPDATE rounds SET state='running',revision=revision+1,updated_at=? WHERE id=?`, utcNow(), id)
	if err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.activate", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

// StopRound fences every previous generation before the controller signals
// cancellation. Dispatched attempts become uncertain even if cancellation
// later acknowledges; remote completion still needs observed reconciliation.
func (s *Store) StopRound(ctx context.Context, actor Actor, id string) (Round, []RoundAttempt, error) {
	if !ownerRoundActor(actor) {
		return Round{}, nil, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, nil, err
	}
	defer tx.Rollback()
	if r.State == RoundPaused {
		now := utcNow()
		if _, err := tx.ExecContext(ctx, `UPDATE rounds SET generation=generation+1,
  revision=revision+1,reconciliation_required=1,stop_reason='owner_stopped',updated_at=?
  WHERE id=?`, now, id); err != nil {
			return Round{}, nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE round_reconciliation_checks SET state='fenced',finished_at=?
  WHERE round_id=? AND state='pending'`, now, id); err != nil {
			return Round{}, nil, err
		}
		if err := writeRoundAudit(ctx, tx, actor, "round.stop", id); err != nil {
			return Round{}, nil, err
		}
		attempts, err := uncertainAttemptsTx(ctx, tx, id)
		if err != nil {
			return Round{}, nil, err
		}
		r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
		if err != nil {
			return Round{}, nil, err
		}
		return r, attempts, tx.Commit()
	}
	if r.State == RoundStopping {
		attempts, err := uncertainAttemptsTx(ctx, tx, id)
		return r, attempts, err
	}
	if r.State != RoundRunning && r.State != RoundQueued && r.State != RoundAwaitingInput {
		return Round{}, nil, ErrFenced
	}
	now := utcNow()
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='stopping',revision=revision+1,
  generation=generation+1,reconciliation_required=1,stop_reason='owner_stopped',updated_at=? WHERE id=?`, now, id); err != nil {
		return Round{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='uncertain',updated_at=?
  WHERE round_id=? AND state='dispatched'`, now, id); err != nil {
		return Round{}, nil, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.stop", id); err != nil {
		return Round{}, nil, err
	}
	attempts, err := uncertainAttemptsTx(ctx, tx, id)
	if err != nil {
		return Round{}, nil, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Round{}, nil, err
	}
	return r, attempts, nil
}

func (s *Store) RecordCancelSignal(ctx context.Context, roundID, attemptID string, signalErr error) error {
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.State != RoundStopping && r.State != RoundPaused {
		return ErrFenced
	}
	message := ""
	if signalErr != nil {
		message = "cancel_failed"
	}
	result, err := tx.ExecContext(ctx, `UPDATE round_attempts SET cancel_requested_at=?,cancel_error=?,updated_at=?
  WHERE id=? AND round_id=? AND state='uncertain'`, utcNow(), message, utcNow(), attemptID, roundID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

func (s *Store) PauseStoppedRound(ctx context.Context, actor Actor, id string) (Round, error) {
	if !ownerRoundActor(actor) {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State != RoundStopping {
		return Round{}, ErrFenced
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='paused',revision=revision+1,updated_at=? WHERE id=?`, utcNow(), id); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.paused", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

func (s *Store) ResumeRound(ctx context.Context, actor Actor, id string, expectedGeneration int64) (Round, error) {
	if !ownerRoundActor(actor) || expectedGeneration < 1 {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State != RoundPaused {
		return Round{}, ErrFenced
	}
	if r.Generation != expectedGeneration {
		return Round{}, ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		return Round{}, ErrExpired
	}
	var uncertain int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts WHERE round_id=?
  AND state='uncertain'`, id).Scan(&uncertain); err != nil {
		return Round{}, err
	}
	if uncertain != 0 {
		return Round{}, ErrUncertain
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='running',revision=revision+1,
  generation=generation+1,reconciliation_required=0,stop_reason='',updated_at=? WHERE id=?`, utcNow(), id); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.resume", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

// ResumeFailedRound revives a failed round for one explicit resume: the
// run re-conducts from its durable cursor under the original bounds and
// overwrites the terminal report, instead of discarding stop-time work.
// Completed rounds never revive; crashed/expired runs commission anew.
// Guards mirror ResumeRound; completed_at clears so the revived round
// reads running everywhere.
func (s *Store) ResumeFailedRound(ctx context.Context, actor Actor, id string, expectedGeneration int64) (Round, error) {
	if !ownerRoundActor(actor) || expectedGeneration < 1 {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State != RoundFailed {
		return Round{}, ErrFenced
	}
	if r.Generation != expectedGeneration {
		return Round{}, ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		return Round{}, ErrExpired
	}
	var uncertain int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts WHERE round_id=?
  AND state='uncertain'`, id).Scan(&uncertain); err != nil {
		return Round{}, err
	}
	if uncertain != 0 {
		return Round{}, ErrUncertain
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='running',revision=revision+1,
  generation=generation+1,reconciliation_required=0,stop_reason='',completed_at=NULL,updated_at=? WHERE id=?`, utcNow(), id); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.resume_failed", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

// AwaitRoundInput fences worker work while a missing owner-held fact is
// requested. An in-flight dispatch must first be stopped and reconciled.
func (s *Store) AwaitRoundInput(ctx context.Context, actor Actor, id string, unresolved json.RawMessage) (Round, error) {
	if !requiredActor(actor) || !validRoundJSON(unresolved, 64*1024) {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State != RoundRunning {
		return Round{}, ErrFenced
	}
	var inflight int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts
  WHERE round_id=? AND state IN ('dispatched','uncertain')`, id).Scan(&inflight); err != nil {
		return Round{}, err
	}
	if inflight != 0 {
		return Round{}, ErrUncertain
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='awaiting_input',generation=generation+1,
  revision=revision+1,unresolved_json=?,updated_at=? WHERE id=?`, string(unresolved), utcNow(), id); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.await_input", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

func (s *Store) ContinueRound(ctx context.Context, actor Actor, id string) (Round, error) {
	if !ownerRoundActor(actor) {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if err := requireRoundState(r, RoundAwaitingInput); err != nil {
		return Round{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='running',generation=generation+1,
  revision=revision+1,unresolved_json='[]',updated_at=? WHERE id=?`, utcNow(), id); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.continue", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

func validRoundJSON(value json.RawMessage, max int) bool {
	return len(value) != 0 && len(value) <= max && json.Valid(value)
}

type RoundProgress struct {
	Step       string
	Cursor     json.RawMessage
	Unresolved json.RawMessage
	Report     json.RawMessage
}

func (s *Store) SaveRoundProgress(ctx context.Context, actor Actor, id string, expectedRevision int64, progress RoundProgress) (Round, error) {
	if !requiredActor(actor) || expectedRevision < 1 || len(progress.Step) > 100 ||
		!validRoundJSON(progress.Cursor, 64*1024) || !validRoundJSON(progress.Unresolved, 64*1024) ||
		!validRoundJSON(progress.Report, 256*1024) {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.Revision != expectedRevision {
		return Round{}, ErrConflict
	}
	if r.State != RoundRunning {
		return Round{}, ErrFenced
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET step=?,cursor_json=?,unresolved_json=?,report_json=?,
  revision=revision+1,updated_at=? WHERE id=?`, progress.Step, string(progress.Cursor),
		string(progress.Unresolved), string(progress.Report), utcNow(), id); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.progress", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

func (s *Store) FinishRound(ctx context.Context, actor Actor, id string, terminal RoundState, reason, deliverable string, report json.RawMessage) (Round, error) {
	if !requiredActor(actor) || (terminal != RoundCompleted && terminal != RoundFailed) ||
		strings.TrimSpace(reason) == "" || len(reason) > 100 ||
		strings.TrimSpace(deliverable) == "" || len(deliverable) > 100 || !validRoundJSON(report, 256*1024) {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, id)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State != RoundRunning && r.State != RoundPaused {
		return Round{}, ErrFenced
	}
	if r.State == RoundRunning {
		var inflight int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts
  WHERE round_id=? AND state IN ('dispatched','uncertain')`, id).Scan(&inflight); err != nil {
			return Round{}, err
		}
		if inflight != 0 {
			return Round{}, ErrUncertain
		}
	}
	now := utcNow()
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state=?,revision=revision+1,generation=generation+1,
  stop_reason=?,deliverable_status=?,report_json=?,updated_at=?,completed_at=? WHERE id=?`,
		terminal, reason, deliverable, string(report), now, now, id); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.finish", id); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}

func (s *Store) RoundResults(ctx context.Context, id string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT result_json FROM round_results WHERE round_id=? ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []json.RawMessage
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		results = append(results, json.RawMessage(raw))
	}
	return results, rows.Err()
}

func requireRoundState(r Round, state RoundState) error {
	if r.State != state {
		return ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		return fmt.Errorf("%w: %s", ErrExpired, r.ID)
	}
	return nil
}

// ExpireRound fences work whose saved deadline has passed. A terminal failed
// state releases the single active slot; uncertain dispatch identities and
// any later evidence remain available for audit and separate reconciliation.
func (s *Store) ExpireRound(ctx context.Context, roundID string) (Round, error) {
	if roundID == "" {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.State == RoundFailed && r.StopReason == "deadline_reached" {
		return r, nil
	}
	if r.State == RoundCompleted || r.State == RoundFailed {
		return Round{}, ErrFenced
	}
	if time.Now().Before(r.Deadline) {
		return Round{}, ErrFenced
	}
	now := utcNow()
	if _, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='uncertain',updated_at=?
	  WHERE round_id=? AND state='dispatched'`, now, roundID); err != nil {
		return Round{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='cancelled',updated_at=?,finished_at=?,error_code='deadline_reached'
	  WHERE round_id=? AND state='reserved'`, now, now, roundID); err != nil {
		return Round{}, err
	}
	var uncertain int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts WHERE round_id=? AND state='uncertain'`, roundID).Scan(&uncertain); err != nil {
		return Round{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE round_reconciliation_checks SET state='fenced',finished_at=?
	  WHERE round_id=? AND state='pending'`, now, roundID); err != nil {
		return Round{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='failed',generation=generation+1,revision=revision+1,
	  reconciliation_required=?,stop_reason='deadline_reached',deliverable_status='partial',
	  updated_at=?,completed_at=? WHERE id=?`, uncertain > 0, now, now, roundID); err != nil {
		return Round{}, err
	}
	if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "round-deadline"}, "round.deadline_reached", roundID); err != nil {
		return Round{}, err
	}
	r, err = scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
	if err != nil {
		return Round{}, err
	}
	return r, tx.Commit()
}
