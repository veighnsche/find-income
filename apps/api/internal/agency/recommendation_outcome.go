package agency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// outcomeRecommendationFacts contains only bounded record/status facts. No
// owner text, offer source, contact address, message, or report JSON enters a
// next-action request for these outcomes.
type outcomeRecommendationFacts struct {
	Outcome         string `json:"outcome"`
	Code            string `json:"code"`
	ResultID        string `json:"resultId"`
	ResultRevision  int64  `json:"resultRevision"`
	AppliedChanges  int    `json:"appliedChanges"`
	UnresolvedCount int    `json:"unresolvedCount"`
}

func (f outcomeRecommendationFacts) useful() bool {
	switch f.Outcome {
	case "process_input":
		return f.AppliedChanges > 0 || f.ResultID != "" && f.Code == "pack_ready"
	case "prepare":
		return f.ResultID != ""
	default:
		return false
	}
}

func outcomeFactsRevision(roundID string, facts outcomeRecommendationFacts) string {
	encoded, _ := json.Marshal(struct {
		RoundID string
		Facts   outcomeRecommendationFacts
	}{roundID, facts})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// computeOutcomeRecommendation uses the shared charged decision
// phase. Its input is deliberately a typed summary, never a serialized report.
func (e *Engine) computeOutcomeRecommendation(ctx context.Context, initial store.Round, facts outcomeRecommendationFacts) *homeRecommendation {
	advice := &homeRecommendation{Status: "unavailable", ProfileVersion: initial.ProfileVersion, RoundID: initial.ID, RoundGeneration: initial.Generation}
	if facts.Outcome != initial.Outcome || !facts.useful() {
		advice.Code = "no_useful_result"
		return advice
	}
	r, err := e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		advice.Code = terminalCode(err)
		return advice
	}
	prior, priorErr := e.Store.RoundAttemptForRequest(ctx, r.ID, homeRecommendationRequestKey+"/0")
	if priorErr != nil && !errors.Is(priorErr, store.ErrNotFound) {
		advice.Code = "recommendation_attempt_unavailable"
		return advice
	}
	if priorErr == nil && prior.State != store.AttemptSucceeded && prior.State != store.AttemptObservedSuccess {
		advice.Code = "recommendation_attempt_uncertain"
		return advice
	}
	if r.Limits.Requests-r.Used.Requests < 1 && priorErr != nil || !hasRoundResource(r.Scope.Resources, "campaign:active") || !slices.Contains(r.Scope.Operations, store.RoundJevRequest) {
		advice.Code = "recommendation_allowance_or_scope_unavailable"
		return advice
	}
	if priorErr != nil && e.Decisions == nil {
		advice.Code = "recommendation_provider_unavailable"
		return advice
	}
	input, choices, refs, unavailable, err := e.buildOutcomeRecommendationInput(ctx, r, facts)
	advice.SourceRefs, advice.UnavailableActions = refs, unavailable
	if err != nil {
		advice.Code = recommendationErrorCode(err)
		return advice
	}
	selectedID, err := e.savedOrRunDecision(ctx, jevservice.Binding{Actor: r.Actor, RoundID: r.ID, ResourceID: "campaign:active",
		RequestKeyPrefix: homeRecommendationRequestKey, ProfileVersion: r.ProfileVersion}, input)
	if err != nil {
		advice.Code = recommendationErrorCode(err)
		return advice
	}
	attempt, err := e.Store.RoundAttemptForRequest(ctx, r.ID, homeRecommendationRequestKey+"/0")
	if err != nil || attempt.State != store.AttemptSucceeded && attempt.State != store.AttemptObservedSuccess {
		advice.Code = "recommendation_attempt_unavailable"
		return advice
	}
	advice.DecisionAttemptID = attempt.ID
	jevAttempts, err := e.Store.JevAttemptsForRound(ctx, r.ID)
	if err != nil {
		advice.Code = "recommendation_evidence_unavailable"
		return advice
	}
	for _, saved := range jevAttempts {
		if saved.RoundAttemptID == attempt.ID && saved.Status == "succeeded" {
			sum := sha256.Sum256(saved.LogicalRequestJSON)
			advice.DecisionInputSHA256 = hex.EncodeToString(sum[:])
			break
		}
	}
	if advice.DecisionInputSHA256 == "" {
		advice.Code = "recommendation_evidence_unavailable"
		return advice
	}
	if selectedID == "" {
		advice.Status, advice.Code = "unresolved", "jev_abstained"
		return advice
	}
	choice, ok := choices[selectedID]
	if !ok {
		advice.Code = "recommendation_choice_invalid"
		return advice
	}
	if err := e.checkRecommendationTarget(ctx, r, choice); err != nil {
		advice.Code = "recommendation_target_changed"
		return advice
	}
	advice.Status, advice.Action, advice.Target = "selected", choice.Action, &choice.Target
	advice.Reason, advice.CandidateID = choice.Reason, selectedID
	return advice
}

