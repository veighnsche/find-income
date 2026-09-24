package agency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// recoverOutcomeRecommendation handles the known local Jev phase before an
// outcome-specific reconciler can mistake it for remote Codex work.
func (e *Engine) recoverOutcomeRecommendation(ctx context.Context, round store.Round, attempt store.RoundAttempt) (bool, bool, error) {
	if attempt.RequestKey != homeRecommendationRequestKey+"/0" || attempt.Operation != store.RoundJevRequest {
		return false, false, nil
	}
	if attempt.ResourceID != "campaign:active" || attempt.State != store.AttemptUncertain || round.Used.Requests < 1 {
		return true, false, nil
	}
	captures, err := e.Store.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		return true, false, err
	}
	var capture *store.JevAttempt
	for i := range captures {
		if captures[i].RoundAttemptID == attempt.ID {
			if capture != nil {
				return true, false, nil
			}
			capture = &captures[i]
		}
	}
	if capture == nil || capture.Status != "succeeded" || capture.Purpose != string(jev.DecisionNextOutcome) ||
		capture.StepIndex != 0 || capture.RubricVersion != "decision-v1" || capture.ProfileVersion != round.ProfileVersion ||
		capture.ResponseTruncated || capture.ResponseReadError || capture.InputTokens == nil || capture.OutputTokens == nil {
		return true, false, nil
	}
	digest := sha256.Sum256(capture.LogicalRequestJSON)
	logicalSHA := hex.EncodeToString(digest[:])
	if logicalSHA != capture.InputSHA256 {
		return true, false, nil
	}
	var recorded struct {
		State jev.DecisionInput `json:"state"`
	}
	if json.Unmarshal(capture.LogicalRequestJSON, &recorded) != nil || recorded.State.Kind != jev.DecisionNextOutcome {
		return true, false, nil
	}
	var facts outcomeRecommendationFacts
	found := false
	for _, source := range recorded.State.Sources {
		if source.ID != "round:"+round.ID || source.SourceKind != "commissioned_outcome_facts" {
			continue
		}
		if found || json.Unmarshal([]byte(source.Excerpt), &facts) != nil || source.SourceRevision != outcomeFactsRevision(round.ID, facts) {
			return true, false, nil
		}
		found = true
	}
	if !found || facts.Outcome != round.Outcome || !facts.useful() {
		return true, false, nil
	}
	replayRound := round
	replayRound.Used.Requests-- // The captured request was charged after its logical input was made.
	input, choices, _, _, err := e.buildOutcomeRecommendationInput(ctx, replayRound, facts)
	if err != nil {
		return true, false, nil
	}
	logical, err := jev.DecisionLogicalRequest(input)
	refs, _ := json.Marshal(input.Sources)
	candidates, _ := json.Marshal(input.Candidates)
	if err != nil || !bytes.Equal(logical, capture.LogicalRequestJSON) || !bytes.Equal(refs, capture.SourceRefsJSON) ||
		!bytes.Equal(candidates, capture.CandidateSetJSON) {
		return true, false, nil
	}
	result, err := jev.RecoverCapturedDecision(input, capture.LogicalRequestJSON, capture.RawResponseBytes, capture.RequestedModel)
	if err != nil || result.ProviderResult.ReturnedModel != capture.ReturnedModel ||
		result.ProviderResult.Usage.InputTokens != *capture.InputTokens || result.ProviderResult.Usage.OutputTokens != *capture.OutputTokens {
		return true, false, nil
	}
	choiceID := result.SelectedID
	if result.Disposition == jev.DecisionUnresolved {
		choiceID = "__unresolved__"
	} else {
		choice, ok := choices[choiceID]
		if !ok || e.checkRecommendationTarget(ctx, round, nil, choice) != nil {
			return true, false, nil
		}
	}
	if err := e.Store.ResolveCapturedHomeRecommendationAttempt(ctx, round.ID, attempt.ID, capture.ID, round.Generation, logicalSHA, choiceID); err != nil {
		return true, false, err
	}
	return true, true, nil
}
