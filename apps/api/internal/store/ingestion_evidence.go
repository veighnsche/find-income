package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AddIngestionEvidence permits the active intake worker to add a fact only to
// its saved opportunity's immutable vacancy source. The lease and mapping are
// checked inside the evidence transaction, again after evaluation, so a stale
// Codex turn cannot write into another intake or a later job attempt.
func (s *Store) AddIngestionEvidence(ctx context.Context, claim Job, input EvidenceInput) (Evidence, string, error) {
	if claim.Kind != IngestionJobKind || claim.ID == "" || claim.LeaseToken == "" || claim.AttemptCount < 1 {
		return Evidence{}, "", ErrInvalid
	}
	var ingestionID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM ingestion_requests WHERE job_id=?`, claim.ID).Scan(&ingestionID)
	if errors.Is(err, sql.ErrNoRows) {
		return Evidence{}, "", ErrNotFound
	}
	if err != nil {
		return Evidence{}, "", err
	}
	guard := func(tx *sql.Tx) error {
		var liveID string
		err := tx.QueryRowContext(ctx, `SELECT i.id FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id
  WHERE i.id=? AND i.job_id=? AND i.status='completed' AND i.opportunity_id=? AND i.source_id=?
    AND j.kind=? AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?`,
			ingestionID, claim.ID, input.OpportunityID, input.SourceID, IngestionJobKind,
			claim.LeaseToken, claim.AttemptCount, jobTime(time.Now())).Scan(&liveID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	return s.writeEvidence(ctx, Actor{Kind: "system", ID: "ingestion:" + ingestionID}, "", input, guard)
}