func (e *Engine) buildOutcomeRecommendationInput(ctx context.Context, round store.Round, facts outcomeRecommendationFacts) (jev.DecisionInput, map[string]recommendationChoice, []recommendationSourceRef, []string, error) {
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != round.ProfileVersion {
		return jev.DecisionInput{}, nil, nil, nil, errProfileChanged
	}
	profileSources, profileIDs, complete := profileDecisionSources(profile, 4)
	if !complete {
		return jev.DecisionInput{}, nil, nil, nil, errDecisionContextTooLarge
	}
	input := jev.DecisionInput{Kind: jev.DecisionNextOutcome, CampaignIntent: round.Intent, MaxReportedTokens: decisionReportedTokenLimit,
		Capabilities: []jev.DecisionCapability{
			{ID: "home_review_result", Description: "Review the saved result of this commissioned work."},
			{ID: "home_prepare", Description: "Suggest owner-clicked preparation for one exact current owner-selected role."},
			{ID: "home_review_pack", Description: "Review one exact current saved application pack."},
		}, Sources: profileSources,
		RemainingAllowance: []jev.DecisionAllowance{{Operation: store.RoundJevRequest, Remaining: round.Limits.Requests - round.Used.Requests},
			{Operation: store.RoundSaveSourceOpportunity, Remaining: round.Limits.Items - round.Used.Items},
			{Operation: store.RoundContextTool, Remaining: round.Limits.Tools - round.Used.Tools},
			{Operation: store.RoundCodexTurn, Remaining: round.Limits.Turns - round.Used.Turns}}}
	refs := make([]recommendationSourceRef, 0, 12)
	for _, source := range profileSources {
		refs = append(refs, recommendationSourceRef{ID: source.ID, Kind: source.SourceKind, Revision: source.SourceRevision})
	}
	factsJSON, _ := json.Marshal(facts)
	if len(factsJSON) > 1200 {
		return jev.DecisionInput{}, nil, refs, nil, errDecisionContextTooLarge
	}
	resultID, resultRevision := "round:"+round.ID, outcomeFactsRevision(round.ID, facts)
	input.Sources = append(input.Sources, jev.DecisionSource{ID: resultID, SourceRevision: resultRevision,
		SourceKind: "commissioned_outcome_facts", Excerpt: string(factsJSON)})
	refs = append(refs, recommendationSourceRef{ID: resultID, Kind: "commissioned_outcome_facts", Revision: resultRevision})
	choices := map[string]recommendationChoice{}
	add := func(id, capability, description, scope string, sourceIDs []string, choice recommendationChoice) {
		input.Candidates = append(input.Candidates, jev.DecisionCandidate{ID: id, CapabilityID: capability, Description: description, Scope: scope, SourceIDs: sourceIDs})
		choices[id] = choice
	}
	baseRefs := append(append([]string{}, profileIDs...), resultID)

	reviewReason := fmt.Sprintf("The %s round saved %d applied changes and %d unresolved items.", facts.Outcome, facts.AppliedChanges, facts.UnresolvedCount)
	add("review-result", "home_review_result", "Review this round's saved result and unresolved items.", "Open the completed round without creating work.", baseRefs,
		recommendationChoice{Action: "review_result", Target: recommendationTarget{Kind: "round", ID: round.ID}, Reason: reviewReason})
	page, err := e.Store.ListOpportunities(ctx, store.OpportunityListOptions{Limit: 100})
	if err != nil || page.NextCursor != "" {
		return jev.DecisionInput{}, nil, refs, nil, errDecisionContextTooLarge
	}
	var unavailable []string
	selected := 0
	for _, opportunity := range page.Items {
		decision, err := e.Store.OwnerOpportunityDecision(ctx, opportunity.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return jev.DecisionInput{}, nil, refs, nil, err
		}
		if decision.Decision != "selected" || decision.OpportunityRevision != opportunity.Revision {
			continue
		}
		selected++
		if selected > 8 {
			return jev.DecisionInput{}, nil, refs, nil, errDecisionContextTooLarge
		}
		packs, err := e.Store.ListApplicationPacks(ctx, opportunity.ID)
		if err != nil {
			return jev.DecisionInput{}, nil, refs, nil, err
		}
		currentPack := len(packs) > 0 && packs[0].OpportunityRevision == opportunity.Revision && packs[0].ProfileRevision == profile.Version
		roleID := "opportunity:" + opportunity.ID
		roleSummary := fmt.Sprintf("owner-selected sourced role id=%s revision=%d; owner decision revision=%d; source present=%t; current pack=%t", opportunity.ID, opportunity.Revision, decision.Revision, opportunity.SourceURL != "" && opportunity.OriginalText != "", currentPack)
		packID, packHash := "", ""
		if currentPack {
			packID, packHash = packs[0].ID, packs[0].ContentSHA256
		}
		assessed := recommendationAssessments{}
		screen, screenPresent, screenErr := e.currentRecommendationAssessment(ctx, opportunity, profile.Version, "screening")
		organisation, organisationPresent, organisationErr := e.currentRecommendationAssessment(ctx, opportunity, profile.Version, "organisation")
		if screenErr != nil || organisationErr != nil {
			return jev.DecisionInput{}, nil, refs, nil, errors.Join(screenErr, organisationErr)
		}
		if screenPresent {
			assessed.ScreeningAssessmentID = screen.ID
		}
		if organisationPresent {
			assessed.OrganisationAssessmentID = organisation.ID
		}
		revision := recommendationOpportunityRevision(opportunity.Revision, decision, assessed, packID, packHash)
		input.Sources = append(input.Sources, jev.DecisionSource{ID: roleID, SourceRevision: revision, SourceKind: "current_opportunity_state", Excerpt: roleSummary})
		ref := recommendationSourceRef{ID: roleID, Kind: "current_opportunity_state", Revision: revision, OpportunityRevision: opportunity.Revision, OwnerDecisionRevision: decision.Revision,
			ScreeningAssessmentID: assessed.ScreeningAssessmentID, OrganisationAssessmentID: assessed.OrganisationAssessmentID}
		if currentPack {
			ref.PackID, ref.PackVersion, ref.PackContentSHA256 = packs[0].ID, packs[0].Version, packs[0].ContentSHA256
		}
		refs = append(refs, ref)
		roleRefs := append(append([]string{}, baseRefs...), roleID)
		if currentPack {
			id := "review-pack:" + packID
			add(id, "home_review_pack", "Review the current saved application pack for an owner-selected role.", "Open immutable pack only; no send authority.", roleRefs,
				recommendationChoice{Action: "review_pack", Target: recommendationTarget{Kind: "application_pack", ID: packID, Revision: packs[0].Version, ContentSHA256: packHash,
					OpportunityID: opportunity.ID, OpportunityRevision: opportunity.Revision, OwnerDecisionRevision: decision.Revision}, Reason: "A current saved pack is ready for owner review."})
		} else if opportunity.SourceURL != "" && opportunity.OriginalText != "" && e.checkPrepare(ctx) == nil {
			id := "prepare:" + opportunity.ID
			add(id, "home_prepare", "Prepare a reviewable pack for this current owner-selected sourced role.", "An owner click starts a new bounded prepare round; no send authority.", roleRefs,
				recommendationChoice{Action: "prepare", Target: recommendationTarget{Kind: "opportunity", ID: opportunity.ID, Revision: opportunity.Revision, OwnerDecisionRevision: decision.Revision}, Reason: "A current owner-selected sourced role has no matching application pack."})
		} else {
			unavailable = append(unavailable, "prepare_inputs_unavailable:"+strconv.Itoa(selected))
		}
	}
	if len(input.Sources) > 12 || len(input.Candidates) > 16 {
		return jev.DecisionInput{}, nil, refs, unavailable, errDecisionContextTooLarge
	}
	return input, choices, refs, unavailable, nil
}

