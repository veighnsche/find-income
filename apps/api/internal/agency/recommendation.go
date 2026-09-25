package agency

import (
	"context"
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type recommendationTarget struct {
	Kind                  string `json:"kind"`
	ID                    string `json:"id"`
	Revision              int64  `json:"revision"`
	ContentSHA256         string `json:"contentSha256,omitempty"`
	OpportunityID         string `json:"opportunityId,omitempty"`
	OpportunityRevision   int64  `json:"opportunityRevision,omitempty"`
	OwnerDecisionRevision int64  `json:"ownerDecisionRevision,omitempty"`
	UpdatedAt             string `json:"updatedAt,omitempty"`
}

type recommendationSourceRef struct {
	ID                       string `json:"id"`
	Kind                     string `json:"kind"`
	Revision                 string `json:"revision"`
	OmittedBytes             int    `json:"omittedBytes"`
	OpportunityRevision      int64  `json:"opportunityRevision,omitempty"`
	OwnerDecisionRevision    int64  `json:"ownerDecisionRevision,omitempty"`
	ScreeningAssessmentID    string `json:"screeningAssessmentId,omitempty"`
	OrganisationAssessmentID string `json:"organisationAssessmentId,omitempty"`
	PackID                   string `json:"packId,omitempty"`
	PackVersion              int64  `json:"packVersion,omitempty"`
	PackContentSHA256        string `json:"packContentSha256,omitempty"`
}

// homeRecommendation is a snapshot of advice, never authority to execute it.
// The read endpoint verifies saved revisions against current records before
// offering an action; every action still passes through owner/start checks.
type homeRecommendation struct {
	Status               string                    `json:"status"`
	Code                 string                    `json:"code,omitempty"`
	Action               string                    `json:"action,omitempty"`
	Target               *recommendationTarget     `json:"target,omitempty"`
	ProfileVersion       int64                     `json:"profileVersion"`
	RoundID              string                    `json:"roundId"`
	RoundGeneration      int64                     `json:"roundGeneration"`
	Reason               string                    `json:"reason,omitempty"`
	CandidateID          string                    `json:"candidateId,omitempty"`
	DecisionAttemptID    string                    `json:"decisionAttemptId,omitempty"`
	DecisionInputSHA256  string                    `json:"decisionInputSha256,omitempty"`
	SourceRefs           []recommendationSourceRef `json:"sourceRefs,omitempty"`
	OmittedOpportunities bool                      `json:"omittedOpportunities,omitempty"`
	UnavailableActions   []string                  `json:"unavailableActions,omitempty"`
}

type recommendationAssessments struct {
	ScreeningAssessmentID    string
	OrganisationAssessmentID string
}

type recommendationChoice struct {
	Action string
	Target recommendationTarget
	Reason string
}

const homeRecommendationRequestKey = "home-recommendation"

func recommendationErrorCode(err error) string {
	if errors.Is(err, errDecisionProviderUnavailable) {
		return "recommendation_provider_unavailable"
	}
	if errors.Is(err, errDecisionContextTooLarge) {
		return "recommendation_context_too_large"
	}
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrFenced) {
		return "recommendation_state_changed"
	}
	if errors.Is(err, store.ErrAllowance) {
		return "recommendation_allowance_exhausted"
	}
	return terminalCode(err)
}

func (e *Engine) currentRecommendationAssessment(ctx context.Context, opportunity store.Opportunity, profileVersion int64, kind string) (store.RoundJevAssessment, bool, error) {
	assessment, err := e.Store.CurrentRoundJevAssessment(ctx, opportunity.ID, kind)
	if errors.Is(err, store.ErrNotFound) {
		return store.RoundJevAssessment{}, false, nil
	}
	if err != nil {
		return store.RoundJevAssessment{}, false, err
	}
	if assessment.OpportunityRevision != opportunity.Revision || assessment.ProfileVersion != profileVersion {
		return store.RoundJevAssessment{}, false, nil
	}
	return assessment, true, nil
}

func (e *Engine) checkRecommendationTarget(ctx context.Context, round store.Round, choice recommendationChoice) error {
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != round.ProfileVersion {
		return store.ErrConflict
	}
	target := choice.Target
	switch choice.Action {
	case "review_result":
		if target.Kind != "round" || target.ID != round.ID || target.Revision != 0 {
			return store.ErrConflict
		}
		return nil
	case "prepare", "review_pack":
		opportunityID := target.ID
		if choice.Action == "review_pack" {
			pack, packErr := e.Store.ApplicationPack(ctx, target.ID)
			if packErr != nil || pack.Version != target.Revision || pack.ContentSHA256 != target.ContentSHA256 || pack.ProfileRevision != profile.Version || pack.OpportunityRevision != target.OpportunityRevision {
				return store.ErrConflict
			}
			opportunityID = pack.OpportunityID
		}
		opportunity, opportunityErr := e.Store.Opportunity(ctx, opportunityID)
		if opportunityErr != nil || opportunity.ArchivedAt != "" || opportunity.Revision != func() int64 {
			if choice.Action == "prepare" {
				return target.Revision
			}
			return target.OpportunityRevision
		}() {
			return store.ErrConflict
		}
		decision, decisionErr := e.Store.OwnerOpportunityDecision(ctx, opportunityID)
		if decisionErr != nil || decision.Decision != "selected" || decision.Revision != target.OwnerDecisionRevision || decision.OpportunityRevision != opportunity.Revision {
			return store.ErrConflict
		}
		return nil
	default:
		return store.ErrInvalid
	}
}
