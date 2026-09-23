package store

import (
	"context"
	"database/sql"
	"errors"
)

// InputOrganisationAssessment finds the organisation recorded by this exact
// commissioned round for its saved vacancy source.
func (s *Store) InputOrganisationAssessment(ctx context.Context, roundID, opportunityID, sourceID string) (RoundJevAssessment, error) {
	if roundID == "" || opportunityID == "" || sourceID == "" {
		return RoundJevAssessment{}, ErrInvalid
	}
	a, err := scanRoundJevAssessment(s.db.QueryRowContext(ctx, `SELECT `+roundJevAssessmentColumns+`
	  FROM round_jev_assessments WHERE round_id=? AND opportunity_id=? AND source_id=? AND kind='organisation'
	  ORDER BY created_at DESC,id DESC LIMIT 1`, roundID, opportunityID, sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return RoundJevAssessment{}, ErrNotFound
	}
	return a, err
}
