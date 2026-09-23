package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type IngestionOpportunityMatch struct {
	OpportunityID  string
	RecordChangeID string
}

func findMappedOpportunitySource(ctx context.Context, reader Reader, opportunityID, sourceURL, originalText string) (IngestionOpportunityMatch, bool, error) {
	if opportunityID == "" || sourceURL == "" || originalText == "" {
		return IngestionOpportunityMatch{}, false, nil
	}
	var match IngestionOpportunityMatch
	err := reader.QueryRowContext(ctx, `SELECT o.id,rc.audit_id FROM opportunities o
  JOIN record_changes rc ON rc.audit_id=(SELECT event.audit_id FROM record_changes event
    WHERE event.entity_kind='opportunity' AND event.entity_id=o.id AND event.snapshot_state='captured'
      AND json_extract(event.snapshot_json,'$.companyId')=o.company_id
      AND json_extract(event.snapshot_json,'$.kind')=o.kind
      AND COALESCE(json_extract(event.snapshot_json,'$.sourceUrl'),'')=COALESCE(o.source_url,'')
      AND json_extract(event.snapshot_json,'$.originalText')=o.original_text
    ORDER BY event.sequence DESC LIMIT 1)
  WHERE o.id=? AND o.archived_at IS NULL AND COALESCE(o.source_url,'')=? AND o.original_text=?`,
		opportunityID, sourceURL, originalText).Scan(&match.OpportunityID, &match.RecordChangeID)
	if errors.Is(err, sql.ErrNoRows) {
		return IngestionOpportunityMatch{}, false, nil
	}
	return match, err == nil, err
}

// findMatchingOpportunity looks only for one current, unarchived opportunity
// whose complete source URL and text equal this intake's source. Ambiguous
// duplicates require review rather than picking a record or merging content.
func findMatchingOpportunity(ctx context.Context, reader Reader, sourceURL, originalText string) (IngestionOpportunityMatch, bool, error) {
	if sourceURL == "" || originalText == "" {
		return IngestionOpportunityMatch{}, false, nil
	}
	rows, err := reader.QueryContext(ctx, `SELECT o.id,rc.audit_id FROM opportunities o
  JOIN record_changes rc ON rc.audit_id=(SELECT event.audit_id FROM record_changes event
    WHERE event.entity_kind='opportunity' AND event.entity_id=o.id AND event.snapshot_state='captured'
      AND json_extract(event.snapshot_json,'$.companyId')=o.company_id
      AND json_extract(event.snapshot_json,'$.kind')=o.kind
      AND COALESCE(json_extract(event.snapshot_json,'$.sourceUrl'),'')=COALESCE(o.source_url,'')
      AND json_extract(event.snapshot_json,'$.originalText')=o.original_text
    ORDER BY event.sequence DESC LIMIT 1)
  WHERE o.archived_at IS NULL AND COALESCE(o.source_url,'')=? AND o.original_text=?
  ORDER BY o.id LIMIT 2`, sourceURL, originalText)
	if err != nil {
		return IngestionOpportunityMatch{}, false, err
	}
	defer rows.Close()
	var match IngestionOpportunityMatch
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&match.OpportunityID, &match.RecordChangeID); err != nil {
			return IngestionOpportunityMatch{}, false, err
		}
	}
	if err := rows.Err(); err != nil {
		return IngestionOpportunityMatch{}, false, err
	}
	if count > 1 {
		return IngestionOpportunityMatch{}, false, ErrConflict
	}
	return match, count == 1, nil
}

