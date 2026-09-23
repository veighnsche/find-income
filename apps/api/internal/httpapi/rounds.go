package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// The owner only selects Start. Scope, budget and deadline are derived from
// current server records, so no browser form grants its own authority.
func (h *Handler) defaultRoundInput(ctx context.Context, requestKey string) (store.StartRoundInput, error) {
	profile, err := h.database.CurrentPreferences(ctx)
	if err != nil {
		return store.StartRoundInput{}, err
	}
	boards, err := h.database.ListCollectorBoards(ctx)
	if err != nil {
		return store.StartRoundInput{}, err
	}
	resources := []string{"campaign:active", "profile:current", "discovery:himalayas"}
	inputRefs := []string{"profile:current", "campaign:active"}
	instructions, err := h.database.OwnerInstructions(ctx, "")
	if err != nil {
		return store.StartRoundInput{}, err
	}
	for _, instruction := range instructions {
		if instruction.RoundID == "" && (instruction.TargetKind == "profile" &&
			instruction.TargetID == "current" && instruction.ExpectedRevision == profile.Version ||
			instruction.TargetKind == "evidence" || instruction.TargetKind == "opportunity" || instruction.TargetKind == "relationship") {
			inputRefs = append(inputRefs, "instruction:"+instruction.ID)
			if instruction.TargetKind == "relationship" {
				resources = append(resources, "relationship:"+instruction.TargetID)
			}
		}
	}
	for _, board := range boards {
		if board.Enabled && board.VerifiedAt != "" {
			resources = append(resources, "board:"+board.ID)
		}
	}
	cursor := ""
	for {
		companies, err := h.database.ListCompanies(ctx, store.CompanyListOptions{Cursor: cursor, Limit: 100})
		if err != nil {
			return store.StartRoundInput{}, err
		}
		for _, company := range companies.Items {
			resources = append(resources, "company:"+company.ID)
		}
		if companies.NextCursor == "" {
			break
		}
		if len(resources) > 950 {
			return store.StartRoundInput{}, store.ErrInvalid
		}
		cursor = companies.NextCursor
	}
	return store.StartRoundInput{RequestKey: requestKey,
		Intent:  "Discover source-linked work opportunities for the current owner profile.",
		Outcome: "discover", ProfileVersion: profile.Version,
		Scope: store.RoundScope{InputRefs: inputRefs,
			Resources: resources, Operations: []string{store.RoundCreateCompany, store.RoundCreateOpportunity, store.RoundSaveSourceOpportunity, store.RoundCorrectPreferences, store.RoundCorrectEvidence, store.RoundCorrectOpportunity, store.RoundRelationshipCounterpartyCreate, store.RoundRelationshipEventCreate, store.RoundRelationshipRouteCreate, store.RoundRelationshipCorrect, store.RoundStageDiscovery, store.RoundRegisterDiscoveryBoard, store.RoundCollectorPage, store.RoundSearchSource, store.RoundFetchSource, store.RoundJevRequest, store.RoundCodexTurn, store.RoundContextTool},
			Delegates: []string{"codex-runner"}},
		Limits:   store.RoundAllowance{Requests: 16, Items: 10, Tools: 18, Turns: 4},
		Deadline: time.Now().Add(30 * time.Minute).UTC()}, nil
}

type roundStartRequest struct {
	RequestKey string `json:"requestKey"`
}

type prepareRoundRequest struct {
	RequestKey    string `json:"requestKey"`
	OpportunityID string `json:"opportunityId"`
}

