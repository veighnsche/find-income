package agency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
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

type recommendationChoice struct {
	Action string
	Target recommendationTarget
	Reason string
}

const homeRecommendationRequestKey = "home-recommendation"

func (e *Engine) computeHomeRecommendation(ctx context.Context, initial store.Round, detail report) *homeRecommendation {
	advice := &homeRecommendation{Status: "unavailable", ProfileVersion: initial.ProfileVersion, RoundID: initial.ID, RoundGeneration: initial.Generation}
	if len(detail.AssessedSources) == 0 {
		advice.Code = "no_assessed_source"
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
	if (r.Limits.Requests-r.Used.Requests < 1 && priorErr != nil) || !hasRoundResource(r.Scope.Resources, "campaign:active") || !slices.Contains(r.Scope.Operations, store.RoundJevRequest) {
		advice.Code = "recommendation_allowance_or_scope_unavailable"
		return advice
	}
	input, choices, refs, unavailable, omitted, err := e.buildHomeRecommendationInput(ctx, r, detail)
	advice.SourceRefs, advice.UnavailableActions, advice.OmittedOpportunities = refs, unavailable, omitted
	if err != nil {
		if omitted {
			advice.Code = "recommendation_context_unbounded"
		} else {
			advice.Code = recommendationErrorCode(err)
		}
		return advice
	}
	selectedID, err := e.savedOrRunDecision(ctx, jevservice.Binding{Actor: r.Actor, RoundID: r.ID, ResourceID: "campaign:active",
		RequestKeyPrefix: homeRecommendationRequestKey, ProfileVersion: r.ProfileVersion}, input)
	if err != nil {
		advice.Code = recommendationErrorCode(err)
		return advice
	}
	attempt, attemptErr := e.Store.RoundAttemptForRequest(ctx, r.ID, homeRecommendationRequestKey+"/0")
	if attemptErr != nil || attempt.State != store.AttemptSucceeded && attempt.State != store.AttemptObservedSuccess {
		advice.Code = "recommendation_attempt_unavailable"
		return advice
	}
	advice.DecisionAttemptID = attempt.ID
	jevAttempts, attemptsErr := e.Store.JevAttemptsForRound(ctx, r.ID)
	if attemptsErr != nil {
		advice.Code = "recommendation_evidence_unavailable"
		return advice
	}
	for _, jevAttempt := range jevAttempts {
		if jevAttempt.RoundAttemptID == attempt.ID && jevAttempt.Status == "succeeded" {
			digest := sha256.Sum256(jevAttempt.LogicalRequestJSON)
			advice.DecisionInputSHA256 = hex.EncodeToString(digest[:])
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
	choice, exists := choices[selectedID]
	if !exists {
		advice.Code = "recommendation_choice_invalid"
		return advice
	}
	// A choice is kept only if its profile, target and owner selection are still
	// current after Jev returned. The click path rechecks these again later.
	if err := e.checkRecommendationTarget(ctx, r, detail.AssessedSources, choice); err != nil {
		advice.Code = "recommendation_target_changed"
		return advice
	}
	advice.Status, advice.Action, advice.Target = "selected", choice.Action, &choice.Target
	advice.Reason, advice.CandidateID = choice.Reason, selectedID
	return advice
}

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

func (e *Engine) buildHomeRecommendationInput(ctx context.Context, r store.Round, detail report) (jev.DecisionInput, map[string]recommendationChoice, []recommendationSourceRef, []string, bool, error) {
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != r.ProfileVersion {
		return jev.DecisionInput{}, nil, nil, nil, false, errProfileChanged
	}
	profileSources, profileIDs, complete := profileDecisionSources(profile, 4)
	if !complete {
		return jev.DecisionInput{}, nil, nil, nil, false, fmt.Errorf("profile context exceeds four bounded sources: %w", store.ErrInvalid)
	}
	input := jev.DecisionInput{Kind: jev.DecisionNextOutcome, CampaignIntent: r.Intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
		Capabilities: []jev.DecisionCapability{
			{ID: "home_discover", Description: "Suggest an owner-clicked bounded discovery commission using the current campaign brief."},
			{ID: "home_review_opportunities", Description: "Open this commission's already saved sourced opportunity cards for owner review."},
			{ID: "home_prepare", Description: "Suggest an owner-clicked application preparation round for one exact current owner-selected role."},
			{ID: "home_review_pack", Description: "Open one existing current reviewable application pack; do not send it."},
		},
		Sources: profileSources,
		RemainingAllowance: []jev.DecisionAllowance{
			{Operation: store.RoundJevRequest, Remaining: r.Limits.Requests - r.Used.Requests},
			{Operation: store.RoundSaveSourceOpportunity, Remaining: r.Limits.Items - r.Used.Items},
			{Operation: store.RoundContextTool, Remaining: r.Limits.Tools - r.Used.Tools},
			{Operation: store.RoundCodexTurn, Remaining: r.Limits.Turns - r.Used.Turns},
		},
	}
	refs := make([]recommendationSourceRef, 0, 12)
	for _, source := range profileSources {
		refs = append(refs, recommendationSourceRef{ID: source.ID, Kind: source.SourceKind, Revision: source.SourceRevision})
	}
	choices := map[string]recommendationChoice{}
	progressRevision := recommendationProgressRevision(r.ID, detail)
	progressExcerpt := fmt.Sprintf("This commissioned discovery saved %d assessed source results; %d other candidates in its current batch remain unreviewed; collector has more=%t.",
		len(detail.AssessedSources), detail.UnreviewedCandidates, detail.CollectorHasMore)
	input.Sources = append(input.Sources, jev.DecisionSource{ID: "round:" + r.ID, SourceRevision: progressRevision, SourceKind: "commissioned_round_result", Excerpt: progressExcerpt})
	refs = append(refs, recommendationSourceRef{ID: "round:" + r.ID, Kind: "commissioned_round_result", Revision: progressRevision})
	add := func(id, description, scope, capability string, sourceIDs []string, choice recommendationChoice) {
		input.Candidates = append(input.Candidates, jev.DecisionCandidate{ID: id, Description: description, Scope: scope, CapabilityID: capability, SourceIDs: sourceIDs})
		choices[id] = choice
	}
	discoverReason := "This commission has saved its result; another bounded discovery round can be started from the current campaign brief."
	if detail.UnreviewedCandidates > 0 || detail.CollectorHasMore {
		discoverReason = fmt.Sprintf("This commission retained %d unreviewed candidates and a collector continuation is available=%t.", detail.UnreviewedCandidates, detail.CollectorHasMore)
	}
	add("discover", "Start another bounded discovery round if further sourced roles would be useful.",
		"Only the owner can commission a new round; this suggestion does not spend or replenish this round's allowance.", "home_discover", append(append([]string{}, profileIDs...), "round:"+r.ID),
		recommendationChoice{Action: "discover", Target: recommendationTarget{Kind: "campaign", ID: "active", Revision: profile.Version}, Reason: discoverReason})
	page, err := e.Store.ListOpportunities(ctx, store.OpportunityListOptions{Limit: 100})
	if err != nil || page.NextCursor != "" {
		return jev.DecisionInput{}, nil, refs, nil, true, fmt.Errorf("current opportunity inventory exceeds bounded review: %w", store.ErrInvalid)
	}
	byID := make(map[string]store.Opportunity, len(page.Items))
	for _, opportunity := range page.Items {
		byID[opportunity.ID] = opportunity
	}
	selected := make([]store.Opportunity, 0, 8)
	ownerDecisions := map[string]store.OwnerDecision{}
	for _, opportunity := range page.Items {
		decision, decisionErr := e.Store.OwnerOpportunityDecision(ctx, opportunity.ID)
		if decisionErr != nil && !errors.Is(decisionErr, store.ErrNotFound) {
			return jev.DecisionInput{}, nil, refs, nil, false, decisionErr
		}
		if decisionErr == nil {
			ownerDecisions[opportunity.ID] = decision
		}
		if decisionErr == nil && decision.Decision == "selected" && decision.OpportunityRevision == opportunity.Revision {
			selected = append(selected, opportunity)
		}
	}
	if len(selected) > 8 {
		return jev.DecisionInput{}, nil, refs, nil, true, fmt.Errorf("current selected-role alternatives exceed bounded review: %w", store.ErrInvalid)
	}
	needed := map[string]store.Opportunity{}
	for _, opportunity := range selected {
		needed[opportunity.ID] = opportunity
	}
	for _, assessed := range detail.AssessedSources {
		if opportunity, found := byID[assessed.OpportunityID]; found {
			needed[opportunity.ID] = opportunity
		} else {
			return jev.DecisionInput{}, nil, refs, nil, false, store.ErrConflict
		}
	}
	if len(profileSources)+1+len(needed) > 12 || len(needed) > 8 {
		return jev.DecisionInput{}, nil, refs, nil, true, fmt.Errorf("current source facts exceed bounded decision: %w", store.ErrInvalid)
	}
	assessedByID := map[string]assessedSource{}
	for _, assessed := range detail.AssessedSources {
		assessedByID[assessed.OpportunityID] = assessed
	}
	sourceIDs := map[string]string{}
	currentPacks := map[string]*store.ApplicationPackSummary{}
	for _, opportunity := range page.Items { // Stable created-at order, never a relevance sort.
		if _, neededNow := needed[opportunity.ID]; !neededNow {
			continue
		}
		decision := ownerDecisions[opportunity.ID]
		decisionBinding := "none"
		if decision.ID != "" {
			decisionBinding = "current"
			if decision.OpportunityRevision != opportunity.Revision {
				decisionBinding = "stale"
			}
		}
		assessed := assessedByID[opportunity.ID]
		screen, screenPresent, screenErr := e.currentRecommendationAssessment(ctx, opportunity, profile.Version, "screening")
		organisation, organisationPresent, organisationErr := e.currentRecommendationAssessment(ctx, opportunity, profile.Version, "organisation")
		if screenErr != nil || organisationErr != nil {
			return jev.DecisionInput{}, nil, refs, nil, false, errors.Join(screenErr, organisationErr)
		}
		if assessed.OpportunityID != "" && (assessed.ScreeningAssessmentID != "" || assessed.OrganisationAssessmentID != "") &&
			(!screenPresent || !organisationPresent || screen.ID != assessed.ScreeningAssessmentID || organisation.ID != assessed.OrganisationAssessmentID) {
			return jev.DecisionInput{}, nil, refs, nil, false, store.ErrConflict
		}
		if assessed.OpportunityID == "" && screenPresent && organisationPresent {
			assessed = assessedSource{OpportunityID: opportunity.ID, ScreeningAssessmentID: screen.ID, ScreeningStatus: screen.Status,
				OrganisationAssessmentID: organisation.ID, OrganisationStatus: organisation.Status, OmittedBytes: screen.OmittedBytes}
			assessed.EvidenceSummary, assessed.AssessmentObservationsOmitted = assessedEvidenceSummary(screen, organisation, screen.OmittedBytes)
		}
		packs, packErr := e.Store.ListApplicationPacks(ctx, opportunity.ID)
		if packErr != nil {
			return jev.DecisionInput{}, nil, refs, nil, false, packErr
		}
		packFact := "current pack=none"
		if len(packs) > 0 && packs[0].OpportunityRevision == opportunity.Revision && packs[0].ProfileRevision == profile.Version {
			currentPacks[opportunity.ID] = &packs[0]
			packFact = fmt.Sprintf("current pack id=%s version=%d sha256=%s opportunityRevision=%d profileVersion=%d", packs[0].ID, packs[0].Version, packs[0].ContentSHA256, packs[0].OpportunityRevision, packs[0].ProfileRevision)
		}
		excerpt := fmt.Sprintf("Saved current role %q, opportunity revision %d; source URL %s; owner decision=%s id=%s revision=%d opportunityRevision=%d binding=%s; screening assessment=%s status=%s; organisation assessment=%s status=%s; %s; observations: %s. ",
			opportunity.Title, opportunity.Revision, opportunity.SourceURL, decision.Decision, decision.ID, decision.Revision, decision.OpportunityRevision, decisionBinding,
			assessed.ScreeningAssessmentID, assessed.ScreeningStatus, assessed.OrganisationAssessmentID, assessed.OrganisationStatus, packFact, assessed.EvidenceSummary)
		remaining := 1900 - len(excerpt)
		if remaining < 0 {
			return jev.DecisionInput{}, nil, refs, nil, true, fmt.Errorf("role facts exceed bounded source: %w", store.ErrInvalid)
		}
		sourceText := prefixUTF8(opportunity.OriginalText, remaining)
		omitted := len(opportunity.OriginalText) - len(sourceText)
		excerpt += sourceText
		if omitted > 0 {
			excerpt += fmt.Sprintf(" [source bytes omitted=%d; complete saved opportunity remains available]", omitted)
		}
		if len(excerpt) > 2000 {
			return jev.DecisionInput{}, nil, refs, nil, true, fmt.Errorf("role source exceeds decision bound: %w", store.ErrInvalid)
		}
		id := "opportunity:" + opportunity.ID
		packID, packHash := "", ""
		if pack := currentPacks[opportunity.ID]; pack != nil {
			packID, packHash = pack.ID, pack.ContentSHA256
		}
		revision := recommendationOpportunityRevision(opportunity.Revision, decision, assessed, packID, packHash)
		input.Sources = append(input.Sources, jev.DecisionSource{ID: id, SourceRevision: revision, SourceKind: "current_opportunity_state",
			URL: opportunity.SourceURL, ObservedAt: opportunity.UpdatedAt, Excerpt: excerpt})
		ref := recommendationSourceRef{ID: id, Kind: "current_opportunity_state", Revision: revision, OmittedBytes: omitted,
			OpportunityRevision: opportunity.Revision, OwnerDecisionRevision: decision.Revision,
			ScreeningAssessmentID: assessed.ScreeningAssessmentID, OrganisationAssessmentID: assessed.OrganisationAssessmentID}
		if pack := currentPacks[opportunity.ID]; pack != nil {
			ref.PackID, ref.PackVersion, ref.PackContentSHA256 = pack.ID, pack.Version, pack.ContentSHA256
		}
		refs = append(refs, ref)
		sourceIDs[opportunity.ID] = id
	}
	if len(detail.AssessedSources) > 0 {
		ids := append([]string{}, profileIDs...)
		ids = append(ids, "round:"+r.ID)
		for _, assessed := range detail.AssessedSources {
			ids = append(ids, sourceIDs[assessed.OpportunityID])
		}
		reason := fmt.Sprintf("This commission saved %d sourced role assessments for owner review; assessment omissions are shown with each result.", len(detail.AssessedSources))
		add("review-round", "Review the sourced roles assessed in this commission.", "Open the saved round opportunity cards; only the owner may select a role to pursue.",
			"home_review_opportunities", ids, recommendationChoice{Action: "review_opportunities", Target: recommendationTarget{Kind: "round", ID: r.ID, Revision: r.Generation}, Reason: reason})
	}
	var unavailable []string
	prepareReady := false
	if len(selected) > 0 {
		prepareReady = e.checkPrepare(ctx) == nil
		if !prepareReady {
			unavailable = append(unavailable, "prepare_runtime_or_approved_sources_unavailable")
		}
	}
	for _, opportunity := range selected {
		decision := ownerDecisions[opportunity.ID]
		currentPack := currentPacks[opportunity.ID]
		refsForRole := append(append([]string{}, profileIDs...), sourceIDs[opportunity.ID])
		if currentPack != nil {
			id := "review-pack:" + currentPack.ID
			reason := fmt.Sprintf("Owner-selected role %q has current reviewable pack version %d for opportunity revision %d and profile version %d.", opportunity.Title, currentPack.Version, opportunity.Revision, profile.Version)
			add(id, "Review the current saved application pack for "+opportunity.Title+".", "Open the immutable pack for review; do not approve or send it.",
				"home_review_pack", refsForRole, recommendationChoice{Action: "review_pack", Target: recommendationTarget{Kind: "application_pack", ID: currentPack.ID, Revision: currentPack.Version,
					OpportunityID: opportunity.ID,
					ContentSHA256: currentPack.ContentSHA256, OpportunityRevision: opportunity.Revision, OwnerDecisionRevision: decision.Revision}, Reason: reason})
		} else if prepareReady && opportunity.SourceURL != "" && opportunity.OriginalText != "" {
			id := "prepare:" + opportunity.ID
			reason := fmt.Sprintf("Owner selected role %q at current opportunity revision %d; no current pack matches that role and profile.", opportunity.Title, opportunity.Revision)
			add(id, "Prepare a reviewable application pack for the owner-selected role "+opportunity.Title+".",
				"An owner click starts the existing bounded prepare round for this exact selected role; no sending authority is granted.",
				"home_prepare", refsForRole, recommendationChoice{Action: "prepare", Target: recommendationTarget{Kind: "opportunity", ID: opportunity.ID, Revision: opportunity.Revision,
					OwnerDecisionRevision: decision.Revision}, Reason: reason})
		}
	}
	if len(input.Candidates) > 16 {
		return jev.DecisionInput{}, nil, refs, unavailable, true, fmt.Errorf("supported next actions exceed bounded choice set: %w", store.ErrInvalid)
	}
	return input, choices, refs, unavailable, false, nil
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

func (e *Engine) checkRecommendationTarget(ctx context.Context, round store.Round, assessed []assessedSource, choice recommendationChoice) error {
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != round.ProfileVersion {
		return store.ErrConflict
	}
	target := choice.Target
	switch choice.Action {
	case "discover":
		if target.Revision != profile.Version || target.Kind != "campaign" || target.ID != "active" {
			return store.ErrConflict
		}
		return nil
	case "review_result":
		if target.Kind != "round" || target.ID != round.ID || target.Revision != 0 {
			return store.ErrConflict
		}
		return nil
	case "review_opportunities":
		expectedGeneration := round.Generation
		if round.State == store.RoundCompleted {
			expectedGeneration-- // FinishRound fences the completed worker generation.
		}
		if target.Kind != "round" || target.ID != round.ID || target.Revision != expectedGeneration {
			return store.ErrConflict
		}
		for _, item := range assessed {
			opportunity, opportunityErr := e.Store.Opportunity(ctx, item.OpportunityID)
			if opportunityErr != nil || opportunity.ArchivedAt != "" {
				return store.ErrConflict
			}
			screen, screenErr := e.Store.CurrentRoundJevAssessment(ctx, opportunity.ID, "screening")
			organisation, organisationErr := e.Store.CurrentRoundJevAssessment(ctx, opportunity.ID, "organisation")
			if screenErr != nil || organisationErr != nil || screen.ID != item.ScreeningAssessmentID || organisation.ID != item.OrganisationAssessmentID ||
				screen.OpportunityRevision != opportunity.Revision || organisation.OpportunityRevision != opportunity.Revision ||
				screen.ProfileVersion != profile.Version || organisation.ProfileVersion != profile.Version {
				return store.ErrConflict
			}
		}
		return nil
	case "review_comparison":
		if target.Kind != "offer_comparison" || target.Revision != 1 {
			return store.ErrConflict
		}
		comparison, err := e.Store.OfferComparisonForOwner(ctx, round.Actor, target.ID)
		if err != nil || !comparison.Current || comparison.RoundID != round.ID || comparison.Comparison.InputSHA256 != target.ContentSHA256 {
			return store.ErrConflict
		}
		return nil
	case "review_delivery":
		if target.Kind != "delivery_review" || target.ID == "" || target.Revision < 1 {
			return store.ErrConflict
		}
		review, err := e.Store.DeliveryReview(ctx, target.ID)
		if err != nil {
			return store.ErrConflict
		}
		recorded := int64(0)
		for _, item := range review.Items {
			if item.RoundID == round.ID && (item.State == "accepted_by_smtp" || item.State == "failed" || item.State == "uncertain") {
				recorded++
			}
		}
		if recorded != target.Revision {
			return store.ErrConflict
		}
		return nil
	case "review_interview":
		if target.Kind != "interview" || target.ID == "" || target.UpdatedAt == "" {
			return store.ErrConflict
		}
		interview, err := e.Store.Interview(ctx, target.ID)
		if err != nil || !interview.Current || interview.RoundID != round.ID || interview.UpdatedAt != target.UpdatedAt || len(interview.Brief) == 0 {
			return store.ErrConflict
		}
		return nil
	case "review_debrief":
		if target.Kind != "interview_debrief" || target.ID == "" || target.UpdatedAt == "" {
			return store.ErrConflict
		}
		debrief, err := e.Store.InterviewDebrief(ctx, target.ID)
		if err != nil || debrief.RoundID != round.ID || debrief.UpdatedAt != target.UpdatedAt || len(debrief.Debrief) == 0 {
			return store.ErrConflict
		}
		interview, err := e.Store.Interview(ctx, debrief.InterviewID)
		if err != nil || !interview.Current {
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
