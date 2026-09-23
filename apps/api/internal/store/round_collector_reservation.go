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
			  JOIN round_attempts a ON a.id=b.attempt_id WHERE b.attempt_id=? AND b.round_id=?`,
				input.CursorAttemptID, roundID).Scan(&payload, &priorResource)
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
