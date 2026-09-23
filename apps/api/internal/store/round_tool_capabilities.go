package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type RoundToolAuthority struct {
	RoundID    string
	AttemptID  string
	Actor      Actor
	Generation int64
}

func roundCapabilityHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueRoundToolCapability binds one isolated turn to one dispatched attempt.
// The token is returned once; only its digest is retained in SQLite.
func (s *Store) IssueRoundToolCapability(ctx context.Context, roundID, attemptID, agentID string) (string, error) {
	if roundID == "" || attemptID == "" || agentID == "" {
		return "", ErrInvalid
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	token := hex.EncodeToString(secret)
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if requireRoundState(r, RoundRunning) != nil || !scopeAllowsActor(r.Scope, Actor{Kind: "agent", ID: agentID}) {
		return "", ErrFenced
	}
	var generation int64
	var state RoundAttemptState
	var operation string
	err = tx.QueryRowContext(ctx, `SELECT generation,state,operation FROM round_attempts WHERE id=? AND round_id=?`,
		attemptID, roundID).Scan(&generation, &state, &operation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if generation != r.Generation || state != AttemptDispatched || operation != RoundCodexTurn {
		return "", ErrFenced
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO round_tool_capabilities
	  (token_sha256,round_id,attempt_id,actor_id,generation,issued_at) VALUES (?,?,?,?,?,?)`,
		roundCapabilityHash(token), roundID, attemptID, agentID, generation, utcNow())
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func verifyRoundToolCapabilityTx(ctx context.Context, tx *sql.Tx, token, roundID string) (RoundToolAuthority, error) {
	if len(token) != 64 || roundID == "" {
		return RoundToolAuthority{}, ErrFenced
	}
	var a RoundToolAuthority
	var state RoundState
	var attemptState RoundAttemptState
	var currentGeneration, attemptGeneration int64
	var deadline string
	err := tx.QueryRowContext(ctx, `SELECT c.round_id,c.attempt_id,c.actor_id,c.generation,
	  r.state,r.generation,r.deadline_at,a.state,a.generation
	  FROM round_tool_capabilities c JOIN rounds r ON r.id=c.round_id
	  JOIN round_attempts a ON a.id=c.attempt_id
	  WHERE c.token_sha256=? AND c.round_id=? AND c.revoked_at IS NULL`,
		roundCapabilityHash(token), roundID).Scan(&a.RoundID, &a.AttemptID, &a.Actor.ID, &a.Generation,
		&state, &currentGeneration, &deadline, &attemptState, &attemptGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return RoundToolAuthority{}, ErrFenced
	}
	if err != nil {
		return RoundToolAuthority{}, err
	}
	deadlineAt, parseErr := time.Parse(time.RFC3339Nano, deadline)
	if parseErr != nil {
		return RoundToolAuthority{}, parseErr
	}
	if state != RoundRunning || currentGeneration != a.Generation ||
		attemptGeneration != a.Generation || attemptState != AttemptDispatched || !time.Now().Before(deadlineAt) {
		return RoundToolAuthority{}, ErrFenced
	}
	a.Actor.Kind = "agent"
	return a, nil
}

func (s *Store) VerifyRoundToolCapability(ctx context.Context, token, roundID string) (RoundToolAuthority, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RoundToolAuthority{}, err
	}
	defer tx.Rollback()
	a, err := verifyRoundToolCapabilityTx(ctx, tx, token, roundID)
	if err != nil {
		return RoundToolAuthority{}, err
	}
	return a, tx.Commit()
}

func (s *Store) RevokeRoundToolCapability(ctx context.Context, token string) error {
	if len(token) != 64 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE round_tool_capabilities SET revoked_at=?
	  WHERE token_sha256=? AND revoked_at IS NULL`, utcNow(), roundCapabilityHash(token))
	return err
}
