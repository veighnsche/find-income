package jevservice

import (
	"context"
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// RunReplyIntent captures and charges the single bounded reply-intent
// judgment. A repeated request key never replays a dispatched provider call.
func (s Service) RunReplyIntent(ctx context.Context, binding Binding, input jev.ReplyIntentInput) (jev.ReplyIntentResult, error) {
	if err := s.ready(binding); err != nil {
		return jev.ReplyIntentResult{}, err
	}
	if binding.MaxReportedTokens < 1 || binding.MaxReportedTokens > 2500 {
		return jev.ReplyIntentResult{}, &jev.Error{Kind: jev.ErrInvalidConfig}
	}
	input.MaxReportedTokens = binding.MaxReportedTokens
	refs, _ := json.Marshal(input.Context)
	candidates, _ := json.Marshal(input.Candidates)
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: "reply_intent", rubric: "reply-intent-v1", sourceRefs: refs, candidates: candidates, maxReportedTokens: binding.MaxReportedTokens}
	result, err := jev.SelectReplyIntent(ctx, evaluator, input)
	if err = evaluator.finish(ctx, err); err != nil {
		return jev.ReplyIntentResult{}, err
	}
	return result, nil
}
