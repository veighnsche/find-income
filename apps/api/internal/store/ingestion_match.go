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

// findMatchingOpportunity looks only for one current, unarchived opportunity
// whose complete source URL and text equal this intake's source. Ambiguous
// duplicates require review rather than picking a record or merging content.
func findMatchingOpportunity(ctx context.Context, reader Reader, sourceURL, originalText string) (IngestionOpportunityMatch, bool, error) {
	if originalText == "" {
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
	var opportunityID, recordChangeID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(i.source_url,''),i.original_text,i.opportunity_id,i.record_change_id
  FROM ingestion_requests i JOIN jobs j ON j.id=i.job_id
  WHERE i.job_id=? AND j.kind=? AND j.state='running' AND j.lease_token=?
    AND j.attempt_count=? AND j.lease_until>?`, claim.ID, IngestionJobKind,
		claim.LeaseToken, claim.AttemptCount, jobTime(time.Now())).
		Scan(&sourceURL, &originalText, &opportunityID, &recordChangeID)
	if errors.Is(err, sql.ErrNoRows) {
		return IngestionOpportunityMatch{}, false, ErrConflict
	}
	if err != nil {
		return IngestionOpportunityMatch{}, false, err
	}
	if opportunityID.Valid && recordChangeID.Valid {
		return IngestionOpportunityMatch{OpportunityID: opportunityID.String, RecordChangeID: recordChangeID.String}, true, nil
	}
	return findMatchingOpportunity(ctx, s.db, sourceURL, originalText)
}
