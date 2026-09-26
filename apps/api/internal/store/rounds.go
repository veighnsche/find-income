package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrActiveRound              = errors.New("another round is active")
	ErrAllowance                = errors.New("round allowance exhausted")
	ErrFenced                   = errors.New("round authority fenced")
	ErrUncertain                = errors.New("round attempt needs reconciliation")
	ErrExpired                  = errors.New("round deadline reached")
	ErrRoundIdempotencyConflict = errors.New("round request key reused with different input")
)

type RoundState string

const (
	RoundQueued        RoundState = "queued"
	RoundRunning       RoundState = "running"
	RoundAwaitingInput RoundState = "awaiting_input"
	RoundStopping      RoundState = "stopping"
	RoundPaused        RoundState = "paused"
	RoundCompleted     RoundState = "completed"
	RoundFailed        RoundState = "failed"
)

type RoundAllowance struct {
	Requests int64 `json:"requests"`
	Items    int64 `json:"items"`
	Tools    int64 `json:"tools"`
	Turns    int64 `json:"turns"`
}

func (a RoundAllowance) valid() bool {
	return a.Requests >= 0 && a.Items >= 0 && a.Tools >= 0 && a.Turns >= 0 &&
		a.Requests <= 10000 && a.Items <= 10000 && a.Tools <= 10000 && a.Turns <= 10000
}

func (a RoundAllowance) nonzero() bool {
	return a.Requests != 0 || a.Items != 0 || a.Tools != 0 || a.Turns != 0
}

type RoundScope struct {
	InputRefs  []string `json:"inputRefs"`
	Resources  []string `json:"resources"`
	Operations []string `json:"operations"`
	Delegates  []string `json:"delegates"`
}

func validScope(scope RoundScope) bool {
	if len(scope.Operations) == 0 || len(scope.Operations) > 100 ||
		len(scope.Resources) > 1000 || len(scope.InputRefs) > 1000 || len(scope.Delegates) > 100 {
		return false
	}
	for _, group := range [][]string{scope.Operations, scope.Resources, scope.InputRefs, scope.Delegates} {
		seen := map[string]bool{}
		for _, value := range group {
			if strings.TrimSpace(value) != value || value == "" || len(value) > 300 || seen[value] {
				return false
			}
			seen[value] = true
		}
	}
	return true
}

func scopeAllowsActor(scope RoundScope, actor Actor) bool {
	if actor.Kind == "administrator" && actor.ID != "" {
		return true
	}
	if actor.Kind != "agent" {
		return false
	}
	for _, delegate := range scope.Delegates {
		if delegate == actor.ID {
			return true
		}
	}
	return false
}

