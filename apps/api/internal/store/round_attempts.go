package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

type RoundAttemptState string

const (
	AttemptReserved        RoundAttemptState = "reserved"
	AttemptDispatched      RoundAttemptState = "dispatched"
	AttemptSucceeded       RoundAttemptState = "succeeded"
	AttemptFailed          RoundAttemptState = "failed"
	AttemptUncertain       RoundAttemptState = "uncertain"
	AttemptObservedSuccess RoundAttemptState = "observed_success"
	AttemptObservedFailure RoundAttemptState = "observed_failure"
	AttemptCancelled       RoundAttemptState = "cancelled"
)

type RoundAttemptInput struct {
	RequestKey      string
	Operation       string
	ResourceID      string
	Cost            RoundAllowance
	CursorAttemptID string
	BoundCapability string
}

type RoundAttempt struct {
	ID                string
	RoundID           string
	RequestKey        string
	Operation         string
	ResourceID        string
	Generation        int64
	State             RoundAttemptState
	Cost              RoundAllowance
	Result            json.RawMessage
	LateResult        json.RawMessage
	ErrorCode         string
	CancelRequestedAt string
	CancelError       string
	CreatedAt         string
	UpdatedAt         string
	DispatchedAt      string
	FinishedAt        string
}

const roundAttemptColumns = `id,round_id,request_key,operation,resource_id,generation,state,
  requests_reserved,items_reserved,tools_reserved,turns_reserved,result_json,late_result_json,
  error_code,cancel_requested_at,cancel_error,created_at,updated_at,dispatched_at,finished_at`

func scanRoundAttempt(row rowScanner) (RoundAttempt, error) {
	var a RoundAttempt
	var result, lateResult, cancelAt, dispatchedAt, finishedAt sql.NullString
	err := row.Scan(&a.ID, &a.RoundID, &a.RequestKey, &a.Operation, &a.ResourceID,
		&a.Generation, &a.State, &a.Cost.Requests, &a.Cost.Items, &a.Cost.Tools,
		&a.Cost.Turns, &result, &lateResult, &a.ErrorCode, &cancelAt,
		&a.CancelError, &a.CreatedAt, &a.UpdatedAt, &dispatchedAt, &finishedAt)
	if err != nil {
		return RoundAttempt{}, err
	}
	if result.Valid {
		a.Result = json.RawMessage(result.String)
	}
	if lateResult.Valid {
		a.LateResult = json.RawMessage(lateResult.String)
	}
	a.CancelRequestedAt, a.DispatchedAt, a.FinishedAt = cancelAt.String, dispatchedAt.String, finishedAt.String
	return a, nil
}

func (s *Store) RoundAttempt(ctx context.Context, id string) (RoundAttempt, error) {
	a, err := scanRoundAttempt(s.db.QueryRowContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RoundAttempt{}, ErrNotFound
	}
	return a, err
}

