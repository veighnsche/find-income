package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type RoundRemoteDispatch struct {
	AttemptID      string          `json:"attemptId"`
	RoundID        string          `json:"roundId"`
	Generation     int64           `json:"generation"`
	ThreadID       string          `json:"threadId"`
	TurnID         string          `json:"turnId"`
	ObservedStatus string          `json:"observedStatus"`
	Evidence       json.RawMessage `json:"evidence"`
}

func remoteAttemptTx(ctx context.Context, tx *sql.Tx, roundID, attemptID string, generation int64) error {
	if roundID == "" || attemptID == "" || generation < 1 {
		return ErrInvalid
	}
	var state RoundAttemptState
	var operation string
	var storedGeneration int64
	err := tx.QueryRowContext(ctx, `SELECT state,operation,generation FROM round_attempts WHERE id=? AND round_id=?`,
		attemptID, roundID).Scan(&state, &operation, &storedGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if operation != RoundCodexTurn || storedGeneration != generation ||
		(state != AttemptDispatched && state != AttemptUncertain) {
		return ErrFenced
	}
	return nil
}

// BindRoundThread and BindRoundTurn retain late remote identifiers as evidence
// on the original attempt even after Stop; they never restore write authority.
func (s *Store) BindRoundThread(ctx context.Context, roundID, attemptID string, generation int64, threadID string) error {
	if threadID == "" || len(threadID) > 300 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := remoteAttemptTx(ctx, tx, roundID, attemptID, generation); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO round_remote_dispatches
	  (attempt_id,round_id,generation,thread_id,updated_at) VALUES (?,?,?,?,?)
	  ON CONFLICT(attempt_id) DO UPDATE SET thread_id=excluded.thread_id,updated_at=excluded.updated_at
	  WHERE round_id=excluded.round_id AND generation=excluded.generation
	    AND (thread_id IS NULL OR thread_id=excluded.thread_id)`, attemptID, roundID, generation, threadID, utcNow())
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

func (s *Store) BindRoundTurn(ctx context.Context, roundID, attemptID string, generation int64, threadID, turnID string) error {
	if threadID == "" || turnID == "" || len(turnID) > 300 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := remoteAttemptTx(ctx, tx, roundID, attemptID, generation); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE round_remote_dispatches SET turn_id=?,updated_at=?
	  WHERE attempt_id=? AND round_id=? AND generation=? AND thread_id=?
	    AND (turn_id IS NULL OR turn_id=?)`, turnID, utcNow(), attemptID, roundID, generation, threadID, turnID)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

func (s *Store) ObserveRoundTurn(ctx context.Context, roundID, attemptID string, generation int64,
	threadID, turnID, status string, evidence json.RawMessage) error {
	if threadID == "" || turnID == "" ||
		(status != "completed" && status != "failed" && status != "interrupted" && status != "unknown") ||
		!validRoundJSON(evidence, 64*1024) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := remoteAttemptTx(ctx, tx, roundID, attemptID, generation); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE round_remote_dispatches SET observed_status=?,observed_evidence_json=?,updated_at=?
	  WHERE attempt_id=? AND round_id=? AND generation=? AND thread_id=? AND turn_id=?
	    AND (observed_status IS NULL OR observed_status='unknown' OR observed_status=?)`,
		status, string(evidence), utcNow(), attemptID, roundID, generation, threadID, turnID, status)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	return tx.Commit()
}

func (s *Store) RoundRemoteDispatch(ctx context.Context, roundID, attemptID string) (RoundRemoteDispatch, error) {
	var d RoundRemoteDispatch
	var threadID, turnID, status, evidence sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT attempt_id,round_id,generation,thread_id,turn_id,
	  observed_status,observed_evidence_json FROM round_remote_dispatches
	  WHERE attempt_id=? AND round_id=?`, attemptID, roundID).Scan(&d.AttemptID, &d.RoundID, &d.Generation,
		&threadID, &turnID, &status, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return RoundRemoteDispatch{}, ErrNotFound
	}
	if err != nil {
		return RoundRemoteDispatch{}, err
	}
	d.ThreadID, d.TurnID, d.ObservedStatus = threadID.String, turnID.String, status.String
	if evidence.Valid {
		d.Evidence = json.RawMessage(evidence.String)
	}
	return d, nil
}
