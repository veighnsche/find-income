// Package agency runs one owner-commissioned discovery round inside its saved
// scope and allowance. It never schedules work from a status read or timer.
package agency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/collector"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type Runtime interface {
	CheckRound(context.Context, string) error
	ExecuteRoundTurn(context.Context, store.Actor, string, codexservice.RoundTurnInput) (store.RoundAttempt, error)
}

type Decisions interface {
	RunDecision(context.Context, jevservice.Binding, jev.DecisionInput) (jev.DecisionResult, error)
	RunScreening(context.Context, jevservice.Binding, jev.ScreeningInput) (jev.ScreeningResult, error)
	RunOrganisation(context.Context, jevservice.Binding, jev.OrganisationInput) (jev.OrganisationResult, error)
}

type SourceCollector interface {
	AcquireLever(context.Context, collector.Request) (collector.Batch, error)
}

type Engine struct {
	Store       *store.Store
	Runtime     Runtime
	Decisions   Decisions
	Collector   SourceCollector
	PackSources PackSourceLoader
	Context     context.Context
	mu          sync.Mutex
	active      map[string]context.CancelFunc
}

func (e *Engine) CheckRound(ctx context.Context, outcome string) error {
	if outcome == "prepare" {
		return e.checkPrepare(ctx)
	}
	if e == nil || e.Store == nil || e.Runtime == nil || e.Decisions == nil || e.Collector == nil || outcome != "discover" {
		return errors.New("commissioned discovery unavailable")
	}
	if err := e.Runtime.CheckRound(ctx, outcome); err != nil {
		return err
	}
	return nil
}

func (e *Engine) LaunchRound(_ context.Context, r store.Round) error {
	if r.Outcome == "prepare" {
		return e.launchPrepare(r)
	}
	if e == nil || e.Store == nil || e.Runtime == nil || e.Decisions == nil || e.Collector == nil || r.State != store.RoundRunning || r.Outcome != "discover" {
		return store.ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]context.CancelFunc{}
	}
	if _, exists := e.active[r.ID]; exists {
		return store.ErrConflict
	}
	base := e.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithDeadline(base, r.Deadline)
	e.active[r.ID] = cancel
	go func() {
		defer cancel()
		defer func() { e.mu.Lock(); delete(e.active, r.ID); e.mu.Unlock() }()
		e.run(ctx, r)
	}()
	return nil
}

