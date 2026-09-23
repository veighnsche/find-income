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

type OfferTradeoffs interface {
	RunOfferTradeoff(context.Context, jevservice.Binding, jev.OfferTradeoffInput) (jev.OfferTradeoffResult, error)
}

type InterviewFocusEvaluator interface {
	RunInterviewFocus(context.Context, jevservice.Binding, jev.InterviewFocusInput) (jev.InterviewFocusResult, error)
}

type SourceCollector interface {
	AcquireLever(context.Context, collector.Request) (collector.Batch, error)
}

type Engine struct {
	Store            *store.Store
	Runtime          Runtime
	Decisions        Decisions
	Tradeoffs        OfferTradeoffs
	InterviewSources PackSourceLoader
	InterviewFocus   InterviewFocusEvaluator
	Collector        SourceCollector
	InputReader      OwnerSourceReader
	PackSources      PackSourceLoader
	Context          context.Context
	mu               sync.Mutex
	active           map[string]*activeWorker
}

type activeWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (e *Engine) workerDone(id string, worker *activeWorker) {
	e.mu.Lock()
	if e.active[id] == worker {
		delete(e.active, id)
	}
	close(worker.done)
	e.mu.Unlock()
}

// WaitRoundStopped is the handoff barrier between a cancelled generation and
// Resume. It never activates or changes the paused round itself.
func (e *Engine) WaitRoundStopped(ctx context.Context, id string) error {
	e.mu.Lock()
	worker := e.active[id]
	e.mu.Unlock()
	if worker == nil {
		return nil
	}
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) CheckRound(ctx context.Context, outcome string) error {
	if outcome == "interview_prepare" || outcome == "interview_debrief" {
		return e.checkInterview(ctx, outcome)
	}
	if outcome == "compare_offers" {
		return e.checkOfferComparison(ctx)
	}
	if outcome == "prepare" {
		return e.checkPrepare(ctx)
	}
	if e == nil || e.Store == nil || e.Runtime == nil || e.Decisions == nil || e.Collector == nil || outcome != "discover" && outcome != "process_input" {
		return errors.New("commissioned discovery unavailable")
	}
	if err := e.Runtime.CheckRound(ctx, outcome); err != nil {
		return err
	}
	return nil
}

func (e *Engine) LaunchRound(_ context.Context, r store.Round) error {
	if r.Outcome == "interview_prepare" || r.Outcome == "interview_debrief" {
		return e.launchInterview(r)
	}
	if r.Outcome == "compare_offers" {
		return e.launchOfferComparison(r)
	}
	if r.Outcome == "prepare" {
		return e.launchPrepare(r)
	}
	if r.Outcome == "process_input" {
		return e.launchInput(r)
	}
	if e == nil || e.Store == nil || e.Runtime == nil || e.Decisions == nil || e.Collector == nil || r.State != store.RoundRunning || r.Outcome != "discover" {
		return store.ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]*activeWorker{}
	}
	if _, exists := e.active[r.ID]; exists {
		return store.ErrConflict
	}
	base := e.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithDeadline(base, r.Deadline)
	worker := &activeWorker{cancel: cancel, done: make(chan struct{})}
	e.active[r.ID] = worker
	go func() {
		defer cancel()
		defer e.workerDone(r.ID, worker)
		e.run(ctx, r)
	}()
	return nil
}

func (e *Engine) CancelRound(id string) {
	e.mu.Lock()
	worker := e.active[id]
	e.mu.Unlock()
	if worker != nil {
		worker.cancel()
	}
}

type report struct {
	Code                     string               `json:"code"`
	BoardID                  string               `json:"boardId,omitempty"`
	CollectorAttemptID       string               `json:"collectorAttemptId,omitempty"`
	CollectorHasMore         bool                 `json:"collectorHasMore"`
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
	AssessedSources          []assessedSource     `json:"assessedSources,omitempty"`
	Recommendation           *homeRecommendation  `json:"recommendation,omitempty"`
}

