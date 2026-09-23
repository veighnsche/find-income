package agency

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func assessedEvidenceSummary(screen, organisation store.RoundJevAssessment, omitted int) (string, int) {
	var screened jev.ScreeningResult
	var organised jev.OrganisationResult
	_ = json.Unmarshal(screen.ResultJSON, &screened)
	_ = json.Unmarshal(organisation.ResultJSON, &organised)
	summary := fmt.Sprintf("screening=%s organisation=%s category=%s sourceOmittedBytes=%d", screen.Status, organisation.Status, organised.CategoryID, omitted)
	omittedObservations := 0
	for index, observation := range screened.Observations {
		entry := fmt.Sprintf("; %s:%s:%s", observation.CriterionID, observation.Scope, observation.SupportState)
		// Reserve room for the explicit completeness marker before adding a
		// complete observation. Never cut a criterion ID or state in half.
		if len(summary)+len(entry)+len("; assessmentObservationsOmitted=16") > 430 {
			omittedObservations = len(screened.Observations) - index
			break
		}
		summary += entry
	}
	summary += fmt.Sprintf("; assessmentObservationsOmitted=%d", omittedObservations)
	return summary, omittedObservations
}

// chooseNextDiscoveryOutcome gives Jev the attributable retained candidates,
// completed assessment observations, explicit omissions, and the exact budget.
// Its result is an exact saved source pin, never a generic unseen next step.
func (e *Engine) chooseNextDiscoveryOutcome(ctx context.Context, r store.Round, cursor agencyCursor, selected store.CollectorSourceOutcome, item store.IngestionRequest, assessed []assessedSource) (*store.CollectorSourceOutcome, bool, error) {
	if cursor.Selected == nil || cursor.Selected.BatchAttemptID == "" || cursor.Selected.OutcomeRoundID == "" {
		return nil, false, store.ErrInvalid
	}
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != r.ProfileVersion {
		return nil, false, errProfileChanged
	}
	profileSources, profileIDs, complete := profileDecisionSources(profile, 4)
	if !complete || len(assessed) > 16 {
		return nil, false, store.ErrInvalid
	}
	outcomes, err := e.Store.RoundCollectorOutcomes(ctx, cursor.Selected.OutcomeRoundID, cursor.Selected.BatchAttemptID)
	if err != nil {
		return nil, false, err
	}
	currentSourceIDs := append([]string{selected.SourceOpeningID}, profileIDs...)
	input := jev.DecisionInput{Kind: jev.DecisionNextOutcome, CampaignIntent: r.Intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
		Capabilities: []jev.DecisionCapability{
			{ID: "assess_exact_source", Description: "Assess one exact current unassessed posting already saved in this bounded source batch."},
			{ID: "finish_round", Description: "Finish this commission with the sourced opportunities and assessments already recorded."},
		},
		Sources: append(profileSources, jev.DecisionSource{ID: selected.SourceOpeningID, SourceRevision: selected.ContentSHA256,
			SourceKind: "lever_posting", URL: selected.SourceURL, ObservedAt: selected.ObservedAt, Excerpt: boundedDecisionExcerpt(item.OriginalText)}),
		RemainingAllowance: []jev.DecisionAllowance{
			{Operation: store.RoundCodexTurn, Remaining: r.Limits.Turns - r.Used.Turns},
			{Operation: store.RoundJevRequest, Remaining: r.Limits.Requests - r.Used.Requests},
			{Operation: store.RoundSaveSourceOpportunity, Remaining: r.Limits.Items - r.Used.Items},
			{Operation: store.RoundContextTool, Remaining: r.Limits.Tools - r.Used.Tools},
		},
		Candidates: []jev.DecisionCandidate{{ID: "finish", Description: "Finish with assessed sourced work and retain other saved postings.",
			Scope: "No further source or provider work in this round; all unassessed postings remain durable.", CapabilityID: "finish_round", SourceIDs: currentSourceIDs}},
	}
	choices := map[string]store.CollectorSourceOutcome{}
	for _, outcome := range outcomes {
		if !outcome.Current || outcome.SourceID != "" || outcome.Decision != "new" && outcome.Decision != "changed" {
			continue
		}
		unassessed, readErr := e.Store.Ingestion(ctx, outcome.IngestionID)
		if readErr != nil {
			return nil, false, readErr
		}
		if len(input.Sources) == 12 || len(input.Candidates) == 16 {
			return nil, false, store.ErrInvalid
		}
		choices[outcome.SourceOpeningID] = outcome
		input.Sources = append(input.Sources, jev.DecisionSource{ID: outcome.SourceOpeningID, SourceRevision: outcome.ContentSHA256,
			SourceKind: "lever_posting", URL: outcome.SourceURL, ObservedAt: outcome.ObservedAt, Excerpt: boundedDecisionExcerpt(unassessed.OriginalText)})
		input.Candidates = append(input.Candidates, jev.DecisionCandidate{ID: outcome.SourceOpeningID,
			Description:  "Assess this exact retained current posting from " + outcome.SourceURL,
			Scope:        "Use the complete saved source for one source linked opportunity extraction, screening and organisation; the decision sees a bounded excerpt and may abstain if insufficient.",
			CapabilityID: "assess_exact_source", SourceIDs: append([]string{outcome.SourceOpeningID}, profileIDs...)})
	}
	if len(choices) == 0 {
		return nil, false, store.ErrNotFound
	}
	for _, prior := range assessed {
		if prior.EvidenceSummary == "" {
			return nil, false, store.ErrInvalid
		}
		input.PreviousOutcomes = append(input.PreviousOutcomes, jev.PreviousDecisionOutcome{ID: prior.IngestionID,
			Description: prefixUTF8("Opportunity "+prior.OpportunityID+"; "+prior.EvidenceSummary, 490)})
	}
	choice, err := e.savedOrRunDecision(ctx, jevservice.Binding{Actor: r.Actor, RoundID: r.ID, ResourceID: "campaign:active",
		RequestKeyPrefix: fmt.Sprintf("next-outcome:p%d", cursor.Phase), ProfileVersion: r.ProfileVersion}, input)
	if err != nil {
		return nil, false, err
	}
	if choice == "finish" {
		return nil, true, nil
	}
	if choice == "" {
		return nil, false, nil
	}
	next, ok := choices[choice]
	if !ok {
		return nil, false, store.ErrFenced
	}
	return &next, false, nil
}

func boundedDecisionExcerpt(text string) string {
	excerpt := prefixUTF8(text, 1850)
	if len(excerpt) < len(text) {
		excerpt += fmt.Sprintf(" [bounded excerpt; %d source bytes omitted here, complete saved source remains available to the extraction turn]", len(text)-len(excerpt))
	}
	return excerpt
}
