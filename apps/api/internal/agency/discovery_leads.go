package agency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (e *Engine) selectDiscoveryLead(ctx context.Context, r store.Round, cursor agencyCursor) (store.Round, agencyCursor, int, error) {
	candidates, err := e.Store.RoundDiscoveryCandidates(ctx, r.ID)
	if err != nil || len(candidates) == 0 {
		if err == nil {
			err = store.ErrNotFound
		}
		return r, cursor, 0, err
	}
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != r.ProfileVersion {
		return r, cursor, len(candidates), errProfileChanged
	}
	profileSources, profileIDs, complete := profileDecisionSources(profile, 4)
	if !complete {
		return r, cursor, len(candidates), store.ErrInvalid
	}
	input := jev.DecisionInput{Kind: jev.DecisionSourceResearch, CampaignIntent: r.Intent, MaxReportedTokens: discoveryDecisionReportedTokenLimit,
		Capabilities: []jev.DecisionCapability{{ID: "official_verify", Description: "Verify one saved unverified public lead against its company detail and official careers site; register only an evidenced Lever board."}},
		Sources:      profileSources, RemainingAllowance: []jev.DecisionAllowance{{Operation: store.RoundCodexTurn, Remaining: r.Limits.Turns - r.Used.Turns}}}
	for _, c := range candidates {
		if c.Kind != "job" || len(input.Candidates) >= 8 {
			continue
		}
		input.Sources = append(input.Sources, jev.DecisionSource{ID: c.ID, SourceRevision: c.SourceSHA256,
			SourceKind: "unverified_public_search_lead", URL: c.URL, ObservedAt: c.ObservedAt, Excerpt: c.EvidenceQuote})
		input.Candidates = append(input.Candidates, jev.DecisionCandidate{ID: c.ID, Description: c.Title,
			Scope:        "Only verify this exact saved lead using its claimed company detail and an evidenced official careers link; no opportunity from summary text.",
			CapabilityID: "official_verify", SourceIDs: append([]string{c.ID}, profileIDs...)})
	}
	if len(input.Candidates) == 0 {
		return r, cursor, len(candidates), store.ErrNotFound
	}
	selectedID, err := e.savedOrRunDecision(ctx, jevservice.Binding{Actor: r.Actor, RoundID: r.ID, ResourceID: "campaign:active",
		RequestKeyPrefix: fmt.Sprintf("select-discovery:p%d", cursor.Phase), ProfileVersion: r.ProfileVersion}, input)
	if err != nil {
		return r, cursor, len(candidates), err
	}
	if selectedID == "" {
		return r, cursor, len(candidates), store.ErrConflict
	}
	selected, err := e.Store.RoundDiscoveryCandidate(ctx, r.ID, selectedID)
	if err != nil || selected.Kind != "job" {
		return r, cursor, len(candidates), store.ErrFenced
	}
	r, err = e.live(ctx, r.ID, r.Generation, r.ProfileVersion)
	if err != nil {
		return r, cursor, len(candidates), err
	}
	if json.Unmarshal(r.Cursor, &cursor) != nil || cursor.SelectedDiscovery != nil || cursor.Selected != nil {
		return r, cursor, len(candidates), store.ErrConflict
	}
	cursor.SelectedDiscovery = &selectedDiscoveryPin{CandidateID: selected.ID, SourceAttemptID: selected.AttemptID,
		VerificationRequestKey: fmt.Sprintf("verify:%s:g%d", selected.ID, r.Generation)}
	encoded, _ := json.Marshal(cursor)
	r, err = e.Store.SaveRoundProgress(ctx, r.Actor, r.ID, r.Revision, store.RoundProgress{Step: "discovery_lead_selected", Cursor: encoded, Unresolved: r.Unresolved, Report: r.Report})
	return r, cursor, len(candidates), err
}

