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

	"github.com/veighnsche/find-income-dashboard/api/internal/deliveryservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// RecommendDelivery is the narrow delivery-service seam. Only counts and a
// saved review ID cross into the commissioned Jev choice.
func (e *Engine) RecommendDelivery(ctx context.Context, round store.Round, summary deliveryservice.DeliveryAdviceFacts) json.RawMessage {
	facts := outcomeRecommendationFacts{Outcome: round.Outcome, Code: "delivery_attempts_recorded", ResultID: summary.ReviewID,
		RecordedDelivery: summary.Recorded, FailedDelivery: summary.Failed, UncertainDelivery: summary.Uncertain}
	encoded, _ := json.Marshal(e.computeOutcomeRecommendation(ctx, round, facts))
	return encoded
}

// outcomeRecommendationFacts contains only bounded record/status facts. No
// owner text, offer source, contact address, message, or report JSON enters a
// next-action request for these outcomes.
type outcomeRecommendationFacts struct {
	Outcome           string `json:"outcome"`
	Code              string `json:"code"`
	ResultID          string `json:"resultId"`
	ResultRevision    int64  `json:"resultRevision"`
	ResultUpdatedAt   string `json:"resultUpdatedAt,omitempty"`
	FocusSaved        bool   `json:"focusSaved,omitempty"`
	AppliedChanges    int    `json:"appliedChanges"`
	UnresolvedCount   int    `json:"unresolvedCount"`
	TradeoffStatus    string `json:"tradeoffStatus"`
	RecordedDelivery  int    `json:"recordedDelivery"`
	FailedDelivery    int    `json:"failedDelivery"`
	UncertainDelivery int    `json:"uncertainDelivery"`
}

