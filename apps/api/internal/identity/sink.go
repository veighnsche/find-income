package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/jevassess"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// StoreSink persists T09 dynamic-assessment records against
// jev_assessments_dynamic plus one jev_assessment_captures link row per
// cited span, atomically. Actor scope resolves from the asking run; a
// reassessment links its predecessor via SupersedesID (T04 B01).
type StoreSink struct {
	Store *store.Store
}

var _ jevassess.AssessmentSink = (*StoreSink)(nil)

// evidenceSpan is one canonical evidence ref as the Jev handler encodes it
// (snake_case capture_id/span_start/span_end).
type evidenceSpan struct {
	CaptureID string `json:"capture_id"`
	SpanStart int64  `json:"span_start"`
	SpanEnd   int64  `json:"span_end"`
}

// SaveDynamicAssessment implements jevassess.AssessmentSink.
func (s *StoreSink) SaveDynamicAssessment(ctx context.Context, rec jevassess.DynamicAssessmentRecord) error {
	if s == nil || s.Store == nil {
		return errors.New("identity sink missing its store")
	}
	spans, err := parseEvidenceSpans(rec.EvidenceRefsJSON)
	if err != nil {
		return err
	}
	return s.Store.ResearchWrite(ctx, func(db store.ResearchDB) error {
		actor, err := store.ResearchRoundActor(ctx, db, rec.RunID)
		if err != nil {
			return fmt.Errorf("resolve assessment actor: %w", err)
		}
		if rec.SupersedesID != "" {
			if _, err := store.GetDynamicAssessment(ctx, db, rec.SupersedesID); err != nil {
				return fmt.Errorf("%w: unknown superseded assessment %q", store.ErrInvalid, rec.SupersedesID)
			}
		}
		row, err := store.InsertSeededDynamicAssessment(ctx, db, actor, rec.ID, store.DynamicAssessmentInput{
			RoundID:          rec.RunID,
			JevAttemptID:     rec.JevAttemptID,
			Purpose:          rec.Purpose,
			QuestionsJSON:    string(rec.QuestionsJSON),
			EvidenceRefsJSON: string(rec.EvidenceRefsJSON),
			ProfileVersion:   rec.ProfileVersion,
			RubricVersion:    rec.RubricVersion,
			CandidatesJSON:   string(rec.CandidatesJSON),
			CandidateSetHash: rec.CandidateSetHash,
			RequestedModel:   rec.RequestedModel,
			ReuseKey:         rec.ReuseKey,
			Status:           rec.Status,
			AnswersJSON:      string(rec.AnswersJSON),
			SupersedesID:     rec.SupersedesID,
		})
		if err != nil {
			return err
		}
		for _, span := range spans {
			if err := store.LinkAssessmentCapture(ctx, db, store.AssessmentCaptureLink{
				AssessmentID: row.ID, CaptureID: span.CaptureID,
				SpanStart: span.SpanStart, SpanEnd: span.SpanEnd,
			}); err != nil {
				return fmt.Errorf("link assessment capture: %w", err)
			}
		}
		return nil
	})
}

// parseEvidenceSpans decodes the handler's canonical evidence refs. An
// assessment without bound evidence is refused: the refs are the binding
// the link table exists to index.
func parseEvidenceSpans(raw []byte) ([]evidenceSpan, error) {
	var spans []evidenceSpan
	if err := json.Unmarshal(raw, &spans); err != nil || len(spans) == 0 {
		return nil, fmt.Errorf("%w: assessment needs bound evidence refs", store.ErrInvalid)
	}
	for _, span := range spans {
		if span.CaptureID == "" || span.SpanStart < 0 || span.SpanEnd <= span.SpanStart {
			return nil, fmt.Errorf("%w: bad assessment evidence span", store.ErrInvalid)
		}
	}
	return spans, nil
}
