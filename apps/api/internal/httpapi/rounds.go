package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type prepareRoundRequest struct {
	RequestKey    string                        `json:"requestKey"`
	OpportunityID string                        `json:"opportunityId"`
	ReplacePaused *generated.ReplacePausedRound `json:"replacePaused,omitempty"`
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
	if body.RequestKey == "" || len(body.RequestKey) > 200 || strings.TrimSpace(body.RequestKey) != body.RequestKey || body.OpportunityID == "" || len(body.OpportunityID) > 128 || strings.TrimSpace(body.OpportunityID) != body.OpportunityID || body.ReplacePaused != nil && (body.ReplacePaused.RoundId == "" || body.ReplacePaused.ExpectedRevision < 1) {
		failRound(w, store.ErrInvalid)
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	previous, err := h.database.RoundByRequest(r.Context(), actor, body.RequestKey)
	if err == nil {
		if previous.Outcome != "prepare" || len(previous.Scope.Resources) != 2 || previous.Scope.Resources[0] != "opportunity:"+body.OpportunityID || previous.Scope.Resources[1] != "campaign:active" || !replacementMatches(previous.Scope.InputRefs, body.ReplacePaused) {
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
		Scope:  store.RoundScope{InputRefs: []string{"profile:current", "opportunity:" + opportunity.ID}, Resources: []string{"opportunity:" + opportunity.ID, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundJevRequest, store.RoundPrepareApplicationPack, store.RoundContextTool}, Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 9, Items: 1, Tools: 3, Turns: 1}, Deadline: time.Now().Add(30 * time.Minute).UTC()}
	if body.ReplacePaused != nil {
		input.Scope.InputRefs = append(input.Scope.InputRefs, replacementRef(body.ReplacePaused.RoundId, body.ReplacePaused.ExpectedRevision))
	}
	var round store.Round
	var created bool
	if body.ReplacePaused == nil {
		round, created, err = h.rounds.Start(r.Context(), actor, input)
	} else {
		round, created, err = h.rounds.ReplacePaused(r.Context(), actor, body.ReplacePaused.RoundId, body.ReplacePaused.ExpectedRevision, input)
	}
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
	RequestKey              string               `json:"requestKey"`
	ID                      string               `json:"id"`
	Intent                  string               `json:"intent"`
	Outcome                 string               `json:"outcome"`
	OriginalProfileVersion  int64                `json:"originalProfileVersion"`
	EffectiveProfileVersion int64                `json:"effectiveProfileVersion"`
	ProfileVersion          int64                `json:"profileVersion"`
	Scope                   store.RoundScope     `json:"scope"`
	State                   store.RoundState     `json:"state"`
	Revision                int64                `json:"revision"`
	Generation              int64                `json:"generation"`
	Deadline                time.Time            `json:"deadline"`
	Limits                  store.RoundAllowance `json:"limits"`
	Used                    store.RoundAllowance `json:"used"`
	Step                    string               `json:"step"`
	Cursor                  json.RawMessage      `json:"cursor"`
	Unresolved              json.RawMessage      `json:"unresolved"`
	Report                  json.RawMessage      `json:"report"`
	StopReason              string               `json:"stopReason"`
	DeliverableStatus       string               `json:"deliverableStatus"`
	ReconciliationRequired  bool                 `json:"reconciliationRequired"`
	CreatedAt               string               `json:"createdAt"`
	UpdatedAt               string               `json:"updatedAt"`
	CompletedAt             string               `json:"completedAt,omitempty"`
}

func roundModel(r store.Round) roundResponse {
	return roundResponse{ID: r.ID, RequestKey: r.RequestKey, Intent: r.Intent, Outcome: r.Outcome, OriginalProfileVersion: r.InitialProfileVersion, EffectiveProfileVersion: r.ProfileVersion, ProfileVersion: r.ProfileVersion,
		Scope: r.Scope, State: r.State, Revision: r.Revision, Generation: r.Generation,
		Deadline: r.Deadline, Limits: r.Limits, Used: r.Used, Step: r.Step, Cursor: r.Cursor,
		Unresolved: r.Unresolved, Report: r.Report, StopReason: r.StopReason,
		DeliverableStatus: r.DeliverableStatus, ReconciliationRequired: r.ReconciliationRequired,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CompletedAt: r.CompletedAt}
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
	writeJSON(w, http.StatusOK, h.roundWithRecommendationCurrentness(r.Context(), round))
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

func (h *Handler) stopAnyRound(w http.ResponseWriter, r *http.Request) {
	round, err := h.database.Round(r.Context(), r.PathValue("id"))
	if err == nil && round.Outcome == "research_run" {
		h.stopResearchRun(w, r, round.ID)
		return
	}
	h.stopRound(w, r)
}

func (h *Handler) resumeAnyRound(w http.ResponseWriter, r *http.Request) {
	round, err := h.database.Round(r.Context(), r.PathValue("id"))
	if err == nil && round.Outcome == "research_run" {
		h.resumeResearchRun(w, r, round.ID)
		return
	}
	h.resumeRound(w, r)
}

// stopResearchRun and resumeResearchRun route research runs to the run
// supervisor so browser Stop/Resume share the tested fence, journal,
// checkpoint and remaining-allowance behavior. The shared rounds service
// cannot resume research runs (its worker launches legacy outcomes only),
// so research must never fall through to it.
func (h *Handler) stopResearchRun(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.researchControl == nil {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Research supervision is not connected yet.")
		return
	}
	// Resolve the muse run before fencing: stopping the round must also
	// fence the live Contributor session behind a discovery run.
	round, err := h.database.Round(r.Context(), id)
	if err != nil {
		failResearch(w, err)
		return
	}
	runRef, isMuse := musewire.CommissionedRunRef(round)
	if _, err := h.researchControl.Stop(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id, "owner stop"); err != nil {
		failResearch(w, err)
		return
	}
	if isMuse && h.muse != nil {
		h.muse.Stop(runRef, "owner stop")
	}
	round, err = h.database.Round(r.Context(), id)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roundModel(round))
}

func (h *Handler) resumeResearchRun(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.researchControl == nil {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Research supervision is not connected yet.")
		return
	}
	if round, err := h.database.Round(r.Context(), id); err == nil {
		if _, isMuse := musewire.CommissionedRunRef(round); isMuse {
			failResearch(w, researchcontract.NewError(researchcontract.OutcomeConflict,
				"run", "muse discovery runs cannot resume after stop; commission a new run"))
			return
		}
	}
	if _, err := h.researchControl.Resume(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id); err != nil {
		failResearch(w, err)
		return
	}
	round, err := h.database.Round(r.Context(), id)
	if err != nil {
		failResearch(w, err)
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
