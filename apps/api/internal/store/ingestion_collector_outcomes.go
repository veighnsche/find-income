package store

import (
	"context"
	"database/sql"
	"errors"
)

// CollectorSourceOutcome is a bounded post-publication decision for one exact
// staged posting. New/changed current revisions are candidates for extraction;
// unchanged/older sightings do not require model work.
type CollectorSourceOutcome struct {
	PostingIndex    int
	SourceOpeningID string
	IngestionID     string
	Decision        string
	SourceURL       string
	ContentSHA256   string
	ObservedAt      string
	OpportunityID   string
	SourceID        string
	Current         bool
}

func (s *Store) RoundCollectorOutcomes(ctx context.Context, roundID, attemptID string) ([]CollectorSourceOutcome, error) {
	if roundID == "" || attemptID == "" {
		return nil, ErrInvalid
	}
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM round_attempts a
  JOIN round_collector_batches b ON b.attempt_id=a.id AND b.round_id=a.round_id
  WHERE a.id=? AND a.round_id=? AND a.operation='source.page' AND a.state='succeeded'`,
		attemptID, roundID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ss.posting_index,ss.source_opening_id,ss.ingestion_id,
  ss.decision,COALESCE(ss.source_url,''),ss.content_sha256,ss.observed_at,
  so.opportunity_id,i.source_id,so.current_ingestion_id
  FROM source_sightings ss JOIN source_openings so ON so.id=ss.source_opening_id
  JOIN ingestion_requests i ON i.id=ss.ingestion_id
  WHERE ss.collector_attempt_id=? ORDER BY ss.posting_index`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CollectorSourceOutcome, 0, 25)
	for rows.Next() {
		var item CollectorSourceOutcome
		var opportunityID, sourceID sql.NullString
		var currentIngestionID string
		if err := rows.Scan(&item.PostingIndex, &item.SourceOpeningID, &item.IngestionID,
			&item.Decision, &item.SourceURL, &item.ContentSHA256, &item.ObservedAt,
			&opportunityID, &sourceID, &currentIngestionID); err != nil {
			return nil, err
		}
		item.OpportunityID, item.SourceID = opportunityID.String, sourceID.String
		item.Current = item.IngestionID == currentIngestionID
		out = append(out, item)
	}
	return out, rows.Err()
}
