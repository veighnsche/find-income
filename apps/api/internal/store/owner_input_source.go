package store

import (
	"context"
	"database/sql"
	"errors"
)

type OwnerInputSourceInput struct {
	RequestKey       string
	TargetID         string
	ExpectedRevision int64
	SourceURL        string
	OriginalText     string
	ReplacePausedID  string
	ReplacePausedRev int64
}

// SubmitOwnerInputSource binds the entire commission request to its original
// intake before source deduplication can return an older ingestion. The binding
// and any new source state commit together, so an exact retry never resubmits.
func (s *Store) SubmitOwnerInputSource(ctx context.Context, actor Actor, input OwnerInputSourceInput) (IngestionRequest, error) {
	if !ownerRoundActor(actor) || !ownerRequestKey(input.RequestKey) || input.TargetID != "active" || input.ExpectedRevision < 1 ||
		(input.ReplacePausedID == "") != (input.ReplacePausedRev == 0) {
		return IngestionRequest{}, ErrInvalid
	}
	source := IngestionInput{Origin: "owner", SourceURL: input.SourceURL, OriginalText: input.OriginalText, IdempotencyKey: input.RequestKey + ":source"}
	if err := validateIngestionInput(actor, &source); err != nil {
		return IngestionRequest{}, err
	}
	digest := ownerDigest(input)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestionRequest{}, err
	}
	defer tx.Rollback()
	// Serialize the lookup with concurrent calls from another Store handle.
	if _, err := tx.ExecContext(ctx, `UPDATE owner_input_sources SET actor_id=actor_id WHERE actor_id=? AND request_key=?`, actor.ID, input.RequestKey); err != nil {
		return IngestionRequest{}, err
	}
	var oldDigest, ingestionID string
	err = tx.QueryRowContext(ctx, `SELECT request_sha256,ingestion_id FROM owner_input_sources WHERE actor_id=? AND request_key=?`, actor.ID, input.RequestKey).Scan(&oldDigest, &ingestionID)
	if err == nil {
		if oldDigest != digest {
			return IngestionRequest{}, ErrRoundIdempotencyConflict
		}
		item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
		  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.id=?`, ingestionID))
		return item, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IngestionRequest{}, err
	}
	item, _, err := submitIngestionTx(ctx, tx, actor, source, collectorSightingRef{})
	if err != nil {
		return IngestionRequest{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owner_input_sources (actor_id,request_key,request_sha256,ingestion_id,created_at)
	  VALUES (?,?,?,?,?)`, actor.ID, input.RequestKey, digest, item.ID, utcNow())
	if err != nil {
		return IngestionRequest{}, err
	}
	return item, tx.Commit()
}
