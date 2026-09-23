package agency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// The two measured decision shapes used 2,961 and 1,684 provider-reported
// tokens. A local default-batch fixture with four full 1,900-byte excerpts
// and complete profile needs 14,260 logical JSON bytes. The 24 KiB context
// guard and 10,000 reported-token cap leave bounded room for that intended
// input while retaining the provider-reported post-response check. Bytes are
// only a context-size guard, never a token estimate.
const discoveryDecisionReportedTokenLimit int64 = 10000
const discoveryDecisionLogicalByteLimit = 24 << 10

var errDecisionContextTooLarge = errors.New("decision context exceeds supported logical request bound")

// savedOrRunDecision never repeats a Jev dispatch at the same phase key. A
// successful response captured before a cursor pin is recovered only when its
// exact candidate/source set and profile still match the saved phase.
func (e *Engine) savedOrRunDecision(ctx context.Context, binding jevservice.Binding, input jev.DecisionInput) (string, error) {
	logical, err := jev.DecisionLogicalRequest(input)
	if err != nil {
		return "", err
	}
	if len(logical) > discoveryDecisionLogicalByteLimit {
		return "", errDecisionContextTooLarge
	}
	attempt, err := e.Store.RoundAttemptForRequest(ctx, binding.RoundID, binding.RequestKeyPrefix+"/0")
	if err == nil {
		if attempt.Operation != store.RoundJevRequest || attempt.State != store.AttemptSucceeded {
			return "", store.ErrUncertain
		}
		attempts, err := e.Store.JevAttemptsForRound(ctx, binding.RoundID)
		if err != nil {
			return "", err
		}
		refs, _ := json.Marshal(input.Sources)
		candidates, _ := json.Marshal(input.Candidates)
		for _, saved := range attempts {
			if saved.RoundAttemptID != attempt.ID {
				continue
			}
			if saved.Status != "succeeded" || saved.Purpose != string(input.Kind) || saved.ProfileVersion != binding.ProfileVersion ||
				saved.ResponseTruncated || saved.ResponseReadError || !bytes.Equal(saved.SourceRefsJSON, refs) || !bytes.Equal(saved.CandidateSetJSON, candidates) {
				return "", store.ErrConflict
			}
			var response struct {
				Answers map[string]struct {
					Choice string `json:"choice"`
				} `json:"answers"`
			}
			if json.Unmarshal(saved.RawResponseBytes, &response) != nil {
				return "", store.ErrUncertain
			}
			choice := response.Answers["selected_candidate"].Choice
			if choice == "__unresolved__" {
				return "", nil
			}
			for _, candidate := range input.Candidates {
				if candidate.ID == choice {
					return choice, nil
				}
			}
			return "", store.ErrFenced
		}
		return "", store.ErrUncertain
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	result, err := e.Decisions.RunDecision(ctx, binding, input)
	if err != nil {
		return "", err
	}
	if result.Disposition != jev.DecisionSelected {
		return "", nil
	}
	return result.SelectedID, nil
}
