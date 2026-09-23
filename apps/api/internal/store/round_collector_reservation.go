package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type RoundCollectorAcquisitionInput struct {
	RequestKey      string
	BoardID         string
	CursorAttemptID string
	MaxPages        int
	MaxItems        int
}

// ReserveCollectorAcquisition turns concrete page/item capacity into a
// server-calculated charge. MaxPages=0 can only drain buffered items from the
// previous staged batch; it never authorizes another provider request.
func (s *Store) ReserveCollectorAcquisition(ctx context.Context, actor Actor, roundID string,
	input RoundCollectorAcquisitionInput) (RoundAttempt, bool, error) {
	if input.BoardID == "" || input.MaxItems < 1 || input.MaxItems > 25 ||
		(input.MaxPages != 0 && input.MaxPages != 1) ||
		(input.MaxPages == 0 && input.CursorAttemptID == "") {
		return RoundAttempt{}, false, ErrInvalid
	}
	cost := RoundAllowance{Requests: int64(input.MaxPages), Items: int64(input.MaxItems), Tools: 1}
	attemptInput := RoundAttemptInput{RequestKey: input.RequestKey, Operation: RoundCollectorPage,
		ResourceID: "board:" + input.BoardID, Cost: cost, CursorAttemptID: input.CursorAttemptID}
	return s.reserveRoundAttempt(ctx, actor, roundID, attemptInput,
		func(ctx context.Context, tx *sql.Tx, r Round) error {
			var current struct {
				AttemptID         string   `json:"collectorBatchAttemptId"`
				ImportedAttemptID string   `json:"importedCollectorAttemptId"`
				CompletedBoardIDs []string `json:"completedBoardIds"`
			}
			if err := json.Unmarshal(r.Cursor, &current); err != nil {
				return err
			}
			if current.AttemptID != input.CursorAttemptID {
				return ErrConflict
			}
			for _, completed := range current.CompletedBoardIDs {
				if completed == input.BoardID {
					return ErrFenced
				}
			}
			if input.CursorAttemptID == "" {
				if input.MaxPages != 1 {
					return ErrInvalid
				}
				return nil
			}
			var payload []byte
			var priorResource string
			err := tx.QueryRowContext(ctx, `SELECT b.payload_json,a.resource_id FROM round_collector_batches b
			  JOIN round_attempts a ON a.id=b.attempt_id WHERE b.attempt_id=?
			  AND (b.round_id=? OR b.attempt_id=?)`,
				input.CursorAttemptID, roundID, current.ImportedAttemptID).Scan(&payload, &priorResource)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			var previous struct {
				Next *struct {
					BoardID    string            `json:"boardId"`
					Pending    []json.RawMessage `json:"pending"`
					EndOfBoard bool              `json:"endOfBoard"`
				} `json:"next"`
			}
			if err := json.Unmarshal(payload, &previous); err != nil {
				return err
			}
			if priorResource != attemptInput.ResourceID {
				// A completed board releases the single current cursor for the
				// next explicitly scoped board. Its completed ID remains saved.
				if previous.Next != nil && (!previous.Next.EndOfBoard || len(previous.Next.Pending) != 0) {
					return ErrFenced
				}
				if input.MaxPages != 1 {
					return ErrInvalid
				}
				return nil
			}
			if previous.Next == nil || previous.Next.BoardID != input.BoardID {
				return ErrFenced
			}
			if len(previous.Next.Pending) > 0 {
				if input.MaxPages != 0 {
					return ErrInvalid
				}
			} else {
				if previous.Next.EndOfBoard {
					return ErrFenced
				}
				if input.MaxPages != 1 {
					return ErrInvalid
				}
			}
			return nil
		})
}

// ImportRoundCollectorCursor explicitly carries one incomplete, staged board
// cursor into a newly commissioned round. It does not schedule or dispatch a
// provider call; the next reservation still spends that round's allowance.
func (s *Store) ImportRoundCollectorCursor(ctx context.Context, actor Actor, newRoundID, priorAttemptID string) (Round, error) {
	if !ownerRoundActor(actor) || newRoundID == "" || priorAttemptID == "" {
		return Round{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, newRoundID)
	if err != nil {
		return Round{}, err
	}
	defer tx.Rollback()
	if r.Actor != actor || requireRoundState(r, RoundRunning) != nil {
		return Round{}, ErrFenced
	}
	var cursor struct {
		AttemptID string `json:"collectorBatchAttemptId"`
	}
	if err := json.Unmarshal(r.Cursor, &cursor); err != nil {
		return Round{}, err
	}
	if cursor.AttemptID != "" {
		return Round{}, ErrConflict
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_collector_batches WHERE round_id=?`, newRoundID).Scan(&existing); err != nil {
		return Round{}, err
	}
	if existing != 0 {
		return Round{}, ErrConflict
	}
	var priorRoundID, priorActorKind, priorActorID, priorResource, priorState, attemptState string
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT b.round_id,pr.actor_kind,pr.actor_id,a.resource_id,pr.state,a.state,b.payload_json
	  FROM round_collector_batches b JOIN round_attempts a ON a.id=b.attempt_id
	  JOIN rounds pr ON pr.id=b.round_id WHERE b.attempt_id=?`, priorAttemptID).Scan(
		&priorRoundID, &priorActorKind, &priorActorID, &priorResource, &priorState, &attemptState, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, ErrNotFound
	}
	if err != nil {
		return Round{}, err
	}
	if priorRoundID == newRoundID || priorActorKind != actor.Kind || priorActorID != actor.ID ||
		attemptState != string(AttemptSucceeded) || priorState != string(RoundCompleted) && priorState != string(RoundFailed) ||
		!scopeAllows(r.Scope, RoundCollectorPage, priorResource) {
		return Round{}, ErrFenced
	}
	var previous struct {
		Next *struct {
			BoardID    string            `json:"boardId"`
			Pending    []json.RawMessage `json:"pending"`
			EndOfBoard bool              `json:"endOfBoard"`
		} `json:"next"`
	}
	if err := json.Unmarshal(payload, &previous); err != nil {
		return Round{}, err
	}
	if previous.Next == nil || "board:"+previous.Next.BoardID != priorResource ||
		previous.Next.EndOfBoard && len(previous.Next.Pending) == 0 {
		return Round{}, ErrFenced
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM round_attempts WHERE round_id=? AND resource_id=?
	  AND state IN ('reserved','dispatched','uncertain')`, priorRoundID, priorResource).Scan(&unresolved); err != nil {
		return Round{}, err
	}
	if unresolved != 0 {
		return Round{}, ErrUncertain
	}
	now := utcNow()
	encoded, _ := json.Marshal(struct {
		AttemptID         string `json:"collectorBatchAttemptId"`
		ImportedAttemptID string `json:"importedCollectorAttemptId"`
	}{priorAttemptID, priorAttemptID})
	result, err := tx.ExecContext(ctx, `UPDATE rounds SET cursor_json=?,step='collector_cursor_imported',revision=revision+1,updated_at=?
	  WHERE id=? AND state='running' AND generation=? AND revision=?`, string(encoded), now, newRoundID, r.Generation, r.Revision)
	if err != nil {
		return Round{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Round{}, err
	}
	if count != 1 {
		return Round{}, ErrFenced
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.collector_cursor_import", newRoundID); err != nil {
		return Round{}, err
	}
	r.Cursor = encoded
	r.Step = "collector_cursor_imported"
	r.Revision++
	r.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return Round{}, err
	}
	return r, nil
}