func (h *Handler) prepareRound(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body prepareRoundRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	if body.RequestKey == "" || len(body.RequestKey) > 200 || strings.TrimSpace(body.RequestKey) != body.RequestKey || body.OpportunityID == "" || len(body.OpportunityID) > 128 || strings.TrimSpace(body.OpportunityID) != body.OpportunityID {
		failRound(w, store.ErrInvalid)
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	previous, err := h.database.RoundByRequest(r.Context(), actor, body.RequestKey)
	if err == nil {
		if previous.Outcome != "prepare" || len(previous.Scope.Resources) != 1 || previous.Scope.Resources[0] != "opportunity:"+body.OpportunityID {
			failRound(w, store.ErrRoundIdempotencyConflict)
			return
		}
		writeJSON(w, http.StatusOK, roundModel(previous))
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		failRound(w, err)
		return
	}
	opportunity, err := h.database.Opportunity(r.Context(), body.OpportunityID)
	if err != nil {
		failRound(w, err)
		return
	}
	if opportunity.ArchivedAt != "" || opportunity.SourceURL == "" || strings.TrimSpace(opportunity.OriginalText) == "" {
		failRound(w, store.ErrInvalid)
		return
	}
	selection, err := h.database.OwnerOpportunityDecision(r.Context(), opportunity.ID)
	if errors.Is(err, store.ErrNotFound) || err == nil && (selection.Decision != "selected" || selection.OpportunityRevision != opportunity.Revision) {
		failRound(w, store.ErrFenced)
		return
	}
	if err != nil {
		failRound(w, err)
		return
	}
	profile, err := h.database.CurrentPreferences(r.Context())
	if err != nil {
		failRound(w, err)
		return
	}
	input := store.StartRoundInput{RequestKey: body.RequestKey, Intent: "Prepare a private application pack for the selected sourced opportunity.", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{"profile:current", "opportunity:" + opportunity.ID}, Resources: []string{"opportunity:" + opportunity.ID}, Operations: []string{store.RoundCodexTurn, store.RoundJevRequest, store.RoundPrepareApplicationPack, store.RoundContextTool}, Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 7, Items: 1, Tools: 3, Turns: 1}, Deadline: time.Now().Add(30 * time.Minute).UTC()}
	round, created, err := h.rounds.Start(r.Context(), actor, input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, roundModel(round))
}

type roundResponse struct {
	ID                     string               `json:"id"`
	Intent                 string               `json:"intent"`
	Outcome                string               `json:"outcome"`
	ProfileVersion         int64                `json:"profileVersion"`
	Scope                  store.RoundScope     `json:"scope"`
	State                  store.RoundState     `json:"state"`
	Revision               int64                `json:"revision"`
	Generation             int64                `json:"generation"`
	Deadline               time.Time            `json:"deadline"`
	Limits                 store.RoundAllowance `json:"limits"`
	Used                   store.RoundAllowance `json:"used"`
	Step                   string               `json:"step"`
	Cursor                 json.RawMessage      `json:"cursor"`
	Unresolved             json.RawMessage      `json:"unresolved"`
	Report                 json.RawMessage      `json:"report"`
	StopReason             string               `json:"stopReason"`
	DeliverableStatus      string               `json:"deliverableStatus"`
	ReconciliationRequired bool                 `json:"reconciliationRequired"`
	CreatedAt              string               `json:"createdAt"`
	UpdatedAt              string               `json:"updatedAt"`
	CompletedAt            string               `json:"completedAt,omitempty"`
}

func roundModel(r store.Round) roundResponse {
	return roundResponse{ID: r.ID, Intent: r.Intent, Outcome: r.Outcome, ProfileVersion: r.ProfileVersion,
		Scope: r.Scope, State: r.State, Revision: r.Revision, Generation: r.Generation,
		Deadline: r.Deadline, Limits: r.Limits, Used: r.Used, Step: r.Step, Cursor: r.Cursor,
		Unresolved: r.Unresolved, Report: r.Report, StopReason: r.StopReason,
		DeliverableStatus: r.DeliverableStatus, ReconciliationRequired: r.ReconciliationRequired,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CompletedAt: r.CompletedAt}
}