func uncertainAttemptsTx(ctx context.Context, tx *sql.Tx, roundID string) ([]RoundAttempt, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+roundAttemptColumns+`
  FROM round_attempts WHERE round_id=? AND state='uncertain' ORDER BY created_at,id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []RoundAttempt
	for rows.Next() {
		a, err := scanRoundAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}

func (s *Store) UncertainRoundAttempts(ctx context.Context, id string) ([]RoundAttempt, error) {
	r, err := s.Round(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.State != RoundPaused && r.State != RoundStopping {
		return nil, ErrFenced
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+roundAttemptColumns+`
  FROM round_attempts WHERE round_id=? AND state='uncertain' ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []RoundAttempt
	for rows.Next() {
		a, err := scanRoundAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}

func attemptDigest(input RoundAttemptInput) string {
	boundHash := ""
	if input.BoundCapability != "" {
		boundHash = roundCapabilityHash(input.BoundCapability)
	}
	value, _ := json.Marshal(struct {
		Operation       string
		ResourceID      string
		Cost            RoundAllowance
		CursorAttemptID string
		BoundHash       string
	}{input.Operation, input.ResourceID, input.Cost, input.CursorAttemptID, boundHash})
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// ReserveRoundAttempt atomically charges the allowance before any worker call.
// Reusing the same request key returns the prior reservation without charging.
func (s *Store) ReserveRoundAttempt(ctx context.Context, actor Actor, roundID string, input RoundAttemptInput) (RoundAttempt, bool, error) {
	if actor.Kind == "agent" {
		expected, ok := RoundOperationCost(input.Operation)
		if !ok || input.Cost != expected {
			return RoundAttempt{}, false, ErrInvalid
		}
	}
	return s.reserveRoundAttempt(ctx, actor, roundID, input, nil)
}

func (s *Store) reserveRoundAttempt(ctx context.Context, actor Actor, roundID string, input RoundAttemptInput,
	validate func(context.Context, *sql.Tx, Round) error) (RoundAttempt, bool, error) {
	if !requiredActor(actor) || strings.TrimSpace(input.RequestKey) != input.RequestKey || input.RequestKey == "" || len(input.RequestKey) > 200 ||
		strings.TrimSpace(input.Operation) != input.Operation || input.Operation == "" || len(input.Operation) > 100 ||
		input.ResourceID == "" || len(input.ResourceID) > 300 || !input.Cost.valid() || !input.Cost.nonzero() {
		return RoundAttempt{}, false, ErrInvalid
	}
	if input.BoundCapability != "" {
		expected, ok := RoundOperationCost(input.Operation)
		if !ok || expected != input.Cost {
			return RoundAttempt{}, false, ErrInvalid
		}
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return RoundAttempt{}, false, err
	}
	defer tx.Rollback()
	if err := requireRoundState(r, RoundRunning); err != nil {
		return RoundAttempt{}, false, err
	}
	if !scopeAllowsActor(r.Scope, actor) || !scopeAllows(r.Scope, input.Operation, input.ResourceID) {
		return RoundAttempt{}, false, ErrFenced
	}
	if input.BoundCapability != "" {
		authority, err := verifyRoundToolCapabilityTx(ctx, tx, input.BoundCapability, roundID)
		if err != nil {
			return RoundAttempt{}, false, err
		}
		if authority.Actor != actor {
			return RoundAttempt{}, false, ErrFenced
		}
	}
	digest := attemptDigest(input)
	var existingID, existingDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM round_attempts
  WHERE round_id=? AND request_key=?`, roundID, input.RequestKey).Scan(&existingID, &existingDigest)
	if err == nil {
		if existingDigest != digest {
			return RoundAttempt{}, false, ErrRoundIdempotencyConflict
		}
		a, err := scanRoundAttempt(tx.QueryRowContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts WHERE id=?`, existingID))
		if err != nil {
			return RoundAttempt{}, false, err
		}
		if a.State == AttemptUncertain {
			return RoundAttempt{}, false, ErrUncertain
		}
		if a.State == AttemptReserved && a.Generation != r.Generation {
			return RoundAttempt{}, false, ErrFenced
		}
		return a, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RoundAttempt{}, false, err
	}
	if validate != nil {
		if err := validate(ctx, tx, r); err != nil {
			return RoundAttempt{}, false, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used+?,
  items_used=items_used+?,tools_used=tools_used+?,turns_used=turns_used+?,
  revision=revision+1,updated_at=? WHERE id=? AND state='running'
  AND requests_used+?<=request_limit AND items_used+?<=item_limit
  AND tools_used+?<=tool_limit AND turns_used+?<=turn_limit`,
		input.Cost.Requests, input.Cost.Items, input.Cost.Tools, input.Cost.Turns, utcNow(), roundID,
		input.Cost.Requests, input.Cost.Items, input.Cost.Tools, input.Cost.Turns)
	if err != nil {
		return RoundAttempt{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return RoundAttempt{}, false, err
	}
	if count != 1 {
		return RoundAttempt{}, false, ErrAllowance
	}
	id, err := randomID()
	if err != nil {
		return RoundAttempt{}, false, err
	}
	now := utcNow()
	_, err = tx.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,'reserved',?,?,?,?,?,?)`, id, roundID, input.RequestKey, digest,
		input.Operation, input.ResourceID, r.Generation, input.Cost.Requests, input.Cost.Items,
		input.Cost.Tools, input.Cost.Turns, now, now)
	if err != nil {
		return RoundAttempt{}, false, err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.reserve", roundID); err != nil {
		return RoundAttempt{}, false, err
	}
	a, err := scanRoundAttempt(tx.QueryRowContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts WHERE id=?`, id))
	if err != nil {
		return RoundAttempt{}, false, err
	}
	return a, true, tx.Commit()
}

// MarkRoundDispatched must commit before an external request starts. The
// attempt ID is the durable dispatch identity supplied to the fake/real worker.
func (s *Store) MarkRoundDispatched(ctx context.Context, roundID, attemptID string) (RoundAttempt, error) {
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return RoundAttempt{}, err
	}
	defer tx.Rollback()
	if err := requireRoundState(r, RoundRunning); err != nil {
		return RoundAttempt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='dispatched',
  dispatched_at=?,updated_at=? WHERE id=? AND round_id=? AND state='reserved' AND generation=?`,
		utcNow(), utcNow(), attemptID, roundID, r.Generation)
	if err != nil {
		return RoundAttempt{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return RoundAttempt{}, err
	}
	if count != 1 {
		return RoundAttempt{}, ErrFenced
	}
	a, err := scanRoundAttempt(tx.QueryRowContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts WHERE id=?`, attemptID))
	if err != nil {
		return RoundAttempt{}, err
	}
	return a, tx.Commit()
}

// FinishRoundAttempt atomically persists one partial result, or a failed
// attempt, only while its dispatch generation still owns the running round.
func (s *Store) FinishRoundAttempt(ctx context.Context, actor Actor, roundID, attemptID string, success bool, result json.RawMessage, errorCode string) (RoundAttempt, error) {
	if !requiredActor(actor) || success && !validRoundJSON(result, 256*1024) ||
		!success && (errorCode == "" || len(errorCode) > 100) {
		return RoundAttempt{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return RoundAttempt{}, err
	}
	defer tx.Rollback()
	if err := requireRoundState(r, RoundRunning); err != nil {
		return RoundAttempt{}, err
	}
	var operation string
	if err := tx.QueryRowContext(ctx, `SELECT operation FROM round_attempts WHERE id=? AND round_id=?`, attemptID, roundID).Scan(&operation); err != nil {
		return RoundAttempt{}, err
	}
	if operation == RoundCodexTurn {
		var observed sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT observed_status FROM round_remote_dispatches WHERE attempt_id=? AND round_id=?`,
			attemptID, roundID).Scan(&observed); err != nil {
			return RoundAttempt{}, ErrUncertain
		}
		if success && observed.String != "completed" || !success && observed.String != "failed" && observed.String != "interrupted" {
			return RoundAttempt{}, ErrUncertain
		}
	}
	state := AttemptFailed
	var resultValue any
	if success {
		state, resultValue, errorCode = AttemptSucceeded, string(result), ""
	}
	now := utcNow()
	updated, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state=?,result_json=?,error_code=?,
  finished_at=?,updated_at=? WHERE id=? AND round_id=? AND state='dispatched' AND generation=?`,
		state, resultValue, errorCode, now, now, attemptID, roundID, r.Generation)
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
	if success {
		id, err := randomID()
		if err != nil {
			return RoundAttempt{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO round_results
  (id,round_id,attempt_id,result_json,created_at) VALUES (?,?,?,?,?)`,
			id, roundID, attemptID, string(result), now); err != nil {
			return RoundAttempt{}, err
		}
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.attempt.finish", roundID); err != nil {
		return RoundAttempt{}, err
	}
	a, err := scanRoundAttempt(tx.QueryRowContext(ctx, `SELECT `+roundAttemptColumns+` FROM round_attempts WHERE id=?`, attemptID))
	if err != nil {
		return RoundAttempt{}, err
	}
	return a, tx.Commit()
}