func (e *Engine) CancelRound(id string) {
	e.mu.Lock()
	cancel := e.active[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type report struct {
	Code                     string               `json:"code"`
	BoardID                  string               `json:"boardId,omitempty"`
	CollectorAttemptID       string               `json:"collectorAttemptId,omitempty"`
	SourceOpeningID          string               `json:"sourceOpeningId,omitempty"`
	IngestionID              string               `json:"ingestionId,omitempty"`
	Opportunities            int                  `json:"opportunities"`
	Screened                 bool                 `json:"screened"`
	ScreeningInputSHA256     string               `json:"screeningInputSha256,omitempty"`
	ScreeningAssessmentID    string               `json:"screeningAssessmentId,omitempty"`
	ScreeningStatus          string               `json:"screeningStatus,omitempty"`
	OrganisationAssessmentID string               `json:"organisationAssessmentId,omitempty"`
	OrganisationStatus       string               `json:"organisationStatus,omitempty"`
	ScreeningOmittedBytes    int                  `json:"screeningOmittedBytes,omitempty"`
	DiscoveryCandidates      int                  `json:"discoveryCandidates,omitempty"`
	UnreviewedCandidates     int                  `json:"unreviewedCandidates,omitempty"`
	Remaining                store.RoundAllowance `json:"remaining"`
}

func (e *Engine) finish(ctx context.Context, roundID string, owner store.Actor, code string, partial bool, detail report) {
	// Cleanup uses a short independent context so a worker deadline does not
	// erase a terminal outcome. The store still enforces the round fence.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	r, err := e.Store.Round(cleanup, roundID)
	if err != nil || r.State != store.RoundRunning {
		return
	}
	if !time.Now().Before(r.Deadline) {
		_, _ = e.Store.ExpireRound(cleanup, roundID)
		return
	}
	detail.Code = code
	detail.Remaining = store.RoundAllowance{Requests: r.Limits.Requests - r.Used.Requests, Items: r.Limits.Items - r.Used.Items, Tools: r.Limits.Tools - r.Used.Tools, Turns: r.Limits.Turns - r.Used.Turns}
	data, _ := json.Marshal(detail)
	status := "complete"
	if partial {
		status = "partial"
	}
	_, _ = e.Store.FinishRound(cleanup, owner, roundID, store.RoundCompleted, code, status, data)
}

func (e *Engine) live(ctx context.Context, roundID string, generation, profileVersion int64) (store.Round, error) {
	if err := ctx.Err(); err != nil {
		return store.Round{}, err
	}
	r, err := e.Store.Round(ctx, roundID)
	if err != nil {
		return r, err
	}
	if r.State != store.RoundRunning || r.Generation != generation {
		return r, store.ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		_, _ = e.Store.ExpireRound(ctx, roundID)
		return r, store.ErrExpired
	}
	p, err := e.Store.CurrentPreferences(ctx)
	if err != nil {
		return r, err
	}
	if p.Version != profileVersion {
		return r, errProfileChanged
	}
	return r, nil
}

var errProfileChanged = errors.New("profile changed during commissioned round")

func (e *Engine) run(ctx context.Context, initial store.Round) {
	owner := initial.Actor
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	detail := report{}
	code := "no_supported_source"
	partial := true
	defer func() {
		if !errors.Is(ctx.Err(), context.Canceled) {
			e.finish(ctx, initial.ID, owner, code, partial, detail)
		}
	}()
	r, err := e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		code = terminalCode(err)
		return
	}
	var cursor struct {
		CollectorBatchAttemptID string `json:"collectorBatchAttemptId"`
	}
	_ = json.Unmarshal(r.Cursor, &cursor)
	attemptID := cursor.CollectorBatchAttemptID
	if attemptID == "" {
		boards, err := e.Store.ListCollectorBoards(ctx)
		if err != nil {
			code = "source_inventory_failed"
			return
		}
		allowed := map[string]bool{}
		for _, resource := range r.Scope.Resources {
			allowed[resource] = true
		}
		choices := map[string]store.CollectorBoard{}
		searchChoices := map[string]store.RoleCriterion{}
		input := jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: r.Intent, MaxReportedTokens: 1200,
			Capabilities:       []jev.DecisionCapability{{ID: "lever_page", Description: "Read one public Lever board page and stage up to four exact postings."}, {ID: "himalayas_search", Description: "Read one bounded public discovery search and stage exact candidate links for later verification."}},
			RemainingAllowance: []jev.DecisionAllowance{{Operation: store.RoundCollectorPage, Remaining: r.Limits.Requests - r.Used.Requests}}}
		profile, profileErr := e.Store.CurrentPreferences(ctx)
		if profileErr != nil || profile.Version != r.ProfileVersion {
			code = "profile_changed"
			return
		}
		profileSources, profileIDs, complete := profileDecisionSources(profile, 4)
		if !complete {
			code = "profile_context_unbounded"
			return
		}
		input.Sources = append(input.Sources, profileSources...)
		for _, board := range boards {
			if !board.Enabled || board.VerifiedAt == "" || !allowed["board:"+board.ID] {
				continue
			}
			choices[board.ID] = board
			input.Sources = append(input.Sources, jev.DecisionSource{ID: board.ID, SourceRevision: fmt.Sprint(board.Revision), SourceKind: "verified_official_careers", URL: board.OfficialCareersURL, ObservedAt: board.VerifiedAt, Excerpt: board.DisplayName + " public careers board"})
			input.Candidates = append(input.Candidates, jev.DecisionCandidate{ID: board.ID, Description: "Inspect " + board.DisplayName + " public careers postings.", Scope: "One bounded Lever page; source facts require subsequent extraction.", CapabilityID: "lever_page", SourceIDs: append([]string{board.ID}, profileIDs...)})
			if len(choices) == 8 {
				break
			}
		}
		for i, criterion := range profile.RoleCriteria {
			if len(input.Candidates) == 16 {
				break
			}
			if criterion.Mode != "require" && criterion.Mode != "prefer" {
				continue
			}
			id := fmt.Sprintf("search-%d", i)
			searchChoices[id] = criterion
			input.Candidates = append(input.Candidates, jev.DecisionCandidate{ID: id, Description: "Search public vacancies for owner criterion " + criterion.Label + ": " + prefixUTF8(criterion.Description, 800), Scope: "One charged bounded Himalayas search; staged links are unverified leads.", CapabilityID: "himalayas_search", SourceIDs: profileIDs})
		}
		if len(input.Candidates) == 0 {
			return
		}
		decision, err := e.Decisions.RunDecision(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: "select-board", ProfileVersion: r.ProfileVersion}, input)
		if err != nil {
			code = terminalCode(err)
			return
		}
		if decision.Disposition != jev.DecisionSelected {
			code = "source_choice_unresolved"
			return
		}
		var board store.CollectorBoard
		if criterion, search := searchChoices[decision.SelectedID]; search {
			_, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
			if err != nil {
				code = terminalCode(err)
				return
			}
			pageNumber := 1
			continuation, continuationErr := e.Store.DiscoverySearchContinuation(ctx, criterion.Label, "")
			if continuationErr == nil {
				if continuation.NextPage < 1 {
					code = "discovery_search_exhausted"
					return
				}
				pageNumber = continuation.NextPage
			} else if !errors.Is(continuationErr, store.ErrNotFound) {
				code = "discovery_continuation_unavailable"
				return
			}
			brief := "Research one public vacancy search for the selected owner criterion. Use source_discovery with method search_jobs, the exact keyword and page in evidence, and empty country. Stage at most one relevant link with discovery_candidate_stage using an exact quote from the returned page. For that staged candidate, read its matching company detail with source_discovery, use discovery_official_links on the claimed company website and at most one evidenced same-origin careers link, then use discovery_board_register only for an exact Lever link from the saved official-site read. The board registration authorizes full posting collection in this round. Do not create an opportunity from a search summary or company detail. Stop if any provenance step is missing."
			evidence, _ := json.Marshal(struct {
				Criterion store.RoleCriterion `json:"criterion"`
				Keyword   string              `json:"keyword"`
				Page      int                 `json:"page"`
			}{criterion, criterion.Label, pageNumber})
			_, err = e.Runtime.ExecuteRoundTurn(ctx, agent, r.ID, codexservice.RoundTurnInput{RequestKey: "discover:" + decision.SelectedID, ResourceID: "discovery:himalayas", Brief: brief, Evidence: string(evidence)})
			if err != nil {
				code = terminalCode(err)
				return
			}
			history, readErr := e.Store.RoundHistory(ctx, r.ID)
			if readErr != nil {
				code = "discovery_history_unavailable"
				return
			}
			registeredID := ""
			for _, event := range history {
				if event.Operation == store.RoundStageDiscovery {
					detail.DiscoveryCandidates++
				} else if event.Operation == store.RoundRegisterDiscoveryBoard && event.EntityKind == "collector_board" {
					registeredID = event.EntityID
				}
			}
			if registeredID == "" {
				if detail.DiscoveryCandidates == 0 {
					code = "discovery_unresolved"
				} else {
					code = "unverified_discovery_candidates"
				}
				return
			}
			boards, err = e.Store.ListCollectorBoards(ctx)
			if err != nil {
				code = "registered_board_unavailable"
				return
			}
			for _, candidate := range boards {
				if candidate.ID == registeredID && candidate.Enabled && candidate.VerifiedAt != "" {
					board = candidate
					break
				}
			}
			if board.ID == "" {
				code = "registered_board_unavailable"
				return
			}
		} else {
			var found bool
			board, found = choices[decision.SelectedID]
			if !found {
				code = "source_choice_invalid"
				return
			}
		}
		detail.BoardID = board.ID
		r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
		if err != nil {
			code = terminalCode(err)
			return
		}
		reserve, created, err := e.Store.ReserveCollectorAcquisition(ctx, owner, r.ID, store.RoundCollectorAcquisitionInput{RequestKey: "collect:" + board.ID, BoardID: board.ID, MaxPages: 1, MaxItems: 4})
		if err != nil || !created {
			code = terminalCode(err)
			return
		}
		attemptID = reserve.ID
		detail.CollectorAttemptID = attemptID
		if _, err = e.Store.MarkRoundDispatched(ctx, r.ID, attemptID); err != nil {
			code = "source_dispatch_failed"
			return
		}
		batch, fetchErr := e.Collector.AcquireLever(ctx, collector.Request{Board: board, MaxPages: 1, MaxItems: 4})
		if fetchErr != nil {
			batch.ErrorCode = "source_acquisition_failed"
			batch.Next = &collector.Cursor{BoardID: board.ID}
		}
		// Even a transport failure is published with its cursor and charge.
		staging, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		current, readErr := e.Store.Round(staging, r.ID)
		if readErr == nil {
			encoded, encodeErr := json.Marshal(batch)
			if encodeErr == nil {
				_, readErr = e.Store.SaveRoundCollectorBatch(staging, owner, r.ID, attemptID, current.Revision, encoded)
			}
		}
		cancel()
		if readErr != nil {
			settle, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, _ = e.Store.FinishRoundAttempt(settle, owner, r.ID, attemptID, false, nil, "collector_stage_failed")
			done()
			code = "source_result_uncertain"
			return
		}
		if batch.ErrorCode != "" {
			code = batch.ErrorCode
			return
		}
	}
	detail.CollectorAttemptID = attemptID
	outcomes, err := e.Store.RoundCollectorOutcomes(ctx, r.ID, attemptID)
	if err != nil {
		code = "source_outcomes_unavailable"
		return
	}
	candidateInput := jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: r.Intent, MaxReportedTokens: 1200,
		Capabilities:       []jev.DecisionCapability{{ID: "source_extract", Description: "Extract one current exact vacancy into a source-linked opportunity."}},
		RemainingAllowance: []jev.DecisionAllowance{{Operation: store.RoundCodexTurn, Remaining: r.Limits.Turns - r.Used.Turns}}}
	choiceProfile, profileErr := e.Store.CurrentPreferences(ctx)
	if profileErr != nil || choiceProfile.Version != r.ProfileVersion {
		code = "profile_changed"
		return
	}
	profileSources, profileIDs, complete := profileDecisionSources(choiceProfile, 8)
	if !complete {
		code = "profile_context_unbounded"
		return
	}
	candidateInput.Sources = append(candidateInput.Sources, profileSources...)
	candidateOutcomes := map[string]store.CollectorSourceOutcome{}
	for i := range outcomes {
		item := &outcomes[i]
		if item.Current && (item.Decision == "new" || item.Decision == "changed") {
			stored, readErr := e.Store.Ingestion(ctx, item.IngestionID)
			if readErr != nil {
				continue
			}
			id := item.SourceOpeningID
			candidateOutcomes[id] = *item
			excerpt := prefixUTF8(stored.OriginalText, 1900)
			if len(excerpt) < len(stored.OriginalText) {
				excerpt += " [excerpt truncated; complete source is retained]"
			}
			candidateInput.Sources = append(candidateInput.Sources, jev.DecisionSource{ID: id, SourceRevision: item.ContentSHA256, SourceKind: "lever_posting", URL: item.SourceURL, ObservedAt: item.ObservedAt, Excerpt: excerpt})
			candidateInput.Candidates = append(candidateInput.Candidates, jev.DecisionCandidate{ID: id, Description: "Review this current saved vacancy source.", Scope: "One source-linked opportunity extraction; no external application.", CapabilityID: "source_extract", SourceIDs: append([]string{id}, profileIDs...)})
		}
	}
	if len(candidateInput.Candidates) == 0 {
		code = "no_new_source"
		return
	}
	detail.UnreviewedCandidates = len(candidateInput.Candidates) - 1
	selection, err := e.Decisions.RunDecision(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: "select-source:" + attemptID, ProfileVersion: r.ProfileVersion}, candidateInput)
	if err != nil {
		code = terminalCode(err)
		return
	}
	if selection.Disposition != jev.DecisionSelected {
		code = "source_choice_unresolved"
		return
	}
	selected, found := candidateOutcomes[selection.SelectedID]
	if !found {
		code = "source_choice_invalid"
		return
	}
	chosen := &selected
	detail.SourceOpeningID, detail.IngestionID = chosen.SourceOpeningID, chosen.IngestionID
	item, err := e.Store.Ingestion(ctx, chosen.IngestionID)
	if err != nil {
		code = "source_record_unavailable"
		return
	}
	if len(item.OriginalText) > 29000 {
		code = "source_exceeds_turn_evidence"
		return
	}
	_, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		code = terminalCode(err)
		return
	}
	needsSave := true
	if chosen.OpportunityID != "" {
		previous, readErr := e.Store.Opportunity(ctx, chosen.OpportunityID)
		if readErr == nil && previous.OriginalText == item.OriginalText {
			needsSave = false
		}
	}
	if needsSave {
		boardAttempt, readErr := e.Store.RoundAttempt(ctx, attemptID)
		if readErr != nil {
			code = "source_board_unavailable"
			return
		}
		boardID := strings.TrimPrefix(boardAttempt.ResourceID, "board:")
		boards, readErr := e.Store.ListCollectorBoards(ctx)
		if readErr != nil {
			code = "source_board_unavailable"
			return
		}
		var board store.CollectorBoard
		for _, candidate := range boards {
			if candidate.ID == boardID {
				board = candidate
				break
			}
		}
		if board.ID == "" {
			code = "source_board_unavailable"
			return
		}
		var companyID string
		var expectedRevision int64
		if chosen.OpportunityID != "" {
			current, readErr := e.Store.Opportunity(ctx, chosen.OpportunityID)
			if readErr != nil {
				code = "mapped_opportunity_unavailable"
				return
			}
			companyID, expectedRevision = current.CompanyID, current.Revision
		} else {
			page, readErr := e.Store.ListCompanies(ctx, store.CompanyListOptions{Limit: 100})
			if readErr != nil {
				code = "company_inventory_unavailable"
				return
			}
			for {
				for _, company := range page.Items {
					if company.ArchivedAt == "" && company.Website != "" && strings.EqualFold(company.Website, board.OfficialCareersURL) {
						companyID, expectedRevision = company.ID, company.Revision
						break
					}
				}
				if companyID != "" || page.NextCursor == "" {
					break
				}
				page, readErr = e.Store.ListCompanies(ctx, store.CompanyListOptions{Cursor: page.NextCursor, Limit: 100})
				if readErr != nil {
					code = "company_inventory_unavailable"
					return
				}
			}
		}
		evidence, _ := json.Marshal(struct {
			SourceOpeningID    string `json:"sourceOpeningId"`
			IngestionID        string `json:"ingestionId"`
			SourceURL          string `json:"sourceUrl"`
			OriginalText       string `json:"originalText"`
			BoardCompanyName   string `json:"boardCompanyName"`
			OfficialCareersURL string `json:"officialCareersUrl"`
			ExistingCompanyID  string `json:"existingCompanyId,omitempty"`
			ExpectedRevision   int64  `json:"expectedRevision,omitempty"`
		}{chosen.SourceOpeningID, chosen.IngestionID, chosen.SourceURL, item.OriginalText, board.DisplayName, board.OfficialCareersURL, companyID, expectedRevision})
		if len(evidence) > 32000 {
			code = "source_exceeds_turn_evidence"
			return
		}
		brief := "Extract this exact current source into a sourced opportunity. If existingCompanyId is supplied, use it and expectedRevision. Otherwise create a company with boardCompanyName and officialCareersUrl using round_mutation company.create, then use its returned id and revision. Save via round_mutation opportunity.source_save using resourceId source-opening:<sourceOpeningId>, and both expectedRevision fields set to the company revision for a new opening or the mapped opportunity revision for a refresh. Set only vacancy facts supported by originalText. If insufficient, leave unresolved. Do not claim an application was sent."
		_, err = e.Runtime.ExecuteRoundTurn(ctx, agent, r.ID, codexservice.RoundTurnInput{RequestKey: "extract:" + chosen.IngestionID, ResourceID: "campaign:active", Brief: brief, Evidence: string(evidence)})
		if err != nil {
			code = terminalCode(err)
			return
		}
	}
	refreshed, err := e.Store.RoundCollectorOutcomes(ctx, r.ID, attemptID)
	if err != nil {
		code = "source_mapping_unavailable"
		return
	}
	for _, current := range refreshed {
		if current.IngestionID == chosen.IngestionID {
			chosen = &current
			break
		}
	}
	if chosen.OpportunityID == "" || chosen.SourceID == "" {
		code = "extraction_unresolved"
		return
	}
	cards, err := e.Store.RoundCards(ctx, r.ID)
	if err != nil {
		code = "cards_unavailable"
		return
	}
	detail.Opportunities = len(cards)
	if detail.Opportunities == 0 {
		detail.Opportunities = 1
	} // Existing mapped current source.
	currentOpportunity, err := e.Store.Opportunity(ctx, chosen.OpportunityID)
	if err != nil || currentOpportunity.OriginalText != item.OriginalText {
		code = "source_mapping_stale"
		return
	}
	// Screening spans cover the whole exact source when it fits the bounded
	// contract. Any omitted tail is reported and cannot yield a full result.
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != r.ProfileVersion {
		code = "profile_changed"
		return
	}
	if len(profile.RoleCriteria) == 0 || len(profile.RoleCriteria) > 16 {
		code = "screening_input_unbounded"
		return
	}
	spans := make([]jev.ScreeningSpan, 0, 12)
	remaining := item.OriginalText
	for len(remaining) > 0 && len(spans) < 12 {
		part := prefixUTF8(remaining, 2000)
		if part == "" {
			break
		}
		spans = append(spans, jev.ScreeningSpan{ID: fmt.Sprintf("%s:%d", chosen.IngestionID, len(spans)), SourceID: chosen.SourceID, SourceRevision: chosen.ContentSHA256, SourceKind: "lever_posting", ObservedAt: chosen.ObservedAt, Excerpt: part})
		remaining = remaining[len(part):]
	}
	detail.ScreeningOmittedBytes = len(remaining)
	criteria := make([]jev.ScreeningCriterion, 0, len(profile.RoleCriteria))
	for _, c := range profile.RoleCriteria {
		criteria = append(criteria, jev.ScreeningCriterion{ID: c.ID, Label: c.Label, Description: c.Description, Kind: c.Kind, Mode: c.Mode})
	}
	screenPrefix := "screen:" + chosen.IngestionID
	screenInput := jev.ScreeningInput{PreferenceVersion: r.ProfileVersion, MaxTotalTokens: 2500, Criteria: criteria, Spans: spans}
	screening, err := e.Decisions.RunScreening(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: screenPrefix, ProfileVersion: r.ProfileVersion}, screenInput)
	if err != nil {
		code = terminalCode(err)
		return
	}
	screenAttemptIDs, err := e.Store.JevAttemptIDsForRequestPrefix(ctx, r.ID, screenPrefix)
	if err != nil {
		code = "screening_attempts_unavailable"
		return
	}
	binding := store.RoundAssessmentInput{Actor: owner, RoundID: r.ID, RoundGeneration: initial.Generation, ResourceID: "campaign:active", OpportunityID: chosen.OpportunityID, OpportunityRevision: currentOpportunity.Revision, SourceID: chosen.SourceID, SourceRevision: chosen.ContentSHA256, ProfileVersion: r.ProfileVersion, JevAttemptIDs: screenAttemptIDs, OmittedBytes: detail.ScreeningOmittedBytes}
	screenAssessment, err := e.Store.ApplyRoundScreening(ctx, binding, screenInput, screening)
	if err != nil {
		code = "screening_apply_failed"
		return
	}
	detail.Screened, detail.ScreeningInputSHA256, detail.ScreeningAssessmentID, detail.ScreeningStatus = true, screening.InputSHA256, screenAssessment.ID, screenAssessment.Status
	categorySet, err := e.Store.CurrentOrganisationCategories(ctx)
	if err != nil || len(categorySet.Categories) == 0 {
		code = "organisation_categories_unavailable"
		return
	}
	organisationInput := jev.OrganisationInput{CategorySetVersion: categorySet.Version}
	for _, c := range categorySet.Categories {
		organisationInput.Categories = append(organisationInput.Categories, jev.OrganisationCategory{ID: c.ID, Description: c.Description})
	}
	for _, span := range spans {
		organisationInput.Facts = append(organisationInput.Facts, jev.OrganisationFact{ID: span.ID, SourceID: span.SourceID, SourceRevision: span.SourceRevision, SourceKind: "vacancy_snapshot", ObservedAt: span.ObservedAt, Excerpt: span.Excerpt})
	}
	organisationPrefix := "organise:" + chosen.IngestionID
	organisation, err := e.Decisions.RunOrganisation(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: organisationPrefix, ProfileVersion: r.ProfileVersion}, organisationInput)
	if err != nil {
		code = terminalCode(err)
		return
	}
	organisationAttempts, err := e.Store.JevAttemptIDsForRequestPrefix(ctx, r.ID, organisationPrefix)
	if err != nil {
		code = "organisation_attempts_unavailable"
		return
	}
	binding.JevAttemptIDs = organisationAttempts
	organisationAssessment, err := e.Store.ApplyRoundOrganisation(ctx, binding, organisationInput, organisation)
	if err != nil {
		code = "organisation_apply_failed"
		return
	}
	detail.OrganisationAssessmentID, detail.OrganisationStatus = organisationAssessment.ID, organisationAssessment.Status
	code = "sourced_opportunity_assessed"
	partial = detail.ScreeningOmittedBytes > 0 || detail.UnreviewedCandidates > 0 || screenAssessment.Status != "proposed" || organisationAssessment.Status != "selected"
}

func prefixUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func profileDecisionSources(profile store.Preferences, maximum int) ([]jev.DecisionSource, []string, bool) {
	encoded, _ := json.Marshal(struct {
		PreferredLocation     string                `json:"preferredLocation"`
		AllowRemote           bool                  `json:"allowRemote"`
		AllowHybrid           bool                  `json:"allowHybrid"`
		TargetHoursHundredths int64                 `json:"targetHoursHundredths"`
		MinMonthlyBaseCents   int64                 `json:"minMonthlyBaseCents"`
		SalaryCurrency        string                `json:"salaryCurrency"`
		RoleCriteria          []store.RoleCriterion `json:"roleCriteria"`
	}{profile.PreferredLocation, profile.AllowRemote, profile.AllowHybrid, profile.TargetHoursHundredths, profile.MinMonthlyBaseCents, profile.SalaryCurrency, profile.RoleCriteria})
	remaining := string(encoded)
	var sources []jev.DecisionSource
	var ids []string
	for len(remaining) > 0 && len(sources) < maximum {
		part := prefixUTF8(remaining, 1900)
		id := fmt.Sprintf("profile:%d", len(sources))
		sources = append(sources, jev.DecisionSource{ID: id, SourceRevision: fmt.Sprint(profile.Version), SourceKind: "owner_profile", Excerpt: part})
		ids = append(ids, id)
		remaining = remaining[len(part):]
	}
	return sources, ids, remaining == ""
}

func terminalCode(err error) string {
	if err == nil {
		return "operation_not_completed"
	}
	switch {
	case errors.Is(err, errProfileChanged):
		return "profile_changed"
	case errors.Is(err, store.ErrAllowance):
		return "allowance_exhausted"
	case errors.Is(err, store.ErrExpired):
		return "deadline_reached"
	case errors.Is(err, store.ErrUncertain):
		return "remote_outcome_uncertain"
	case errors.Is(err, context.Canceled):
		return "worker_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_reached"
	case errors.Is(err, store.ErrFenced):
		return "round_fenced"
	default:
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unavailable") {
			return "provider_unavailable"
		}
		return "work_step_failed"
	}
}