func (f outcomeRecommendationFacts) useful() bool {
	switch f.Outcome {
	case "process_input":
		return f.AppliedChanges > 0 || f.ResultID != "" && f.Code == "pack_ready"
	case "prepare":
		return f.ResultID != ""
	case "compare_offers":
		return f.ResultID != ""
	case "deliver":
		return f.RecordedDelivery > 0
	case "interview_prepare", "interview_debrief":
		return f.ResultID != "" && f.ResultUpdatedAt != ""
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

// computeOutcomeRecommendation shares discovery's charged, fenced decision
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
	if err := e.checkRecommendationTarget(ctx, r, nil, choice); err != nil {
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
	input := jev.DecisionInput{Kind: jev.DecisionNextOutcome, CampaignIntent: round.Intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
		Capabilities: []jev.DecisionCapability{
			{ID: "home_discover", Description: "Suggest an owner-clicked bounded discovery commission."},
			{ID: "home_review_result", Description: "Review the saved result of this commissioned work."},
			{ID: "home_prepare", Description: "Suggest owner-clicked preparation for one exact current owner-selected role."},
			{ID: "home_review_pack", Description: "Review one exact current saved application pack without sending it."},
			{ID: "home_review_comparison", Description: "Review the saved offer comparison without accepting an offer."},
			{ID: "home_review_delivery", Description: "Review recorded delivery submission states without claiming receipt."},
			{ID: "home_review_interview", Description: "Review the saved sourced interview brief without booking or messaging."},
			{ID: "home_review_debrief", Description: "Review the saved owner-reported debrief without contacting anyone."},
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
	add("discover", "home_discover", "Start a new bounded source discovery round if more roles would be useful.", "Owner click commissions a new round; no work starts from this advice.", baseRefs,
		recommendationChoice{Action: "discover", Target: recommendationTarget{Kind: "campaign", ID: "active", Revision: profile.Version}, Reason: "The saved outcome is complete; a new owner-clicked discovery round can seek more sourced roles."})
	add("review-result", "home_review_result", "Review this round's saved result and unresolved items.", "Open the completed round without creating work.", baseRefs,
		recommendationChoice{Action: "review_result", Target: recommendationTarget{Kind: "round", ID: round.ID}, Reason: fmt.Sprintf("The %s round saved %d applied changes and %d unresolved items.", facts.Outcome, facts.AppliedChanges, facts.UnresolvedCount)})
	if facts.Outcome == "compare_offers" {
		comparison, err := e.Store.OfferComparisonForOwner(ctx, round.Actor, facts.ResultID)
		pausedPending := round.State == store.RoundPaused && facts.TradeoffStatus == "pending" && comparison.TradeoffStatus == "uncertain"
		if err != nil || !comparison.Current || comparison.RoundID != round.ID || comparison.TradeoffStatus != facts.TradeoffStatus && !pausedPending {
			return jev.DecisionInput{}, nil, refs, nil, store.ErrConflict
		}
		summary := comparisonRecommendationSummary(comparison, facts.TradeoffStatus)
		sum := sha256.Sum256([]byte(summary))
		revision := hex.EncodeToString(sum[:])
		id := "comparison:" + comparison.ID
		input.Sources = append(input.Sources, jev.DecisionSource{ID: id, SourceRevision: revision, SourceKind: "saved_comparison_facts", Excerpt: summary})
		refs = append(refs, recommendationSourceRef{ID: id, Kind: "saved_comparison_facts", Revision: revision})
		add("review-comparison", "home_review_comparison", "Review exact cited comparison and unknown terms before any owner decision.", "Open saved comparison; no accept or contact authority.", append(append([]string{}, baseRefs...), id),
			recommendationChoice{Action: "review_comparison", Target: recommendationTarget{Kind: "offer_comparison", ID: comparison.ID, Revision: 1, ContentSHA256: comparison.Comparison.InputSHA256}, Reason: "A current cited offer comparison is saved for owner review; no employer decision has been made."})
	}
	if facts.Outcome == "deliver" {
		add("review-delivery", "home_review_delivery", "Review recorded submission states and unresolved delivery uncertainty.", "Open saved delivery review; never resend from advice.", baseRefs,
			recommendationChoice{Action: "review_delivery", Target: recommendationTarget{Kind: "delivery_review", ID: facts.ResultID, Revision: int64(facts.RecordedDelivery)}, Reason: fmt.Sprintf("This commissioned delivery recorded %d item states; %d failed and %d remain uncertain. Employer receipt is unverified.", facts.RecordedDelivery, facts.FailedDelivery, facts.UncertainDelivery)})
	}
	if facts.Outcome == "interview_prepare" || facts.Outcome == "interview_debrief" {
		kind, action, capability := "interview", "review_interview", "home_review_interview"
		if facts.Outcome == "interview_debrief" {
			kind, action, capability = "interview_debrief", "review_debrief", "home_review_debrief"
		}
		add("review-"+facts.Outcome, capability, "Review the exact saved interview result and its stated unknown count.", "Open the saved record; no book, message, or send authority.", baseRefs,
			recommendationChoice{Action: action, Target: recommendationTarget{Kind: kind, ID: facts.ResultID, UpdatedAt: facts.ResultUpdatedAt},
				Reason: fmt.Sprintf("The %s commission saved a sourced result with %d stated unknowns; focus saved=%t.", facts.Outcome, facts.UnresolvedCount, facts.FocusSaved)})
	}
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
		assessed := assessedSource{}
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

func comparisonRecommendationSummary(comparison store.SavedOfferComparison, tradeoffStatus string) string {
	return fmt.Sprintf("saved offer comparison id=%s; current=%t; cited offers=%d; stated pay comparisons=%d; missing-term groups=%d; tradeoff status=%s",
		comparison.ID, comparison.Current, len(comparison.Comparison.Input.Offers), len(comparison.Comparison.Pay), len(comparison.Comparison.Missing), tradeoffStatus)
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
	case "compare_offers":
		var offer offerReport
		if json.Unmarshal(round.Report, &offer) != nil {
			return facts, store.ErrInvalid
		}
		facts.Code, facts.ResultID, facts.TradeoffStatus = offer.Code, offer.ComparisonID, offer.TradeoffStatus
	case "deliver":
		if len(round.Scope.InputRefs) != 1 || len(round.Scope.InputRefs[0]) <= len("delivery_review:") ||
			round.Scope.InputRefs[0][:len("delivery_review:")] != "delivery_review:" {
			return facts, store.ErrInvalid
		}
		facts.Code, facts.ResultID = "delivery_attempts_recorded", round.Scope.InputRefs[0][len("delivery_review:"):]
		review, err := db.DeliveryReview(ctx, facts.ResultID)
		if err != nil {
			return facts, err
		}
		for _, item := range review.Items {
			if item.RoundID != round.ID {
				continue
			}
			switch item.State {
			case "accepted_by_smtp":
				facts.RecordedDelivery++
			case "failed":
				facts.RecordedDelivery++
				facts.FailedDelivery++
			case "uncertain":
				facts.RecordedDelivery++
				facts.UncertainDelivery++
			}
		}
	case "interview_prepare":
		var outcome interviewOutcome
		if json.Unmarshal(round.Report, &outcome) != nil || !outcome.BriefSaved || outcome.InterviewID == "" {
			return facts, store.ErrInvalid
		}
		interview, err := db.Interview(ctx, outcome.InterviewID)
		if err != nil || !interview.Current || interview.RoundID != round.ID || len(interview.Brief) == 0 {
			return facts, store.ErrConflict
		}
		facts.Code, facts.ResultID, facts.ResultUpdatedAt, facts.FocusSaved, facts.UnresolvedCount =
			outcome.Code, outcome.InterviewID, interview.UpdatedAt, outcome.FocusSaved, len(outcome.Unknowns)
	case "interview_debrief":
		var outcome interviewOutcome
		if json.Unmarshal(round.Report, &outcome) != nil || !outcome.BriefSaved || outcome.DebriefID == "" {
			return facts, store.ErrInvalid
		}
		debrief, err := db.InterviewDebrief(ctx, outcome.DebriefID)
		if err != nil || debrief.RoundID != round.ID || len(debrief.Debrief) == 0 {
			return facts, store.ErrConflict
		}
		interview, err := db.Interview(ctx, debrief.InterviewID)
		if err != nil || !interview.Current {
			return facts, store.ErrConflict
		}
		facts.Code, facts.ResultID, facts.ResultUpdatedAt, facts.UnresolvedCount =
			outcome.Code, outcome.DebriefID, debrief.UpdatedAt, len(outcome.Unknowns)
	default:
		return facts, store.ErrInvalid
	}
	return facts, nil
}