func (h *Handler) roundCapability(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	input, err := h.defaultRoundInput(r.Context(), "")
	if err != nil {
		failRound(w, err)
		return
	}
	canStart := h.rounds != nil && h.rounds.Readiness != nil && h.rounds.Worker != nil &&
		h.rounds.Readiness.CheckRound(r.Context(), input.Outcome) == nil
	reason := ""
	if !canStart {
		reason = "round_executor_unavailable"
	}
	if _, err := h.database.ActiveRound(r.Context()); err == nil {
		canStart = false
		reason = "round_active"
	} else if !errors.Is(err, store.ErrNotFound) {
		failRound(w, err)
		return
	}
	sources := 0
	for _, resource := range input.Scope.Resources {
		if strings.HasPrefix(resource, "board:") {
			sources++
		}
	}
	writeJSON(w, http.StatusOK, struct {
		CanStart    bool                 `json:"canStart"`
		Reason      string               `json:"reason"`
		Intent      string               `json:"intent"`
		Outcome     string               `json:"outcome"`
		Limits      store.RoundAllowance `json:"limits"`
		SourceCount int                  `json:"sourceCount"`
	}{canStart, reason, input.Intent, input.Outcome, input.Limits, sources})
}

func failRound(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rounds.ErrNotReady):
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Round execution is unavailable.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid round request.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Round or resource not found.")
	case errors.Is(err, store.ErrAllowance):
		fail(w, http.StatusTooManyRequests, generated.ApiErrorCodeRateLimited, "Round allowance exhausted.")
	case errors.Is(err, store.ErrFenced):
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Round authority is no longer active or the operation is outside its scope.")
	case errors.Is(err, store.ErrActiveRound), errors.Is(err, store.ErrRoundIdempotencyConflict),
		errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrExpired), errors.Is(err, store.ErrUncertain):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Round state changed; refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Round request failed.")
	}
}

func (h *Handler) activeRound(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	round, err := h.database.ActiveRound(r.Context())
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roundModel(round))
}

func (h *Handler) getRound(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	round, err := h.database.Round(r.Context(), r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roundModel(round))
}

func (h *Handler) roundResults(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	results, err := h.database.RoundResults(r.Context(), r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	if results == nil {
		results = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, struct {
		Items []json.RawMessage `json:"items"`
	}{results})
}

func (h *Handler) startRound(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body roundStartRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	if body.RequestKey == "" || len(body.RequestKey) > 200 || strings.TrimSpace(body.RequestKey) != body.RequestKey {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "A bounded requestKey is required.")
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	previous, err := h.database.RoundByRequest(r.Context(), actor, body.RequestKey)
	if err == nil {
		writeJSON(w, http.StatusOK, roundModel(previous))
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		failRound(w, err)
		return
	}
	input, err := h.defaultRoundInput(r.Context(), body.RequestKey)
	if err != nil {
		failRound(w, err)
		return
	}
	round, created, err := h.rounds.Start(r.Context(), actor, input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, roundModel(round))
}

func (h *Handler) stopRound(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	round, err := h.rounds.Stop(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roundModel(round))
}

func (h *Handler) resumeRound(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	round, err := h.rounds.Resume(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roundModel(round))
}

func (h *Handler) roundMutation(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "opportunities:write", true)
	if !ok {
		return
	}
	if p.IsOwner() {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Delegated Codex agent required for record changes.")
		return
	}
	var body store.RoundMutationInput
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	if body.Operation == store.RoundPrepareApplicationPack || body.ApplicationPack != nil {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Application packs require the commissioned preparation tool.")
		return
	}
	body.Capability = r.Header.Get("X-Round-Capability")
	if body.Capability == "" {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "A bound round capability is required.")
		return
	}
	result, created, err := h.rounds.ApplyMutation(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), body)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

// Recruitment writes enter through the commissioned round boundary. These
// former routes stay explicit so clients receive an honest unavailable state.
func (h *Handler) unsupportedRecruitmentMutation(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	if p.IsOwner() && !h.mutationAllowed(w, r, p) {
		return
	}
	fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable,
		"This recruitment change is unavailable outside a commissioned round.")
}