func (e *Engine) continueDiscoveryResearch(ctx context.Context, r store.Round, cursor agencyCursor) (store.Round, agencyCursor, int, error) {
	pin := cursor.Research
	if pin == nil || pin.Criterion.ID == "" || pin.Criterion.Label == "" || pin.SearchID == "" || pin.Page < 1 || pin.TurnKey == "" || cursor.Selected != nil || cursor.SelectedDiscovery != nil {
		return r, cursor, 0, store.ErrInvalid
	}
	prior, priorErr := e.Store.RoundAttemptForRequest(ctx, r.ID, pin.TurnKey)
	if priorErr != nil && !errors.Is(priorErr, store.ErrNotFound) {
		return r, cursor, 0, priorErr
	}
	if priorErr == nil && (prior.Operation != store.RoundCodexTurn || prior.ResourceID != "discovery:himalayas") {
		return r, cursor, 0, store.ErrFenced
	}
	if priorErr == nil && prior.State == store.AttemptObservedSuccess {
		if err := e.checkReconciledDiscoveryResearch(ctx, r.ID, pin, prior); err != nil {
			return r, cursor, 0, err
		}
	} else if priorErr == nil && prior.State != store.AttemptSucceeded && prior.DispatchedAt != "" {
		return r, cursor, 0, store.ErrUncertain
	}
	if priorErr != nil || prior.State != store.AttemptSucceeded && prior.State != store.AttemptObservedSuccess {
		key := fmt.Sprintf("discover:%s:g%d", pin.SearchID, r.Generation)
		if pin.TurnKey != key {
			r, priorErr = e.live(ctx, r.ID, r.Generation, r.ProfileVersion)
			if priorErr != nil {
				return r, cursor, 0, priorErr
			}
			cursor.Research.TurnKey = key
			encoded, _ := json.Marshal(cursor)
			r, priorErr = e.Store.SaveRoundProgress(ctx, r.Actor, r.ID, r.Revision, store.RoundProgress{Step: "discovery_research_selected", Cursor: encoded, Unresolved: r.Unresolved, Report: r.Report})
			if priorErr != nil {
				return r, cursor, 0, priorErr
			}
		}
		stagingCapacity := r.Limits.Items - r.Used.Items - 4 // board, one collector item, new company, sourced opportunity
		if stagingCapacity > 4 {
			stagingCapacity = 4
		}
		if stagingCapacity < 1 {
			return r, cursor, 0, store.ErrAllowance
		}
		brief := fmt.Sprintf("Read one bounded public vacancy search for the selected owner criterion using source_discovery with method search_jobs, the exact keyword and page in evidence, and empty country. Stage up to %d distinct job links present in the saved response with discovery_candidate_stage and exact evidence quotes, without ranking relevance or choosing one yourself. Stop after neutral staging; Jev will choose among the saved candidates before any company or official-site verification. Do not create an opportunity from a search summary or company detail.", stagingCapacity)
		evidence, _ := json.Marshal(struct {
			Criterion store.RoleCriterion `json:"criterion"`
			Keyword   string              `json:"keyword"`
			Page      int                 `json:"page"`
		}{pin.Criterion, pin.Criterion.Label, pin.Page})
		if _, err := e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID,
			codexservice.RoundTurnInput{RequestKey: cursor.Research.TurnKey, ResourceID: "discovery:himalayas", Brief: brief, Evidence: string(evidence)}); err != nil {
			return r, cursor, 0, err
		}
	}
	r, err := e.live(ctx, r.ID, r.Generation, r.ProfileVersion)
	if err != nil {
		return r, cursor, 0, err
	}
	return e.selectDiscoveryLead(ctx, r, cursor)
}