func scopeAllows(scope RoundScope, operation, resource string) bool {
	allowed := false
	for _, item := range scope.Operations {
		if item == operation {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	if resource == "" {
		return false
	}
	for _, item := range scope.Resources {
		if item == resource {
			return true
		}
	}
	return false
}

type StartRoundInput struct {
	RequestKey     string
	Intent         string
	Outcome        string
	ProfileVersion int64
	Scope          RoundScope
	Limits         RoundAllowance
	Deadline       time.Time
}

type Round struct {
	ID                     string
	Actor                  Actor
	RequestKey             string
	Intent                 string
	Outcome                string
	InitialProfileVersion  int64
	ProfileVersion         int64
	Scope                  RoundScope
	State                  RoundState
	Revision               int64
	Generation             int64
	Deadline               time.Time
	Limits                 RoundAllowance
	Used                   RoundAllowance
	Step                   string
	Cursor                 json.RawMessage
	Unresolved             json.RawMessage
	Report                 json.RawMessage
	StopReason             string
	DeliverableStatus      string
	ReconciliationRequired bool
	CreatedAt              string
	UpdatedAt              string
	CompletedAt            string
}

const roundColumns = `id,actor_kind,actor_id,request_key,intent,outcome,initial_profile_version,profile_version,
  scope_json,state,revision,generation,deadline_at,request_limit,item_limit,tool_limit,turn_limit,
  requests_used,items_used,tools_used,turns_used,step,cursor_json,unresolved_json,report_json,
  stop_reason,deliverable_status,reconciliation_required,created_at,updated_at,completed_at`

func scanRound(row rowScanner) (Round, error) {
	var r Round
	var scopeJSON, deadline, cursor, unresolved, report string
	var reconcile int
	var completed sql.NullString
	err := row.Scan(&r.ID, &r.Actor.Kind, &r.Actor.ID, &r.RequestKey, &r.Intent, &r.Outcome,
		&r.InitialProfileVersion, &r.ProfileVersion, &scopeJSON, &r.State, &r.Revision, &r.Generation, &deadline,
		&r.Limits.Requests, &r.Limits.Items, &r.Limits.Tools, &r.Limits.Turns,
		&r.Used.Requests, &r.Used.Items, &r.Used.Tools, &r.Used.Turns,
		&r.Step, &cursor, &unresolved, &report, &r.StopReason, &r.DeliverableStatus,
		&reconcile, &r.CreatedAt, &r.UpdatedAt, &completed)
	if err != nil {
		return Round{}, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &r.Scope); err != nil {
		return Round{}, err
	}
	r.Deadline, err = time.Parse(time.RFC3339Nano, deadline)
	if err != nil {
		return Round{}, err
	}
	r.Cursor, r.Unresolved, r.Report = json.RawMessage(cursor), json.RawMessage(unresolved), json.RawMessage(report)
	r.ReconciliationRequired, r.CompletedAt = reconcile == 1, completed.String
	return r, nil
}

func (s *Store) Round(ctx context.Context, id string) (Round, error) {
	r, err := scanRound(s.db.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, ErrNotFound
	}
	return r, err
}

func (s *Store) ActiveRound(ctx context.Context) (Round, error) {
	r, err := scanRound(s.db.QueryRowContext(ctx, `SELECT `+roundColumns+`
  FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused') LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, ErrNotFound
	}
	return r, err
}

// ListRoundsOptions filters the newest-first round list. Empty Outcome
// matches every outcome; empty States matches every state.
type ListRoundsOptions struct {
	Outcome string
	States  []string
	Cursor  string
	Limit   int
}

// ListRounds returns durable rounds newest-first over (updated_at, id)
// for server run recovery (C1/R13). The cursor is "updatedAt|id" of the
// last item of the previous page; unknown states and malformed cursors
// fail with ErrInvalid. Pure read: no commissions, no resumes.
func (s *Store) ListRounds(ctx context.Context, opts ListRoundsOptions) (rounds []Round, nextCursor string, err error) {
	if len(opts.Outcome) > 100 || len(opts.Cursor) > 512 {
		return nil, "", ErrInvalid
	}
	for _, state := range opts.States {
		switch RoundState(state) {
		case RoundQueued, RoundRunning, RoundAwaitingInput, RoundStopping,
			RoundPaused, RoundCompleted, RoundFailed:
		default:
			return nil, "", fmt.Errorf("%w: unknown round state %q", ErrInvalid, state)
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	cursorAt, cursorID := "9999-12-31T23:59:59.999999999Z", "9999-12-31T23:59:59.999999999Z"
	if opts.Cursor != "" {
		parts := strings.SplitN(opts.Cursor, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
			len(parts[0]) > 64 || len(parts[1]) > 128 {
			return nil, "", fmt.Errorf("%w: unknown rounds cursor; re-page from the head", ErrInvalid)
		}
		cursorAt, cursorID = parts[0], parts[1]
	}
	query := `SELECT ` + roundColumns + ` FROM rounds
	  WHERE (updated_at < ? OR (updated_at = ? AND id < ?))`
	args := []any{cursorAt, cursorAt, cursorID}
	if opts.Outcome != "" {
		query += ` AND outcome=?`
		args = append(args, opts.Outcome)
	}
	if len(opts.States) > 0 {
		query += ` AND state IN (?` + strings.Repeat(",?", len(opts.States)-1) + `)`
		for _, state := range opts.States {
			args = append(args, state)
		}
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	rounds = []Round{}
	for rows.Next() {
		round, err := scanRound(rows)
		if err != nil {
			return nil, "", err
		}
		rounds = append(rounds, round)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(rounds) > limit {
		rounds = rounds[:limit]
		last := rounds[len(rounds)-1]
		nextCursor = last.UpdatedAt + "|" + last.ID
	}
	return rounds, nextCursor, nil
}

func (s *Store) RoundByRequest(ctx context.Context, actor Actor, key string) (Round, error) {
	r, err := scanRound(s.db.QueryRowContext(ctx, `SELECT `+roundColumns+`
  FROM rounds WHERE actor_kind=? AND actor_id=? AND request_key=?`, actor.Kind, actor.ID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, ErrNotFound
	}
	return r, err
}

func roundDigest(input StartRoundInput) (string, error) {
	value, err := json.Marshal(struct {
		Intent         string
		Outcome        string
		ProfileVersion int64
		Scope          RoundScope
		Limits         RoundAllowance
		Deadline       string
	}{input.Intent, input.Outcome, input.ProfileVersion, input.Scope, input.Limits, input.Deadline.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:]), nil
}

func ownerRoundActor(actor Actor) bool { return actor.Kind == "administrator" && actor.ID != "" }

func validRoundOutcome(outcome string) bool {
	return strings.TrimSpace(outcome) != "" && len(outcome) <= 100 && outcome != "discover"
}

// StartRound binds the request identity, captured profile and allowance in one
// SQLite writer transaction. An active paused round still owns the slot.
func (s *Store) StartRound(ctx context.Context, actor Actor, input StartRoundInput) (Round, bool, error) {
	now := time.Now().UTC()
	if !ownerRoundActor(actor) || strings.TrimSpace(input.RequestKey) != input.RequestKey ||
		input.RequestKey == "" || len(input.RequestKey) > 200 ||
		strings.TrimSpace(input.Intent) == "" || len(input.Intent) > 2000 ||
		!validRoundOutcome(input.Outcome) ||
		input.ProfileVersion < 1 || !validScope(input.Scope) || !input.Limits.valid() || !input.Limits.nonzero() ||
		input.Deadline.IsZero() {
		return Round{}, false, ErrInvalid
	}
	digest, err := roundDigest(input)
	if err != nil {
		return Round{}, false, err
	}
	scopeJSON, _ := json.Marshal(input.Scope)
	id, err := randomID()
	if err != nil {
		return Round{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Round{}, false, err
	}
	defer tx.Rollback()
	// Take the writer reservation before reading the profile snapshot.
	if _, err := tx.ExecContext(ctx, `UPDATE preferences_current SET version=version WHERE singleton=1`); err != nil {
		return Round{}, false, err
	}
	var oldID, oldDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM rounds
  WHERE actor_kind=? AND actor_id=? AND request_key=?`, actor.Kind, actor.ID, input.RequestKey).Scan(&oldID, &oldDigest)
	if err == nil {
		if oldDigest != digest {
			return Round{}, false, ErrRoundIdempotencyConflict
		}
		r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, oldID))
		if err != nil {
			return Round{}, false, err
		}
		return r, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Round{}, false, err
	}
	if !input.Deadline.After(now) || input.Deadline.After(now.Add(24*time.Hour)) {
		return Round{}, false, ErrInvalid
	}
	var currentVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM preferences_current WHERE singleton=1`).Scan(&currentVersion); err != nil {
		return Round{}, false, err
	}
	if currentVersion != input.ProfileVersion {
		return Round{}, false, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO rounds
  (id,actor_kind,actor_id,request_key,request_sha256,intent,outcome,initial_profile_version,profile_version,scope_json,
   state,revision,generation,deadline_at,request_limit,item_limit,tool_limit,turn_limit,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,'queued',1,1,?,?,?,?,?,?,?)
  ON CONFLICT(actor_kind,actor_id,request_key) DO NOTHING`, id, actor.Kind, actor.ID,
		input.RequestKey, digest, input.Intent, input.Outcome, input.ProfileVersion, input.ProfileVersion, string(scopeJSON),
		input.Deadline.UTC().Format(time.RFC3339Nano), input.Limits.Requests, input.Limits.Items,
		input.Limits.Tools, input.Limits.Turns, utcNow(), utcNow())
	if err != nil {
		if strings.Contains(err.Error(), "one_active_round") || strings.Contains(err.Error(), "UNIQUE constraint failed: index") {
			return Round{}, false, ErrActiveRound
		}
		return Round{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Round{}, false, err
	}
	if inserted == 0 {
		var oldDigest string
		if err := tx.QueryRowContext(ctx, `SELECT id,request_sha256 FROM rounds
  WHERE actor_kind=? AND actor_id=? AND request_key=?`, actor.Kind, actor.ID, input.RequestKey).Scan(&id, &oldDigest); err != nil {
			return Round{}, false, err
		}
		if oldDigest != digest {
			return Round{}, false, ErrRoundIdempotencyConflict
		}
	} else if err := writeRoundAudit(ctx, tx, actor, "round.start", id); err != nil {
		return Round{}, false, err
	}
	r, err := scanRound(tx.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, id))
	if err != nil {
		return Round{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Round{}, false, err
	}
	return r, inserted == 1, nil
}

func writeRoundAudit(ctx context.Context, tx *sql.Tx, actor Actor, operation, id string) error {
	auditID, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,occurred_at)
  VALUES (?,?,?,?, 'round',?,?)`, auditID, actor.Kind, actor.ID, operation, id, utcNow())
	return err
}

// recoverRounds pauses interrupted authority and fences paused checks before
// an opened store is returned.
func recoverRounds(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM rounds
  WHERE state IN ('queued','running','awaiting_input','stopping','paused')`)
	if err != nil {
		return err
	}
	var interrupted []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		interrupted = append(interrupted, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	now := utcNow()
	if _, err := tx.ExecContext(ctx, `UPDATE round_reconciliation_checks
  SET state='fenced',finished_at=? WHERE state='pending' AND round_id IN
    (SELECT id FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping','paused'))`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='uncertain',updated_at=?
  WHERE state='dispatched' AND round_id IN
    (SELECT id FROM rounds WHERE state IN ('queued','running','awaiting_input','stopping'))`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET revision=revision+1,
  generation=generation+1,updated_at=? WHERE state='paused'`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET state='paused',revision=revision+1,
  generation=generation+1,reconciliation_required=1,stop_reason='process_interrupted',updated_at=?
  WHERE state IN ('queued','running','awaiting_input','stopping')`, now); err != nil {
		return err
	}
	for _, id := range interrupted {
		if err := writeRoundAudit(ctx, tx, Actor{Kind: "system", ID: "round-recovery"}, "round.recover", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
