package agency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// HomeRecommendationCurrentness is computed for a read response. It never
// changes the saved report or authorizes the recommended action.
type HomeRecommendationCurrentness struct {
	Status    string `json:"status"` // current, stale, unavailable
	Code      string `json:"code"`
	CheckedAt string `json:"checkedAt"`
}

func recommendationProgressRevision(roundID string, detail report) string {
	progress := struct {
		RoundID              string
		CollectorAttemptID   string
		CollectorHasMore     bool
		UnreviewedCandidates int
		AssessedSources      []assessedSource
	}{roundID, detail.CollectorAttemptID, detail.CollectorHasMore, detail.UnreviewedCandidates, detail.AssessedSources}
	encoded, _ := json.Marshal(progress)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func recommendationOpportunityRevision(revision int64, decision store.OwnerDecision, assessed assessedSource, packID, packHash string) string {
	fingerprint, _ := json.Marshal(struct {
		OpportunityRevision int64
		DecisionID          string
		DecisionRevision    int64
		ScreenID            string
		OrganisationID      string
		PackID              string
		PackHash            string
	}{revision, decision.ID, decision.Revision, assessed.ScreeningAssessmentID, assessed.OrganisationAssessmentID, packID, packHash})
	digest := sha256.Sum256(fingerprint)
	return hex.EncodeToString(digest[:])
}

// ReadHomeRecommendationCurrentness uses only current local store reads. An
// unknown read is unavailable, while a proven changed snapshot is stale.
func ReadHomeRecommendationCurrentness(ctx context.Context, db *store.Store, round store.Round) HomeRecommendationCurrentness {
	verdict := HomeRecommendationCurrentness{Status: "unavailable", Code: "no_saved_recommendation", CheckedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if db == nil || ctx == nil || round.ID == "" || round.Outcome != "discover" && round.Outcome != "process_input" && round.Outcome != "prepare" && round.Outcome != "compare_offers" && round.Outcome != "deliver" && round.Outcome != "interview_prepare" && round.Outcome != "interview_debrief" {
		return verdict
	}
	var detail report
	var saved struct {
		Recommendation *homeRecommendation `json:"recommendation"`
	}
	if len(round.Report) == 0 || json.Unmarshal(round.Report, &saved) != nil {
		verdict.Code = "invalid_saved_report"
		return verdict
	}
	if round.Outcome == "discover" && json.Unmarshal(round.Report, &detail) != nil {
		verdict.Code = "invalid_saved_report"
		return verdict
	}
	advice := saved.Recommendation
	if advice == nil {
		return verdict
	}
	if advice.Status != "selected" || advice.Target == nil {
		verdict.Code = "recommendation_not_selected"
		return verdict
	}
	if round.State != store.RoundCompleted && (round.Outcome != "deliver" || round.State != store.RoundFailed) ||
		advice.RoundID != round.ID || advice.RoundGeneration+1 != round.Generation || advice.ProfileVersion != round.ProfileVersion {
		verdict.Status, verdict.Code = "stale", "round_identity_changed"
		return verdict
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		verdict.Code = "current_profile_unavailable"
		return verdict
	}
	if profile.Version != advice.ProfileVersion {
		verdict.Status, verdict.Code = "stale", "profile_changed"
		return verdict
	}
	attempt, err := db.RoundAttemptForRequest(ctx, round.ID, homeRecommendationRequestKey+"/0")
	if err != nil {
		verdict.Code = "decision_attempt_unavailable"
		return verdict
	}
	if attempt.State != store.AttemptSucceeded || attempt.ID != advice.DecisionAttemptID || advice.DecisionInputSHA256 == "" || advice.CandidateID == "" {
		verdict.Status, verdict.Code = "stale", "decision_identity_changed"
		return verdict
	}
	jevAttempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		verdict.Code = "decision_evidence_unavailable"
		return verdict
	}
	matched := false
	for _, jevAttempt := range jevAttempts {
		if jevAttempt.RoundAttemptID != attempt.ID {
			continue
		}
		digest := sha256.Sum256(jevAttempt.LogicalRequestJSON)
		if jevAttempt.Status != "succeeded" || hex.EncodeToString(digest[:]) != advice.DecisionInputSHA256 {
			verdict.Status, verdict.Code = "stale", "decision_identity_changed"
			return verdict
		}
		var response struct {
			Answers map[string]struct {
				Choice string `json:"choice"`
			} `json:"answers"`
		}
		if json.Unmarshal(jevAttempt.RawResponseBytes, &response) != nil || response.Answers["selected_candidate"].Choice != advice.CandidateID {
			verdict.Status, verdict.Code = "stale", "decision_identity_changed"
			return verdict
		}
		matched = true
		break
	}
	if !matched {
		verdict.Code = "decision_evidence_unavailable"
		return verdict
	}
	assessedByID := map[string]assessedSource{}
	for _, source := range detail.AssessedSources {
		assessedByID[source.OpportunityID] = source
	}
	if len(advice.SourceRefs) == 0 {
		verdict.Status, verdict.Code = "stale", "source_refs_missing"
		return verdict
	}
	profileSources, _, complete := profileDecisionSources(profile, 4)
	if !complete {
		verdict.Status, verdict.Code = "stale", "profile_context_changed"
		return verdict
	}
	profileRefs := map[string]string{}
	for _, source := range profileSources {
		profileRefs[source.ID] = source.SourceRevision
	}
	knownOpportunities := map[string]bool{}
	var outcomeFacts outcomeRecommendationFacts
	if round.Outcome != "discover" {
		outcomeFacts, err = savedOutcomeRecommendationFacts(ctx, db, round)
		if err != nil {
			verdict.Code = "outcome_facts_unavailable"
			return verdict
		}
		if !outcomeFacts.useful() {
			verdict.Status, verdict.Code = "stale", "outcome_result_changed"
			return verdict
		}
	}
	for _, ref := range advice.SourceRefs {
		switch ref.Kind {
		case "owner_profile":
			if revision, found := profileRefs[ref.ID]; !found || revision != ref.Revision || revision != strconv.FormatInt(profile.Version, 10) {
				verdict.Status, verdict.Code = "stale", "profile_source_changed"
				return verdict
			}
		case "commissioned_round_result":
			if ref.ID != "round:"+round.ID || ref.Revision != recommendationProgressRevision(round.ID, detail) {
				verdict.Status, verdict.Code = "stale", "round_result_changed"
				return verdict
			}
		case "commissioned_outcome_facts":
			if round.Outcome == "discover" || ref.ID != "round:"+round.ID || ref.Revision != outcomeFactsRevision(round.ID, outcomeFacts) {
				verdict.Status, verdict.Code = "stale", "outcome_result_changed"
				return verdict
			}
		case "saved_comparison_facts":
			if round.Outcome != "compare_offers" || ref.ID != "comparison:"+outcomeFacts.ResultID {
				verdict.Status, verdict.Code = "stale", "comparison_identity_changed"
				return verdict
			}
			comparison, readErr := db.OfferComparisonForOwner(ctx, round.Actor, outcomeFacts.ResultID)
			if readErr != nil {
				verdict.Code = "comparison_read_unavailable"
				return verdict
			}
			if !comparison.Current || comparison.RoundID != round.ID || comparison.TradeoffStatus != outcomeFacts.TradeoffStatus {
				verdict.Status, verdict.Code = "stale", "comparison_changed"
				return verdict
			}
			summary := comparisonRecommendationSummary(comparison, outcomeFacts.TradeoffStatus)
			sum := sha256.Sum256([]byte(summary))
			if ref.Revision != hex.EncodeToString(sum[:]) {
				verdict.Status, verdict.Code = "stale", "comparison_changed"
				return verdict
			}
		case "current_opportunity_state":
			if !strings.HasPrefix(ref.ID, "opportunity:") {
				verdict.Status, verdict.Code = "stale", "source_identity_changed"
				return verdict
			}
			id := strings.TrimPrefix(ref.ID, "opportunity:")
			knownOpportunities[id] = true
			opportunity, readErr := db.Opportunity(ctx, id)
			if errors.Is(readErr, store.ErrNotFound) {
				verdict.Status, verdict.Code = "stale", "opportunity_removed"
				return verdict
			}
			if readErr != nil {
				verdict.Code = "opportunity_read_unavailable"
				return verdict
			}
			if opportunity.ArchivedAt != "" || opportunity.Revision != ref.OpportunityRevision {
				verdict.Status, verdict.Code = "stale", "opportunity_changed"
				return verdict
			}
			decision, decisionErr := db.OwnerOpportunityDecision(ctx, id)
			if errors.Is(decisionErr, store.ErrNotFound) {
				decision = store.OwnerDecision{}
			} else if decisionErr != nil {
				verdict.Code = "owner_decision_read_unavailable"
				return verdict
			}
			if decision.Revision != ref.OwnerDecisionRevision {
				verdict.Status, verdict.Code = "stale", "owner_decision_changed"
				return verdict
			}
			packs, packErr := db.ListApplicationPacks(ctx, id)
			if packErr != nil {
				verdict.Code = "pack_read_unavailable"
				return verdict
			}
			packID, packHash, packVersion := "", "", int64(0)
			if len(packs) > 0 && packs[0].OpportunityRevision == opportunity.Revision && packs[0].ProfileRevision == profile.Version {
				packID, packHash, packVersion = packs[0].ID, packs[0].ContentSHA256, packs[0].Version
			}
			if ref.PackID != packID || ref.PackVersion != packVersion || ref.PackContentSHA256 != packHash {
				verdict.Status, verdict.Code = "stale", "pack_changed"
				return verdict
			}
			roundAssessed := assessedByID[id]
			if roundAssessed.OpportunityID != "" && (ref.ScreeningAssessmentID != roundAssessed.ScreeningAssessmentID || ref.OrganisationAssessmentID != roundAssessed.OrganisationAssessmentID) {
				verdict.Status, verdict.Code = "stale", "round_assessment_changed"
				return verdict
			}
			engine := &Engine{Store: db}
			screen, screenPresent, screenErr := engine.currentRecommendationAssessment(ctx, opportunity, profile.Version, "screening")
			organisation, organisationPresent, organisationErr := engine.currentRecommendationAssessment(ctx, opportunity, profile.Version, "organisation")
			if screenErr != nil || organisationErr != nil {
				verdict.Code = "assessment_read_unavailable"
				return verdict
			}
			if screenPresent != (ref.ScreeningAssessmentID != "") || organisationPresent != (ref.OrganisationAssessmentID != "") ||
				screenPresent && screen.ID != ref.ScreeningAssessmentID || organisationPresent && organisation.ID != ref.OrganisationAssessmentID {
				verdict.Status, verdict.Code = "stale", "assessment_changed"
				return verdict
			}
			assessed := assessedSource{ScreeningAssessmentID: ref.ScreeningAssessmentID, OrganisationAssessmentID: ref.OrganisationAssessmentID}
			if ref.Revision != recommendationOpportunityRevision(opportunity.Revision, decision, assessed, packID, packHash) {
				verdict.Status, verdict.Code = "stale", "assessment_or_source_changed"
				return verdict
			}
		default:
			verdict.Status, verdict.Code = "stale", "unrecognized_source_ref"
			return verdict
		}
	}
	page, err := db.ListOpportunities(ctx, store.OpportunityListOptions{Limit: 100})
	if err != nil || page.NextCursor != "" {
		verdict.Code = "current_selection_inventory_unavailable"
		return verdict
	}
	for _, opportunity := range page.Items {
		decision, decisionErr := db.OwnerOpportunityDecision(ctx, opportunity.ID)
		if errors.Is(decisionErr, store.ErrNotFound) {
			continue
		}
		if decisionErr != nil {
			verdict.Code = "current_selection_inventory_unavailable"
			return verdict
		}
		if decision.Decision == "selected" && decision.OpportunityRevision == opportunity.Revision && !knownOpportunities[opportunity.ID] {
			verdict.Status, verdict.Code = "stale", "selected_roles_changed"
			return verdict
		}
	}
	engine := &Engine{Store: db}
	choice := recommendationChoice{Action: advice.Action, Target: *advice.Target}
	if err := engine.checkRecommendationTarget(ctx, round, detail.AssessedSources, choice); err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalid) {
			verdict.Status, verdict.Code = "stale", "target_changed"
		} else {
			verdict.Code = "target_read_unavailable"
		}
		return verdict
	}
	if advice.Action == "prepare" {
		packs, packErr := db.ListApplicationPacks(ctx, advice.Target.ID)
		if packErr != nil {
			verdict.Code = "pack_read_unavailable"
			return verdict
		}
		if len(packs) > 0 && packs[0].OpportunityRevision == advice.Target.Revision && packs[0].ProfileRevision == profile.Version {
			verdict.Status, verdict.Code = "stale", "current_pack_now_exists"
			return verdict
		}
	}
	if advice.Action == "review_pack" {
		packs, packErr := db.ListApplicationPacks(ctx, advice.Target.OpportunityID)
		if packErr != nil {
			verdict.Code = "pack_read_unavailable"
			return verdict
		}
		if len(packs) == 0 || packs[0].ID != advice.Target.ID {
			verdict.Status, verdict.Code = "stale", "newer_pack_available"
			return verdict
		}
	}
	verdict.Status, verdict.Code = "current", "verified"
	return verdict
}