func (e *Engine) checkReconciledDiscoveryResearch(ctx context.Context, roundID string, pin *discoveryResearchPin, turn store.RoundAttempt) error {
	candidates, err := e.Store.RoundDiscoveryCandidates(ctx, roundID)
	if err != nil || len(candidates) == 0 {
		return store.ErrUncertain
	}
	for _, candidate := range candidates {
		read, err := e.Store.DiscoveryHTTP(ctx, candidate.AttemptID)
		if err != nil || read.RoundID != roundID || read.Method != "search_jobs" || read.StatusCode != 200 || read.ErrorCode != "" || read.ResponseSHA256 != candidate.SourceSHA256 {
			return store.ErrUncertain
		}
		searchAttempt, err := e.Store.RoundAttempt(ctx, candidate.AttemptID)
		if err != nil || searchAttempt.Operation != store.RoundSearchSource || searchAttempt.State != store.AttemptSucceeded || searchAttempt.Generation != turn.Generation || !attemptCreatedAtOrAfter(searchAttempt.CreatedAt, turn.CreatedAt) {
			return store.ErrUncertain
		}
		stageAttempt, err := e.Store.RoundAttempt(ctx, candidate.ID)
		if err != nil || stageAttempt.Operation != store.RoundStageDiscovery || stageAttempt.State != store.AttemptSucceeded || stageAttempt.Generation != turn.Generation || !attemptCreatedAtOrAfter(stageAttempt.CreatedAt, searchAttempt.CreatedAt) {
			return store.ErrUncertain
		}
		var call struct {
			Params struct {
				Arguments struct {
					Keyword string `json:"keyword"`
					Country string `json:"country"`
					Page    int    `json:"page"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(read.RequestJSON), &call) != nil || call.Params.Arguments.Keyword != pin.Criterion.Label || call.Params.Arguments.Country != "" || call.Params.Arguments.Page != pin.Page {
			return store.ErrUncertain
		}
	}
	return nil
}

func attemptCreatedAtOrAfter(current, earlier string) bool {
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current)
	earlierTime, earlierErr := time.Parse(time.RFC3339Nano, earlier)
	return currentErr == nil && earlierErr == nil && !currentTime.Before(earlierTime)
}

func (e *Engine) verifySelectedDiscoveryLead(ctx context.Context, r store.Round, pin *selectedDiscoveryPin) (store.CollectorBoard, error) {
	if pin == nil || pin.CandidateID == "" || pin.SourceAttemptID == "" || pin.VerificationRequestKey == "" {
		return store.CollectorBoard{}, store.ErrInvalid
	}
	selected, err := e.Store.RoundDiscoveryCandidate(ctx, r.ID, pin.CandidateID)
	if err != nil || selected.Kind != "job" || selected.AttemptID != pin.SourceAttemptID {
		return store.CollectorBoard{}, store.ErrFenced
	}
	boardID, boardErr := e.Store.RoundDiscoveryBoardForCandidate(ctx, r.ID, selected.ID)
	if boardErr != nil && !errors.Is(boardErr, store.ErrNotFound) {
		return store.CollectorBoard{}, boardErr
	}
	if errors.Is(boardErr, store.ErrNotFound) {
		prior, priorErr := e.Store.RoundAttemptForRequest(ctx, r.ID, pin.VerificationRequestKey)
		if priorErr != nil && !errors.Is(priorErr, store.ErrNotFound) {
			return store.CollectorBoard{}, priorErr
		}
		if priorErr == nil && prior.DispatchedAt != "" {
			return store.CollectorBoard{}, store.ErrUncertain
		}
		currentKey := fmt.Sprintf("verify:%s:g%d", selected.ID, r.Generation)
		if pin.VerificationRequestKey != currentKey {
			live, liveErr := e.live(ctx, r.ID, r.Generation, r.ProfileVersion)
			if liveErr != nil {
				return store.CollectorBoard{}, liveErr
			}
			var cursor agencyCursor
			if json.Unmarshal(live.Cursor, &cursor) != nil || cursor.SelectedDiscovery == nil || cursor.SelectedDiscovery.CandidateID != selected.ID {
				return store.CollectorBoard{}, store.ErrFenced
			}
			cursor.SelectedDiscovery.VerificationRequestKey = currentKey
			encoded, _ := json.Marshal(cursor)
			live, liveErr = e.Store.SaveRoundProgress(ctx, live.Actor, live.ID, live.Revision, store.RoundProgress{Step: "discovery_lead_selected", Cursor: encoded, Unresolved: live.Unresolved, Report: live.Report})
			if liveErr != nil {
				return store.CollectorBoard{}, liveErr
			}
			r, pin = live, cursor.SelectedDiscovery
		}
		evidence, _ := json.Marshal(struct {
			Candidate store.RoundDiscoveryCandidate `json:"candidate"`
		}{selected})
		brief := "Verify only the owner-round selected unverified public lead in evidence. Read its matching company detail with source_discovery, then use discovery_official_links on the claimed company website and at most one evidenced same-origin careers link. Register a board using discovery_board_register only if an exact Lever link appears in the saved official read. Do not stage or select another candidate, create an opportunity from search summary text, infer missing employer facts, or browse unrelated sources."
		if _, err := e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID,
			codexservice.RoundTurnInput{RequestKey: pin.VerificationRequestKey, ResourceID: "discovery:himalayas", Brief: brief, Evidence: string(evidence)}); err != nil {
			return store.CollectorBoard{}, err
		}
		boardID, err = e.Store.RoundDiscoveryBoardForCandidate(ctx, r.ID, selected.ID)
		if err != nil {
			return store.CollectorBoard{}, err
		}
	}
	boards, err := e.Store.ListCollectorBoards(ctx)
	if err != nil {
		return store.CollectorBoard{}, err
	}
	for _, board := range boards {
		if board.ID == boardID && board.Enabled && board.VerifiedAt != "" {
			return board, nil
		}
	}
	return store.CollectorBoard{}, store.ErrFenced
}