// FindMatchingIngestionOpportunity is a lease-scoped hint for the Codex tool
// bridge. RecordIngestionResult rechecks and links it; SaveIngestionOpportunity
// also repeats this lookup inside its writer transaction to close the race.
func (s *Store) FindMatchingIngestionOpportunity(ctx context.Context, claim Job) (IngestionOpportunityMatch, bool, error) {
	if claim.Kind != IngestionJobKind || claim.ID == "" || claim.LeaseToken == "" || claim.AttemptCount < 1 {
		return IngestionOpportunityMatch{}, false, ErrInvalid
	}
	var sourceURL, originalText string
	var opportunityID, recordChangeID, sourceOpeningID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(i.source_url,''),i.original_text,i.opportunity_id,i.record_change_id,i.source_opening_id
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id
  WHERE i.job_id=? AND j.kind=? AND j.state='running' AND j.lease_token=?
    AND j.attempt_count=? AND j.lease_until>?`, claim.ID, IngestionJobKind,
		claim.LeaseToken, claim.AttemptCount, jobTime(time.Now())).
		Scan(&sourceURL, &originalText, &opportunityID, &recordChangeID, &sourceOpeningID)
	if errors.Is(err, sql.ErrNoRows) {
		return IngestionOpportunityMatch{}, false, ErrConflict
	}
	if err != nil {
		return IngestionOpportunityMatch{}, false, err
	}
	if opportunityID.Valid && recordChangeID.Valid {
		return IngestionOpportunityMatch{OpportunityID: opportunityID.String, RecordChangeID: recordChangeID.String}, true, nil
	}
	if sourceOpeningID.Valid {
		var mappedID sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT opportunity_id FROM source_openings WHERE id=?`, sourceOpeningID.String).Scan(&mappedID); err != nil {
			return IngestionOpportunityMatch{}, false, err
		}
		if !mappedID.Valid {
			return IngestionOpportunityMatch{}, false, nil
		}
		return findMappedOpportunitySource(ctx, s.db, mappedID.String, sourceURL, originalText)
	}
	return findMatchingOpportunity(ctx, s.db, sourceURL, originalText)
}

// ResolveIngestionBeforeDispatch links a verified, identical current source
// without starting Codex. Its lease check and mapping commit are atomic. A
// changed source returns false so extraction can refresh the mapped record.
func (s *Store) ResolveIngestionBeforeDispatch(ctx context.Context, claim Job) (bool, error) {
	if claim.Kind != IngestionJobKind || claim.ID == "" || claim.LeaseToken == "" || claim.AttemptCount < 1 {
		return false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE ingestion_requests SET id=id WHERE job_id=?`, claim.ID); err != nil {
		return false, err
	}
	item, err := scanIngestion(tx.QueryRowContext(ctx, `SELECT `+ingestionColumns+`
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id WHERE i.job_id=?
    AND j.kind=? AND j.state='running' AND j.lease_token=? AND j.attempt_count=? AND j.lease_until>?`,
		claim.ID, IngestionJobKind, claim.LeaseToken, claim.AttemptCount, jobTime(time.Now())))
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrConflict
	}
	if err != nil {
		return false, err
	}
	if item.DispatchStarted || item.Status == "needs_text" || item.OriginalText == "" {
		return false, nil
	}
	if item.SourceOpeningID != "" {
		var latestID, mappedID sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT current_ingestion_id,opportunity_id FROM source_openings WHERE id=?`, item.SourceOpeningID).Scan(&latestID, &mappedID); err != nil {
			return false, err
		}
		if latestID.String != item.ID {
			return false, ErrConflict
		}
		if !mappedID.Valid {
			return false, nil
		}
	}
	var match IngestionOpportunityMatch
	var found bool
	if item.SourceOpeningID != "" {
		var mappedID sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT opportunity_id FROM source_openings WHERE id=?`, item.SourceOpeningID).Scan(&mappedID); err != nil {
			return false, err
		}
		if !mappedID.Valid {
			return false, nil
		}
		match, found, err = findMappedOpportunitySource(ctx, tx, mappedID.String, item.SourceURL, item.OriginalText)
	} else {
		match, found, err = findMatchingOpportunity(ctx, tx, item.SourceURL, item.OriginalText)
	}
	if err != nil || !found {
		return false, err
	}
	if err = recordIngestionResultTx(ctx, tx, claim, item, match.OpportunityID, match.RecordChangeID); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ingestion_requests SET status='completed',safe_error_code=NULL,updated_at=?
  WHERE id=? AND job_id=? AND opportunity_id=? AND record_change_id=? AND source_id IS NOT NULL`,
		utcNow(), item.ID, claim.ID, match.OpportunityID, match.RecordChangeID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return false, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
