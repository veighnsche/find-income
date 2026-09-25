package jevservice

import (
	"context"
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// RunAnswerMatch judges one batch of saved employer questions against pinned
// approved-answer candidates through the recording evaluator: one charged Jev
// call per reservation, zero Codex/LLM involvement.
func (s Service) RunAnswerMatch(ctx context.Context, binding Binding, input jev.AnswerMatchInput) (jev.AnswerMatchResult, error) {
	if err := s.ready(binding); err != nil {
		return jev.AnswerMatchResult{}, err
	}
	refs, _ := json.Marshal(struct {
		CheckID             string `json:"check_id"`
		QuestionSetSHA256   string `json:"question_set_sha256"`
		AnswerCatalogDigest string `json:"answer_catalog_digest"`
	}{input.CheckID, input.QuestionSetSHA256, input.AnswerCatalogDigest})
	candidates, _ := json.Marshal(input.Questions)
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: jev.AnswerMatchPurpose,
		rubric: jev.AnswerMatchRubricVersion, sourceRefs: refs, candidates: candidates,
		maxReportedTokens: input.MaxReportedTokens}
	result, err := jev.MatchAnswers(ctx, evaluator, input)
	if err = evaluator.finish(ctx, err); err != nil {
		return jev.AnswerMatchResult{}, err
	}
	return result, nil
}
