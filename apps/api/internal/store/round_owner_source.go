package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// PublishOwnerInputSourceText commits one bounded verified URL read with its
// reserved fetch attempt and current source bytes. A stopped generation cannot
// publish a late response or silently replace another submission revision.
func (s *Store) PublishOwnerInputSourceText(ctx context.Context, owner Actor, roundID, attemptID, ingestionID, text string) (IngestionRequest, error) {
	if !ownerRoundActor(owner) || roundID == "" || attemptID == "" || ingestionID == "" ||
		!boundedNonempty(text, 200000) {
		return IngestionRequest{}, ErrInvalid
	}
	tx, r, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return IngestionRequest{}, err
	}
	defer tx.Rollback()
	if r.Actor != owner || r.Outcome != "process_input" || requireRoundState(r, RoundRunning) != nil ||
		!scopeHas(r.Scope.InputRefs, "ingestion:"+ingestionID) || !scopeAllows(r.Scope, RoundFetchSource, "ingestion:"+ingestionID) {
		return IngestionRequest{}, ErrFenced
	}
	var state RoundAttemptState
	var generation int64
	var operation, resource string
	err = tx.QueryRowContext(ctx, `SELECT state,generation,operation,resource_id FROM round_attempts WHERE id=? AND round_id=?`, attemptID, roundID).Scan(&state, &generation, &operation, &resource)
	if errors.Is(err, sql.ErrNoRows) {
		return IngestionRequest{}, ErrNotFound
	}
	if err != nil {
		return IngestionRequest{}, err
	}
	if state != AttemptDispatched || generation != r.Generation || operation != RoundFetchSource || resource != "ingestion:"+ingestionID {
		return IngestionRequest{}, ErrFenced
	}
	item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
	  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, ingestionID))
	if err != nil {
		return IngestionRequest{}, err
	}
	if item.Actor != owner || item.Origin != "owner" || item.SourceURL == "" || item.OriginalText != "" ||
		item.SourceOpeningID == "" || item.DispatchStarted || item.SourceID != "" {
		return IngestionRequest{}, ErrFenced
	}
	var currentIngestionID string
	if err := tx.QueryRowContext(ctx, `SELECT current_ingestion_id FROM source_openings WHERE id=?`, item.SourceOpeningID).Scan(&currentIngestionID); err != nil {
		return IngestionRequest{}, err
	}
	if currentIngestionID != item.ID {
		return IngestionRequest{}, ErrConflict
	}
	hash, now := sourceDigest(text), utcNow()
	if _, err := tx.ExecContext(ctx, `UPDATE ingestion_requests SET original_text=?,updated_at=? WHERE id=? AND original_text=''`, text, now, ingestionID); err != nil {
		return IngestionRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE source_openings SET current_sha256=?,updated_at=? WHERE id=? AND current_ingestion_id=?`, hash, now, item.SourceOpeningID, item.ID); err != nil {
		return IngestionRequest{}, err
	}
	if err := insertSourceSighting(ctx, tx, item.SourceOpeningID, item.ID, IngestionInput{Origin: "owner", SourceURL: item.SourceURL, OriginalText: text}, hash, now, now, owner, "changed"); err != nil {
		return IngestionRequest{}, err
	}
	result, _ := json.Marshal(map[string]string{"ingestionId": ingestionID, "contentSha256": hash})
	if _, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='succeeded',result_json=?,finished_at=?,updated_at=? WHERE id=? AND state='dispatched' AND generation=?`, string(result), now, now, attemptID, r.Generation); err != nil {
		return IngestionRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET revision=revision+1,step='owner_source_read',updated_at=? WHERE id=? AND revision=?`, now, roundID, r.Revision); err != nil {
		return IngestionRequest{}, err
	}
	item.OriginalText = text
	item.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return IngestionRequest{}, err
	}
	return item, nil
}