// savedOutcomeRecommendationFacts reconstructs only the safe facts that were
// supplied to Jev. It runs locally on reads and never dispatches a provider.
func savedOutcomeRecommendationFacts(ctx context.Context, db *store.Store, round store.Round) (outcomeRecommendationFacts, error) {
	facts := outcomeRecommendationFacts{Outcome: round.Outcome}
	switch round.Outcome {
	case "process_input":
		var pack packReport
		if json.Unmarshal(round.Report, &pack) != nil {
			return facts, store.ErrInvalid
		}
		if pack.PackID != "" {
			facts.Code, facts.ResultID, facts.ResultRevision = pack.Code, pack.PackID, pack.Version
			return facts, nil
		}
		var input inputReport
		if json.Unmarshal(round.Report, &input) != nil {
			return facts, store.ErrInvalid
		}
		facts.Code, facts.ResultID = input.Code, round.ID
		facts.AppliedChanges, facts.UnresolvedCount = len(input.AppliedChanges), len(input.Unresolved)
	case "prepare":
		var pack packReport
		if json.Unmarshal(round.Report, &pack) != nil {
			return facts, store.ErrInvalid
		}
		facts.Code, facts.ResultID, facts.ResultRevision = pack.Code, pack.PackID, pack.Version
	default:
		return facts, store.ErrInvalid
	}
	return facts, nil
}
