package jevservice

import (
	"context"
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// RunInterviewFocus captures and charges the single bounded focus judgment.
// A repeated request key never replays a dispatched provider call.
func (s Service) RunInterviewFocus(ctx context.Context, binding Binding, input jev.InterviewFocusInput) (jev.InterviewFocusResult, error) {
	if err := s.ready(binding); err != nil {
		return jev.InterviewFocusResult{}, err
	}
	if binding.MaxReportedTokens < 1 || binding.MaxReportedTokens > 2500 {
		return jev.InterviewFocusResult{}, &jev.Error{Kind: jev.ErrInvalidConfig}
	}
	input.MaxReportedTokens = binding.MaxReportedTokens
	refs, _ := json.Marshal(input.Context)
	candidates, _ := json.Marshal(input.Candidates)
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: "interview_focus", rubric: "interview-focus-v1", sourceRefs: refs, candidates: candidates, maxReportedTokens: binding.MaxReportedTokens}
	result, err := jev.SelectInterviewFocus(ctx, evaluator, input)
	if err = evaluator.finish(ctx, err); err != nil {
		return jev.InterviewFocusResult{}, err
	}
	return result, nil
}
