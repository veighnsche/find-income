package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

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

func (h *Handler) listRounds(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	query := r.URL.Query()
	opts := store.ListRoundsOptions{
		Outcome: query.Get("outcome"),
		States:  query["state"],
		Cursor:  query.Get("cursor"),
		Limit:   25,
	}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid rounds request.")
			return
		}
		opts.Limit = parsed
	}
	rounds, nextCursor, err := h.database.ListRounds(r.Context(), opts)
	if err != nil {
		failRound(w, err)
		return
	}
	items := make([]roundResponse, 0, len(rounds))
	for _, round := range rounds {
		items = append(items, roundModel(round))
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []roundResponse `json:"items"`
		NextCursor string          `json:"nextCursor,omitempty"`
	}{items, nextCursor})
}

// failRunNotFound reports the C1 unknown-run shape: 404 with the
// run_not_found code so fresh contexts tell a bad id from a missing
// record. Other errors keep the shared round mapping.
func failRunNotFound(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeRunNotFound, "Research run not found.")
		return
	}
	failRound(w, err)
}

func (h *Handler) getRound(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	round, err := h.database.Round(r.Context(), r.PathValue("id"))
	if err != nil {
		failRunNotFound(w, err)
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
		failRunNotFound(w, err)
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
	round, err := h.database.Round(r.Context(), id)
	if err != nil {
		failResearch(w, err)
		return
	}
	if runRef, isMuse := musewire.CommissionedRunRef(round); isMuse {
		// Discovery resume is two-phase: the run control revives the
		// round, then the stopped Contributor session re-conducts from
		// its durable cursor in the background.
		if _, err := h.researchControl.Resume(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id); err != nil {
			failResearch(w, err)
			return
		}
		if h.muse == nil {
			fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Discovery is not connected yet.")
			return
		}
		if _, err := h.muse.ResumeDiscoveryAsync(r.Context(), runRef); err != nil {
			failResearch(w, err)
			return
		}
		round, err := h.database.Round(r.Context(), id)
		if err != nil {
			failResearch(w, err)
			return
		}
		writeJSON(w, http.StatusOK, roundModel(round))
		return
	}
	if _, err := h.researchControl.Resume(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id); err != nil {
		failResearch(w, err)
		return
	}
	round, err = h.database.Round(r.Context(), id)
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