type assessedSource struct {
	IngestionID                   string `json:"ingestionId"`
	SourceOpeningID               string `json:"sourceOpeningId"`
	OpportunityID                 string `json:"opportunityId"`
	ScreeningAssessmentID         string `json:"screeningAssessmentId"`
	ScreeningStatus               string `json:"screeningStatus"`
	OrganisationAssessmentID      string `json:"organisationAssessmentId"`
	OrganisationStatus            string `json:"organisationStatus"`
	OmittedBytes                  int    `json:"omittedBytes"`
	AssessmentObservationsOmitted int    `json:"assessmentObservationsOmitted"`
	EvidenceSummary               string `json:"evidenceSummary"`
}

type selectedSourcePin struct {
	OutcomeRoundID       string `json:"outcomeRoundId"`
	BatchAttemptID       string `json:"batchAttemptId"`
	BoardID              string `json:"boardId"`
	SourceOpeningID      string `json:"sourceOpeningId"`
	IngestionID          string `json:"ingestionId"`
	ContentSHA256        string `json:"contentSha256"`
	ExtractionRequestKey string `json:"extractionRequestKey"`
	UnreviewedCandidates int    `json:"unreviewedCandidates"`
}

type selectedDiscoveryPin struct {
	CandidateID            string `json:"candidateId"`
	SourceAttemptID        string `json:"sourceAttemptId"`
	VerificationRequestKey string `json:"verificationRequestKey"`
}

type discoveryResearchPin struct {
	Criterion store.RoleCriterion `json:"criterion"`
	SearchID  string              `json:"searchId"`
	Page      int                 `json:"page"`
	TurnKey   string              `json:"turnKey"`
}

