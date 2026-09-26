package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Prepare activity journal (A5). Every artifact draft operation records
// phase/action/result/hold entries naming the facts and answers consumed,
// the items produced, and the ready/held outcome. Entries are
// append-only; the read reuses the research Event shape with RunID set
// to the opportunity id.
const (
	PrepareTurnStarted  = "prepare.turn_started"
	PrepareArtifactHeld = "prepare.held"
	PrepareArtifactDone = "prepare.artifact_saved"
	PrepareCompleted    = "prepare.completed"
	PrepareFailed       = "prepare.failed"
)

func validPrepareKind(kind string) bool {
	switch kind {
	case PrepareTurnStarted, PrepareArtifactHeld, PrepareArtifactDone, PrepareCompleted, PrepareFailed:
		return true
	default:
		return false
	}
}

// PrepareActivityInput is one journal entry. Payload is a small JSON
// object with facts, answers, items, or error detail.
type PrepareActivityInput struct {
	CheckID string
	Kind    string
	Outcome string
	Payload json.RawMessage
}

// RecordPrepareActivity appends one entry for a selected role.
func (s *Store) RecordPrepareActivity(ctx context.Context, actor Actor, opportunityID string, input PrepareActivityInput) (string, error) {
	if opportunityID == "" || !validPrepareKind(input.Kind) || len(input.Outcome) > 64 {
		return "", fmt.Errorf("%w: prepare activity", ErrInvalid)
	}
	if len(input.Payload) > 8192 {
		return "", fmt.Errorf("%w: prepare activity payload", ErrInvalid)
	}
	if input.Payload != nil && !json.Valid(input.Payload) {
		return "", fmt.Errorf("%w: prepare activity payload", ErrInvalid)
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := checkSelectedRoleTx(ctx, tx, opportunityID); err != nil {
		return "", err
	}
	if input.CheckID != "" {
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT opportunity_id FROM job_checks WHERE id=?`,
			input.CheckID).Scan(&owner); err != nil || owner != opportunityID {
			if errors.Is(err, sql.ErrNoRows) || err == nil {
				return "", fmt.Errorf("%w: prepare activity check", ErrInvalid)
			}
			return "", err
		}
	}
	now := recordNow()
	if _, err := tx.ExecContext(ctx, `INSERT INTO prepare_activity
	  (event_id,opportunity_id,check_id,kind,outcome,payload_json,actor_kind,actor_id,observed_at,recorded_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?)`, id, opportunityID, nullString(input.CheckID),
		input.Kind, nullString(input.Outcome), nullString(string(input.Payload)),
		actor.Kind, actor.ID, now, now); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// ListPrepareActivity pages one role's prepare activity in record order.
// Cursor is the last seen event id ("" starts at the head); nextCursor is
// "" at the tail.
func (s *Store) ListPrepareActivity(ctx context.Context, opportunityID, cursor string, limit int) ([]researchcontract.Event, string, error) {
	if opportunityID == "" {
		return nil, "", fmt.Errorf("%w: opportunity id required", ErrInvalid)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	guard, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	guardErr := checkSelectedRoleTx(ctx, guard, opportunityID)
	_ = guard.Rollback()
	if guardErr != nil {
		return nil, "", guardErr
	}
	const base = `SELECT event_id,opportunity_id,check_id,kind,outcome,` +
		`payload_json,observed_at,recorded_at ` +
		`FROM prepare_activity WHERE opportunity_id=?`
	var rows *sql.Rows
	if cursor == "" {
		rows, err = s.db.QueryContext(ctx, base+` ORDER BY recorded_at,event_id LIMIT ?`, opportunityID, limit)
	} else {
		var recorded string
		if err := s.db.QueryRowContext(ctx, `SELECT recorded_at FROM prepare_activity
		  WHERE event_id=? AND opportunity_id=?`, cursor, opportunityID).Scan(&recorded); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", fmt.Errorf("%w: unknown event cursor", ErrInvalid)
			}
			return nil, "", err
		}
		rows, err = s.db.QueryContext(ctx, base+` AND (recorded_at,event_id) > (?,?)
		  ORDER BY recorded_at,event_id LIMIT ?`, opportunityID, recorded, cursor, limit)
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	events := []researchcontract.Event{}
	for rows.Next() {
		var e researchcontract.Event
		var checkID, outcome, payload sql.NullString
		var observed, recorded string
		if err := rows.Scan(&e.ID, &e.RunID, &checkID, &e.Kind, &outcome,
			&payload, &observed, &recorded); err != nil {
			return nil, "", err
		}
		e.Outcome = researchcontract.Outcome(outcome.String)
		if payload.Valid {
			e.Payload = json.RawMessage(payload.String)
		}
		if e.ObservedAt, err = parseResearchTime(observed); err != nil {
			return nil, "", err
		}
		if e.RecordedAt, err = parseResearchTime(recorded); err != nil {
			return nil, "", err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(events) == limit {
		next = events[len(events)-1].ID
	}
	return events, next, nil
}
