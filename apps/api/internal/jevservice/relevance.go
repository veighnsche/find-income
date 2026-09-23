package jevservice

import (
	"context"
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
)

// AssessPackRelevance charges and captures the single Jev call used by the
// existing bounded pack helper. Caller pins the saved requirement and approved
// source version before invoking it. No model-forged relevance is accepted.
func (s Service) AssessPackRelevance(ctx context.Context, binding Binding, requirement string, source applicationpacks.Source, excerpt string) (applicationpacks.Relevance, error) {
	if err := s.ready(binding); err != nil {
		return applicationpacks.Relevance{}, err
	}
	if binding.MaxReportedTokens < 1 || binding.MaxReportedTokens > 1_000_000 {
		return applicationpacks.Relevance{}, &jev.Error{Kind: jev.ErrInvalidConfig}
	}
	refs, _ := json.Marshal(struct {
		ID      string `json:"source_id"`
		SHA256  string `json:"source_sha256"`
		Excerpt string `json:"excerpt"`
	}{source.ID, source.SHA256, excerpt})
	candidates, _ := json.Marshal(struct {
		Requirement string `json:"requirement"`
	}{requirement})
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: "pack_relevance", rubric: "pack-relevance-v1",
		sourceRefs: refs, candidates: candidates, maxReportedTokens: binding.MaxReportedTokens}
	result, err := applicationpacks.AssessRelevance(ctx, evaluator, requirement, source, excerpt)
	if err = evaluator.finish(ctx, err); err != nil {
		return applicationpacks.Relevance{}, err
	}
	return result, nil
}