type agencyCursor struct {
	CollectorBatchAttemptID    string                `json:"collectorBatchAttemptId,omitempty"`
	ImportedCollectorAttemptID string                `json:"importedCollectorAttemptId,omitempty"`
	CompletedBoardIDs          []string              `json:"completedBoardIds,omitempty"`
	Selected                   *selectedSourcePin    `json:"selected,omitempty"`
	SelectedDiscovery          *selectedDiscoveryPin `json:"selectedDiscovery,omitempty"`
	Research                   *discoveryResearchPin `json:"research,omitempty"`
	Phase                      int                   `json:"phase,omitempty"`
	AssessedSources            []assessedSource      `json:"assessedSources,omitempty"`
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

func (e *Engine) collectorContinuation(ctx context.Context, owner store.Actor, r store.Round, boardID string) (collector.Cursor, string, error) {
	var saved struct {
		AttemptID string `json:"collectorBatchAttemptId"`
	}
	if err := json.Unmarshal(r.Cursor, &saved); err != nil {
		return collector.Cursor{}, "", err
	}
	attemptID := saved.AttemptID
	var payload json.RawMessage
	if attemptID == "" {
		var err error
		attemptID, payload, err = e.Store.LatestRoundCollectorContinuation(ctx, owner, boardID)
		if errors.Is(err, store.ErrNotFound) {
			return collector.Cursor{}, "", nil
		}
		if err != nil {
			return collector.Cursor{}, "", err
		}
		if _, err := e.Store.ImportRoundCollectorCursor(ctx, owner, r.ID, attemptID); err != nil {
			return collector.Cursor{}, "", err
		}
	} else {
		var err error
		payload, err = e.Store.RoundCollectorBatch(ctx, attemptID)
		if err != nil {
			return collector.Cursor{}, "", err
		}
	}
	var batch collector.Batch
	if err := json.Unmarshal(payload, &batch); err != nil {
		return collector.Cursor{}, "", err
	}
	if batch.Next == nil {
		prior, err := e.Store.RoundAttempt(ctx, attemptID)
		if err != nil {
			return collector.Cursor{}, "", err
		}
		if prior.ResourceID == "board:"+boardID {
			return collector.Cursor{}, "", store.ErrFenced
		}
		return collector.Cursor{}, attemptID, nil
	}
	if batch.Next.BoardID != boardID {
		return collector.Cursor{}, "", store.ErrFenced
	}
	return *batch.Next, attemptID, nil
}

func (e *Engine) acquireCollectorBatch(ctx context.Context, owner store.Actor, r store.Round, board store.CollectorBoard) (string, string) {
	cursor, priorAttemptID, err := e.collectorContinuation(ctx, owner, r, board.ID)
	if err != nil {
		return "", terminalCode(err)
	}
	requestKey := "collect:" + board.ID
	if priorAttemptID != "" {
		requestKey += ":" + priorAttemptID
	}
	maxPages := 1
	if len(cursor.Pending) > 0 {
		maxPages = 0
	}
	companyKnown, err := e.boardCompanyKnown(ctx, board.OfficialCareersURL)
	if err != nil {
		return "", "company_inventory_unavailable"
	}
	reservedWrites := int64(2) // A new employer needs a company and a sourced opportunity.
	if companyKnown {
		reservedWrites = 1
	}
	maxItems := r.Limits.Items - r.Used.Items - reservedWrites
	if maxItems > 4 {
		maxItems = 4
	}
	if maxItems < 1 {
		return "", "source_item_allowance_insufficient"
	}
	reserve, created, err := e.Store.ReserveCollectorAcquisition(ctx, owner, r.ID, store.RoundCollectorAcquisitionInput{RequestKey: requestKey, BoardID: board.ID, CursorAttemptID: priorAttemptID, MaxPages: maxPages, MaxItems: int(maxItems)})
	if err != nil || !created {
		return "", terminalCode(err)
	}
	attemptID := reserve.ID
	if _, err = e.Store.MarkRoundDispatched(ctx, r.ID, attemptID); err != nil {
		return "", "source_dispatch_failed"
	}
	batch, fetchErr := e.Collector.AcquireLever(ctx, collector.Request{Board: board, Cursor: cursor, MaxPages: maxPages, MaxItems: int(maxItems)})
	if fetchErr != nil {
		batch.ErrorCode = "source_acquisition_failed"
		cursor.BoardID = board.ID
		batch.Next = &cursor
	}
	// Even a transport failure is published with its cursor and charge.
	staging, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	current, readErr := e.Store.Round(staging, r.ID)
	if readErr == nil {
		encoded, encodeErr := json.Marshal(batch)
		if encodeErr == nil {
			_, readErr = e.Store.SaveRoundCollectorBatch(staging, owner, r.ID, attemptID, current.Revision, encoded)
		} else {
			readErr = encodeErr
		}
	}
	cancel()
	if readErr != nil {
		settle, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, _ = e.Store.FinishRoundAttempt(settle, owner, r.ID, attemptID, false, nil, "collector_stage_failed")
		done()
		return "", "source_result_uncertain"
	}
	if batch.ErrorCode != "" {
		return "", batch.ErrorCode
	}
	return attemptID, ""
}

func (e *Engine) boardCompanyKnown(ctx context.Context, officialURL string) (bool, error) {
	page, err := e.Store.ListCompanies(ctx, store.CompanyListOptions{Limit: 100})
	for err == nil {
		for _, company := range page.Items {
			if company.ArchivedAt == "" && company.Website != "" && strings.EqualFold(company.Website, officialURL) {
				return true, nil
			}
		}
		if page.NextCursor == "" {
			return false, nil
		}
		page, err = e.Store.ListCompanies(ctx, store.CompanyListOptions{Cursor: page.NextCursor, Limit: 100})
	}
	return false, err
}

var errProfileChanged = errors.New("profile changed during commissioned round")

func (e *Engine) run(ctx context.Context, initial store.Round) {
	owner := initial.Actor
	code, partial, detail := "no_supported_source", true, report{}
	defer func() {
		if !errors.Is(ctx.Err(), context.Canceled) {
			detail.Recommendation = e.computeHomeRecommendation(ctx, initial, detail)
			e.finish(ctx, initial.ID, owner, code, partial, detail)
		}
	}()
	for {
		var again bool
		code, partial, detail, again = e.runDiscoveryPhase(ctx, initial)
		if !again {
			return
		}
		var err error
		initial, err = e.Store.Round(ctx, initial.ID)
		if err != nil {
			code, partial = "round_progress_unavailable", true
			return
		}
	}
}

func (e *Engine) runDiscoveryPhase(ctx context.Context, initial store.Round) (code string, partial bool, detail report, again bool) {
	owner := initial.Actor
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	code, partial = "no_supported_source", true
	r, err := e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		code = terminalCode(err)
		return
	}
	var cursor agencyCursor
	if json.Unmarshal(r.Cursor, &cursor) != nil {
		code = "round_cursor_invalid"
		return
	}
	detail.AssessedSources = append([]assessedSource(nil), cursor.AssessedSources...)
	detail.Opportunities = len(detail.AssessedSources)
	attemptID := cursor.CollectorBatchAttemptID
	outcomeRoundID := r.ID
	var chosen *store.CollectorSourceOutcome
	pinnedAtLaunch := cursor.Selected != nil
	importedBoardID := ""
	if !pinnedAtLaunch && attemptID != "" {
		stagedAttempt, readErr := e.Store.RoundAttempt(ctx, attemptID)
		if readErr != nil || stagedAttempt.Operation != store.RoundCollectorPage || stagedAttempt.State != store.AttemptSucceeded || !strings.HasPrefix(stagedAttempt.ResourceID, "board:") || !hasRoundResource(r.Scope.Resources, stagedAttempt.ResourceID) {
			code = "source_batch_invalid"
			return
		}
		if cursor.ImportedCollectorAttemptID == attemptID && r.Step == "collector_cursor_imported" {
			if _, err := e.Store.ImportRoundCollectorCursor(ctx, owner, r.ID, attemptID); err != nil {
				code = "source_batch_invalid"
				return
			}
			importedBoardID = strings.TrimPrefix(stagedAttempt.ResourceID, "board:")
			attemptID = ""
		} else if stagedAttempt.RoundID != r.ID {
			code = "source_batch_invalid"
			return
		}
		detail.BoardID = strings.TrimPrefix(stagedAttempt.ResourceID, "board:")
	}
	if importedBoardID != "" {
		boards, readErr := e.Store.ListCollectorBoards(ctx)
		if readErr != nil {
			code = "source_inventory_failed"
			return
		}
		var board store.CollectorBoard
		for _, candidate := range boards {
			if candidate.ID == importedBoardID && candidate.Enabled && candidate.VerifiedAt != "" {
				board = candidate
				break
			}
		}
		if board.ID == "" {
			code = "source_batch_invalid"
			return
		}
		r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
		if err != nil {
			code = terminalCode(err)
			return
		}
		attemptID, code = e.acquireCollectorBatch(ctx, owner, r, board)
		if code != "" {
			return
		}
	}
	if cursor.Selected != nil {
		pin := cursor.Selected
		attemptID, outcomeRoundID = pin.BatchAttemptID, pin.OutcomeRoundID
		if pin.BatchAttemptID == "" || pin.OutcomeRoundID == "" || pin.BoardID == "" || pin.SourceOpeningID == "" || pin.IngestionID == "" || pin.ExtractionRequestKey == "" ||
			!hasRoundResource(r.Scope.Resources, "board:"+pin.BoardID) {
			code = "selected_source_invalid"
			return
		}
		boardAttempt, readErr := e.Store.RoundAttempt(ctx, attemptID)
		if readErr != nil || boardAttempt.RoundID != outcomeRoundID || boardAttempt.ResourceID != "board:"+pin.BoardID || boardAttempt.State != store.AttemptSucceeded {
			code = "selected_source_invalid"
			return
		}
		outcomes, readErr := e.Store.RoundCollectorOutcomes(ctx, outcomeRoundID, attemptID)
		if readErr != nil {
			code = "selected_source_unavailable"
			return
		}
		for i := range outcomes {
			candidate := &outcomes[i]
			if candidate.SourceOpeningID == pin.SourceOpeningID && candidate.IngestionID == pin.IngestionID && candidate.ContentSHA256 == pin.ContentSHA256 && candidate.Current {
				chosen = candidate
				break
			}
		}
		if chosen == nil {
			code = "selected_source_stale"
			return
		}
		detail.BoardID, detail.CollectorAttemptID, detail.UnreviewedCandidates = pin.BoardID, attemptID, pin.UnreviewedCandidates
	}
	if cursor.Selected == nil && (cursor.Research != nil || cursor.SelectedDiscovery != nil) {
		if cursor.SelectedDiscovery == nil {
			var staged int
			r, cursor, staged, err = e.continueDiscoveryResearch(ctx, r, cursor)
			detail.DiscoveryCandidates = staged
			if err != nil {
				code = terminalCode(err)
				return
			}
		}
		board, verifyErr := e.verifySelectedDiscoveryLead(ctx, r, cursor.SelectedDiscovery)
		if verifyErr != nil {
			code = "discovery_verification_unresolved"
			if errors.Is(verifyErr, store.ErrUncertain) {
				code = "discovery_verification_uncertain"
			}
			return
		}
		detail.BoardID = board.ID
		if attemptID == "" {
			backlogRoundID, backlogAttemptID, backlogErr := e.Store.OldestPendingCollectorBatch(ctx, owner, board.ID)
			if backlogErr == nil {
				attemptID, outcomeRoundID = backlogAttemptID, backlogRoundID
			} else if !errors.Is(backlogErr, store.ErrNotFound) {
				code = "source_backlog_unavailable"
				return
			}
			if attemptID == "" {
				r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
				if err != nil {
					code = terminalCode(err)
					return
				}
				attemptID, code = e.acquireCollectorBatch(ctx, owner, r, board)
				if code != "" {
					return
				}
			}
		}
	}
	if cursor.Selected == nil {
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
			input := jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: r.Intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
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
			selectedID, err := e.savedOrRunDecision(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: "select-board", ProfileVersion: r.ProfileVersion}, input)
			if err != nil {
				code = terminalCode(err)
				return
			}
			if selectedID == "" {
				code = "source_choice_unresolved"
				return
			}
			var board store.CollectorBoard
			if criterion, search := searchChoices[selectedID]; search {
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
				r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
				if err != nil {
					code = terminalCode(err)
					return
				}
				if json.Unmarshal(r.Cursor, &cursor) != nil || cursor.Research != nil || cursor.SelectedDiscovery != nil {
					code = "round_cursor_conflict"
					return
				}
				cursor.Research = &discoveryResearchPin{Criterion: criterion, SearchID: selectedID, Page: pageNumber,
					TurnKey: fmt.Sprintf("discover:%s:g%d", selectedID, r.Generation)}
				encodedCursor, _ := json.Marshal(cursor)
				r, err = e.Store.SaveRoundProgress(ctx, owner, r.ID, r.Revision, store.RoundProgress{Step: "discovery_research_selected", Cursor: encodedCursor, Unresolved: r.Unresolved, Report: r.Report})
				if err != nil {
					code = terminalCode(err)
					return
				}
				var staged int
				r, cursor, staged, err = e.continueDiscoveryResearch(ctx, r, cursor)
				detail.DiscoveryCandidates = staged
				if err != nil {
					code = "discovery_choice_unresolved"
					return
				}
				board, err = e.verifySelectedDiscoveryLead(ctx, r, cursor.SelectedDiscovery)
				if err != nil {
					code = "discovery_verification_unresolved"
					if errors.Is(err, store.ErrUncertain) {
						code = "discovery_verification_uncertain"
					}
					return
				}
			} else {
				var found bool
				board, found = choices[selectedID]
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
			backlogRoundID, backlogAttemptID, backlogErr := e.Store.OldestPendingCollectorBatch(ctx, owner, board.ID)
			if backlogErr == nil {
				attemptID, outcomeRoundID = backlogAttemptID, backlogRoundID
			} else if !errors.Is(backlogErr, store.ErrNotFound) {
				code = "source_backlog_unavailable"
				return
			}
			if attemptID == "" {
				attemptID, code = e.acquireCollectorBatch(ctx, owner, r, board)
				if code != "" {
					return
				}
			}
		}
		detail.CollectorAttemptID = attemptID
		staged, err := e.Store.RoundCollectorBatch(ctx, attemptID)
		if err != nil {
			code = "source_batch_unavailable"
			return
		}
		var stagedBoundary struct {
			Next *collector.Cursor `json:"next"`
		}
		if json.Unmarshal(staged, &stagedBoundary) != nil {
			code = "source_batch_invalid"
			return
		}
		detail.CollectorHasMore = stagedBoundary.Next != nil
		outcomes, err := e.Store.RoundCollectorOutcomes(ctx, outcomeRoundID, attemptID)
		if err != nil {
			code = "source_outcomes_unavailable"
			return
		}
		candidateInput := jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: r.Intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
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
			if item.Current && item.SourceID == "" && (item.Decision == "new" || item.Decision == "changed") {
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
		selectedID, err := e.savedOrRunDecision(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: fmt.Sprintf("select-source:%s:p%d", attemptID, cursor.Phase), ProfileVersion: r.ProfileVersion}, candidateInput)
		if err != nil {
			code = terminalCode(err)
			return
		}
		if selectedID == "" {
			code = "source_choice_unresolved"
			return
		}
		selected, found := candidateOutcomes[selectedID]
		if !found {
			code = "source_choice_invalid"
			return
		}
		chosen = &selected
		r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
		if err != nil {
			code = terminalCode(err)
			return
		}
		if json.Unmarshal(r.Cursor, &cursor) != nil || cursor.Selected != nil {
			code = "round_cursor_conflict"
			return
		}
		cursor.Selected = &selectedSourcePin{OutcomeRoundID: outcomeRoundID, BatchAttemptID: attemptID,
			BoardID: detail.BoardID, SourceOpeningID: chosen.SourceOpeningID, IngestionID: chosen.IngestionID,
			ContentSHA256: chosen.ContentSHA256, ExtractionRequestKey: fmt.Sprintf("extract:%s:g%d", chosen.IngestionID, initial.Generation), UnreviewedCandidates: detail.UnreviewedCandidates}
		pinnedCursor, _ := json.Marshal(cursor)
		r, err = e.Store.SaveRoundProgress(ctx, owner, r.ID, r.Revision, store.RoundProgress{Step: "source_selected", Cursor: pinnedCursor, Unresolved: r.Unresolved, Report: r.Report})
		if err != nil {
			code = terminalCode(err)
			return
		}
	} else {
		staged, readErr := e.Store.RoundCollectorBatch(ctx, attemptID)
		if readErr != nil {
			code = "source_batch_unavailable"
			return
		}
		var stagedBoundary struct {
			Next *collector.Cursor `json:"next"`
		}
		if json.Unmarshal(staged, &stagedBoundary) != nil {
			code = "source_batch_invalid"
			return
		}
		detail.CollectorHasMore = stagedBoundary.Next != nil
	}
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
	if pinnedAtLaunch && chosen.SourceID == "" {
		prior, priorErr := e.Store.RoundAttemptForRequest(ctx, r.ID, cursor.Selected.ExtractionRequestKey)
		if priorErr != nil && !errors.Is(priorErr, store.ErrNotFound) {
			code = "source_extraction_evidence_unavailable"
			return
		}
		if priorErr == nil && (prior.DispatchedAt != "" || prior.State != store.AttemptReserved && prior.State != store.AttemptCancelled) {
			code = "source_extraction_unresolved"
			return
		}
		r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
		if err != nil {
			code = terminalCode(err)
			return
		}
		if json.Unmarshal(r.Cursor, &cursor) != nil || cursor.Selected == nil || cursor.Selected.SourceOpeningID != chosen.SourceOpeningID || cursor.Selected.IngestionID != chosen.IngestionID {
			code = "selected_source_invalid"
			return
		}
		cursor.Selected.ExtractionRequestKey = fmt.Sprintf("extract:%s:g%d", chosen.IngestionID, initial.Generation)
		encoded, _ := json.Marshal(cursor)
		r, err = e.Store.SaveRoundProgress(ctx, owner, r.ID, r.Revision, store.RoundProgress{Step: "source_selected", Cursor: encoded, Unresolved: r.Unresolved, Report: r.Report})
		if err != nil {
			code = terminalCode(err)
			return
		}
	}
	if chosen.OpportunityID != "" {
		previous, readErr := e.Store.Opportunity(ctx, chosen.OpportunityID)
		if readErr == nil && previous.OriginalText == item.OriginalText {
			needsSave = false
		}
	}
	if pinnedAtLaunch && chosen.SourceID != "" && needsSave {
		code = "source_mapping_stale"
		return
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
		_, err = e.Runtime.ExecuteRoundTurn(ctx, agent, r.ID, codexservice.RoundTurnInput{RequestKey: cursor.Selected.ExtractionRequestKey, ResourceID: "campaign:active", Brief: brief, Evidence: string(evidence)})
		if err != nil {
			code = terminalCode(err)
			return
		}
	}
	refreshed, err := e.Store.RoundCollectorOutcomes(ctx, outcomeRoundID, attemptID)
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
	screenPrefix := fmt.Sprintf("screen:%s:g%d", chosen.IngestionID, initial.Generation)
	screenInput := jev.ScreeningInput{PreferenceVersion: r.ProfileVersion, MaxTotalTokens: 2500, Criteria: criteria, Spans: spans}
	binding := store.RoundAssessmentInput{Actor: owner, RoundID: r.ID, RoundGeneration: initial.Generation, ResourceID: "campaign:active", OpportunityID: chosen.OpportunityID, OpportunityRevision: currentOpportunity.Revision, SourceID: chosen.SourceID, SourceRevision: chosen.ContentSHA256, ProfileVersion: r.ProfileVersion, OmittedBytes: detail.ScreeningOmittedBytes}
	screenAssessment, currentErr := e.Store.CurrentRoundJevAssessment(ctx, chosen.OpportunityID, "screening")
	if currentErr != nil && !errors.Is(currentErr, store.ErrNotFound) && !errors.Is(currentErr, store.ErrConflict) {
		code = "screening_current_unavailable"
		return
	}
	if currentErr != nil || screenAssessment.RoundID != r.ID || screenAssessment.SourceID != chosen.SourceID || screenAssessment.SourceRevision != chosen.ContentSHA256 || screenAssessment.OpportunityRevision != currentOpportunity.Revision || screenAssessment.ProfileVersion != r.ProfileVersion {
		screening, screenErr := e.Decisions.RunScreening(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: screenPrefix, ProfileVersion: r.ProfileVersion}, screenInput)
		if screenErr != nil {
			code = terminalCode(screenErr)
			return
		}
		screenAttemptIDs, readErr := e.Store.JevAttemptIDsForRequestPrefix(ctx, r.ID, screenPrefix)
		if readErr != nil {
			code = "screening_attempts_unavailable"
			return
		}
		binding.JevAttemptIDs = screenAttemptIDs
		screenAssessment, err = e.Store.ApplyRoundScreening(ctx, binding, screenInput, screening)
		if err != nil {
			code = "screening_apply_failed"
			return
		}
	}
	detail.Screened, detail.ScreeningInputSHA256, detail.ScreeningAssessmentID, detail.ScreeningStatus = true, screenAssessment.InputSHA256, screenAssessment.ID, screenAssessment.Status
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
	organisationPrefix := fmt.Sprintf("organise:%s:g%d", chosen.IngestionID, initial.Generation)
	organisationAssessment, currentErr := e.Store.CurrentRoundJevAssessment(ctx, chosen.OpportunityID, "organisation")
	if currentErr != nil && !errors.Is(currentErr, store.ErrNotFound) && !errors.Is(currentErr, store.ErrConflict) {
		code = "organisation_current_unavailable"
		return
	}
	if currentErr != nil || organisationAssessment.RoundID != r.ID || organisationAssessment.SourceID != chosen.SourceID || organisationAssessment.SourceRevision != chosen.ContentSHA256 || organisationAssessment.OpportunityRevision != currentOpportunity.Revision || organisationAssessment.ProfileVersion != r.ProfileVersion || organisationAssessment.CategoryVersion != categorySet.Version {
		organisation, organiseErr := e.Decisions.RunOrganisation(ctx, jevservice.Binding{Actor: owner, RoundID: r.ID, ResourceID: "campaign:active", RequestKeyPrefix: organisationPrefix, ProfileVersion: r.ProfileVersion}, organisationInput)
		if organiseErr != nil {
			code = terminalCode(organiseErr)
			return
		}
		organisationAttempts, readErr := e.Store.JevAttemptIDsForRequestPrefix(ctx, r.ID, organisationPrefix)
		if readErr != nil {
			code = "organisation_attempts_unavailable"
			return
		}
		binding.JevAttemptIDs = organisationAttempts
		organisationAssessment, err = e.Store.ApplyRoundOrganisation(ctx, binding, organisationInput, organisation)
		if err != nil {
			code = "organisation_apply_failed"
			return
		}
	}
	detail.OrganisationAssessmentID, detail.OrganisationStatus = organisationAssessment.ID, organisationAssessment.Status
	code = "sourced_opportunity_assessed"
	partial = detail.CollectorHasMore || detail.ScreeningOmittedBytes > 0 || detail.UnreviewedCandidates > 0 || screenAssessment.Status != "proposed" || organisationAssessment.Status != "selected"
	summary, assessmentOmitted := assessedEvidenceSummary(screenAssessment, organisationAssessment, detail.ScreeningOmittedBytes)
	completed := assessedSource{IngestionID: chosen.IngestionID, SourceOpeningID: chosen.SourceOpeningID, OpportunityID: chosen.OpportunityID,
		ScreeningAssessmentID: screenAssessment.ID, ScreeningStatus: screenAssessment.Status,
		OrganisationAssessmentID: organisationAssessment.ID, OrganisationStatus: organisationAssessment.Status, OmittedBytes: detail.ScreeningOmittedBytes,
		AssessmentObservationsOmitted: assessmentOmitted, EvidenceSummary: summary}
	detail.AssessedSources = append(append([]assessedSource(nil), cursor.AssessedSources...), completed)
	if detail.UnreviewedCandidates == 0 || screenAssessment.Status != "proposed" || organisationAssessment.Status != "selected" || detail.ScreeningOmittedBytes != 0 {
		return
	}
	r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		code, partial = terminalCode(err), true
		return
	}
	neededRequests := int64(6)
	priorNext, priorErr := e.Store.RoundAttemptForRequest(ctx, r.ID, fmt.Sprintf("next-outcome:p%d/0", cursor.Phase))
	if priorErr != nil && !errors.Is(priorErr, store.ErrNotFound) {
		code, partial = "next_outcome_evidence_unavailable", true
		return
	}
	if priorErr == nil {
		if priorNext.State != store.AttemptSucceeded {
			code, partial = "next_outcome_uncertain", true
			return
		}
		neededRequests = 5
	}
	if r.Limits.Requests-r.Used.Requests < neededRequests || r.Limits.Items-r.Used.Items < 1 || r.Limits.Tools-r.Used.Tools < 2 || r.Limits.Turns-r.Used.Turns < 1 {
		return
	}
	next, finishedByChoice, nextErr := e.chooseNextDiscoveryOutcome(ctx, r, cursor, *chosen, item, detail.AssessedSources)
	if nextErr != nil {
		code, partial = terminalCode(nextErr), true
		return
	}
	if finishedByChoice {
		return
	}
	if next == nil {
		code, partial = "next_outcome_unresolved", true
		return
	}
	r, err = e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		code, partial = terminalCode(err), true
		return
	}
	cursor.AssessedSources = detail.AssessedSources
	cursor.Phase++
	cursor.Selected = &selectedSourcePin{OutcomeRoundID: outcomeRoundID, BatchAttemptID: attemptID,
		BoardID: detail.BoardID, SourceOpeningID: next.SourceOpeningID, IngestionID: next.IngestionID,
		ContentSHA256: next.ContentSHA256, ExtractionRequestKey: fmt.Sprintf("extract:%s:g%d", next.IngestionID, initial.Generation),
		UnreviewedCandidates: detail.UnreviewedCandidates - 1}
	encodedCursor, _ := json.Marshal(cursor)
	if _, err = e.Store.SaveRoundProgress(ctx, owner, r.ID, r.Revision, store.RoundProgress{Step: "next_source_selected", Cursor: encodedCursor, Unresolved: r.Unresolved, Report: r.Report}); err != nil {
		code, partial = terminalCode(err), true
		return
	}
	again = true
	return
}

func hasRoundResource(resources []string, target string) bool {
	for _, resource := range resources {
		if resource == target {
			return true
		}
	}
	return false
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
	case errors.Is(err, errDecisionContextTooLarge):
		return "decision_context_too_large"
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
