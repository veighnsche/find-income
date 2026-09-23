package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const maxRoundCollectorBatchBytes = 16 << 20

type RoundCollectorBatchRef struct {
	AttemptID string `json:"attemptId"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
}

// SaveRoundCollectorBatch is the publication boundary for one previously
// reserved/dispatched page. The exact posting bytes and a small cursor
// reference commit together; generic round cursor/result limits stay small.
func (s *Store) SaveRoundCollectorBatch(ctx context.Context, actor Actor, roundID, attemptID string,
	expectedRevision int64, payload json.RawMessage) (RoundCollectorBatchRef, error) {
	if !requiredActor(actor) || roundID == "" || attemptID == "" || expectedRevision < 1 ||
		len(payload) == 0 || len(payload) > maxRoundCollectorBatchBytes || !json.Valid(payload) {
		return RoundCollectorBatchRef{}, ErrInvalid
	}
	var batch struct {
		Postings []struct {
			BoardID string `json:"boardId"`
		} `json:"postings"`
		Next *struct {
			BoardID    string            `json:"boardId"`
			Pending    []json.RawMessage `json:"pending"`
			EndOfBoard bool              `json:"endOfBoard"`
		} `json:"next"`
		PagesFetched  int    `json:"pagesFetched"`
		ItemsExamined int    `json:"itemsExamined"`
		ErrorCode     string `json:"errorCode"`
		Interrupted   bool   `json:"interrupted"`
	}
	if err := json.Unmarshal(payload, &batch); err != nil {
		return RoundCollectorBatchRef{}, ErrInvalid
	}
	hash := sha256.Sum256(payload)
	ref := RoundCollectorBatchRef{AttemptID: attemptID, Bytes: len(payload), SHA256: hex.EncodeToString(hash[:])}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return RoundCollectorBatchRef{}, err
	}
	defer tx.Rollback()
	if !scopeAllowsActor(r.Scope, actor) || r.Revision != expectedRevision ||
		requireRoundState(r, RoundRunning) != nil {
		return RoundCollectorBatchRef{}, ErrFenced
	}
	var state RoundAttemptState
	var generation int64
	var operation string
	var resourceID string
	var pagesReserved, itemsReserved int
	err = tx.QueryRowContext(ctx, `SELECT state,generation,operation,resource_id,requests_reserved,items_reserved
  FROM round_attempts WHERE id=? AND round_id=?`, attemptID, roundID).
		Scan(&state, &generation, &operation, &resourceID, &pagesReserved, &itemsReserved)
	if errors.Is(err, sql.ErrNoRows) {
		return RoundCollectorBatchRef{}, ErrNotFound
	}
	if err != nil {
		return RoundCollectorBatchRef{}, err
	}
	if state != AttemptDispatched || generation != r.Generation || operation != RoundCollectorPage {
		return RoundCollectorBatchRef{}, ErrFenced
	}
	if len(resourceID) <= len("board:") || resourceID[:len("board:")] != "board:" {
		return RoundCollectorBatchRef{}, ErrFenced
	}
	boardID := resourceID[len("board:"):]
	if batch.PagesFetched < 0 || batch.PagesFetched > pagesReserved ||
		batch.ItemsExamined < 0 || batch.ItemsExamined > itemsReserved ||
		len(batch.Postings) > batch.ItemsExamined ||
		batch.Next != nil && batch.Next.BoardID != boardID ||
		batch.Next == nil && (batch.ErrorCode != "" || batch.Interrupted) {
		return RoundCollectorBatchRef{}, ErrInvalid
	}
	for _, posting := range batch.Postings {
		if posting.BoardID != boardID {
			return RoundCollectorBatchRef{}, ErrInvalid
		}
	}
	now := utcNow()
	if _, err = tx.ExecContext(ctx, `INSERT INTO round_collector_batches
	  (attempt_id,round_id,payload_json,bytes,created_at) VALUES (?,?,?,?,?)`,
		attemptID, roundID, []byte(payload), len(payload), now); err != nil {
		return RoundCollectorBatchRef{}, err
	}
	if err = publishCollectorBatchTx(ctx, tx, actor, roundID, attemptID, payload); err != nil {
		return RoundCollectorBatchRef{}, err
	}
	refJSON, _ := json.Marshal(ref)
	if _, err = tx.ExecContext(ctx, `UPDATE round_attempts SET state='succeeded',result_json=?,finished_at=?,updated_at=?
	  WHERE id=? AND round_id=? AND state='dispatched' AND generation=?`,
		string(refJSON), now, now, attemptID, roundID, r.Generation); err != nil {
		return RoundCollectorBatchRef{}, err
	}
	var priorCursor struct {
		CompletedBoardIDs []string `json:"completedBoardIds"`
	}
	if err := json.Unmarshal(r.Cursor, &priorCursor); err != nil {
		return RoundCollectorBatchRef{}, err
	}
	completed := batch.Next == nil || batch.Next.EndOfBoard && len(batch.Next.Pending) == 0
	if completed && batch.PagesFetched == 0 && batch.ItemsExamined == 0 {
		return RoundCollectorBatchRef{}, ErrInvalid
	}
	if completed {
		priorCursor.CompletedBoardIDs = append(priorCursor.CompletedBoardIDs, boardID)
	}
	cursorJSON, _ := json.Marshal(struct {
		AttemptID         string   `json:"collectorBatchAttemptId"`
		CompletedBoardIDs []string `json:"completedBoardIds"`
	}{attemptID, priorCursor.CompletedBoardIDs})
	if _, err = tx.ExecContext(ctx, `UPDATE rounds SET cursor_json=?,step='collector_batch_staged',revision=revision+1,updated_at=?
	  WHERE id=? AND state='running' AND generation=? AND revision=?`,
		string(cursorJSON), now, roundID, r.Generation, expectedRevision); err != nil {
		return RoundCollectorBatchRef{}, err
	}
	resultID, err := randomID()
	if err != nil {
		return RoundCollectorBatchRef{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO round_results(id,round_id,attempt_id,result_json,created_at)
	  VALUES (?,?,?,?,?)`, resultID, roundID, attemptID, string(refJSON), now); err != nil {
		return RoundCollectorBatchRef{}, err
	}
	if err = writeRoundAudit(ctx, tx, actor, "round.collector_batch_stage", roundID); err != nil {
		return RoundCollectorBatchRef{}, err
	}
	return ref, tx.Commit()
}

func (s *Store) RoundCollectorBatch(ctx context.Context, attemptID string) (json.RawMessage, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM round_collector_batches WHERE attempt_id=?`, attemptID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return json.RawMessage(payload), err
}