// A late remote response is evidence only. It cannot add a current round
// result after Stop, restart or an uncertain dispatch.
func (s *Store) RecordLateRoundResult(ctx context.Context, roundID, attemptID string, result json.RawMessage) error {
	if !validRoundJSON(result, 256*1024) {
		return ErrInvalid
	}
	tx, _, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `UPDATE round_attempts SET late_result_json=?,updated_at=?
  WHERE id=? AND round_id=? AND state='uncertain'`, string(result), utcNow(), attemptID, roundID)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

// AttachRoundRecordChange links an already audited domain write to its live
// attempt. I05 must perform the domain write and this link in one authorised
// transaction; this method only supplies the durable ledger for I04.
func (s *Store) AttachRoundRecordChange(ctx context.Context, roundID, attemptID, auditID string) error {
	if auditID == "" {
		return ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireRoundState(r, RoundRunning); err != nil {
		return err
	}
	var generation int64
	var state RoundAttemptState
	if err := tx.QueryRowContext(ctx, `SELECT generation,state FROM round_attempts
  WHERE id=? AND round_id=?`, attemptID, roundID).Scan(&generation, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != AttemptDispatched || generation != r.Generation {
		return ErrFenced
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_record_changes
  (round_id,attempt_id,audit_id,attached_at) VALUES (?,?,?,?)`, roundID, attemptID, auditID, utcNow())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RoundRecordChanges(ctx context.Context, roundID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT audit_id FROM round_record_changes
  WHERE round_id=? ORDER BY attached_at,audit_id`, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
