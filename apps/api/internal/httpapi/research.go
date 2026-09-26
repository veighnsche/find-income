package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ResearchRunControl routes owner stop/resume for research runs to the run
// supervisor (T13 semantics: fence-first stop with journaled reason and
// checkpoint refresh; resume reconciles, rotates credentials/generation and
// continues on remaining allowance). *rounds.Supervisor satisfies it
// structurally; commissioning stays supervisor-direct through ResearchService.
// Run control reuses the existing rounds stop/resume routes; no separate
// research stop/resume endpoints exist by design.
type ResearchRunControl interface {
	Stop(ctx context.Context, actor store.Actor, runID, reason string) (rounds.StopOutput, error)
	Resume(ctx context.Context, actor store.Actor, runID string) (rounds.ResumeOutput, error)
}

// ResearchService is the agreed supervision contract behind the research
// endpoints (T06 §8). Contract tests run against doubles; T23 connects the
// real supervisor.
type ResearchService interface {
	CommissionResearch(ctx context.Context, in CommissionResearchInput) (CommissionResearchOutput, error)
	SteerResearch(ctx context.Context, in SteerResearchInput) (generated.SteeringMessage, error)
	ResearchRun(ctx context.Context, actor store.Actor, runID string) (generated.ResearchRunView, error)
	ResearchActivity(ctx context.Context, actor store.Actor, runID, cursor string, limit int) (generated.ResearchActivityPage, error)
	ResearchCapture(ctx context.Context, actor store.Actor, captureID string) (generated.ResearchCaptureView, error)
	ResearchIdentity(ctx context.Context, actor store.Actor, subjectKind, subjectID string) (generated.ResearchIdentityView, error)
	ResearchReport(ctx context.Context, actor store.Actor, runID string) (generated.ResearchReportView, error)
}

// CommissionResearchInput carries a validated commission request.
type CommissionResearchInput struct {
	Actor          store.Actor
	BriefText      string
	CorrectionsRef string
	Allowance      *generated.ResearchAllowance
	IdempotencyKey string
}

// CommissionResearchOutput marks exact replays: Created=false returns 200
// with the existing run instead of 201.
type CommissionResearchOutput struct {
	View    generated.ResearchRunView
	Created bool
}

// SteerResearchInput carries a validated steering message.
type SteerResearchInput struct {
	Actor          store.Actor
	RunID          string
	Body           string
	IdempotencyKey string
}

func validID(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value
}

func validAllowance(a generated.ResearchAllowance) bool {
	return a.TimeMs >= 1 && a.MaxActions >= 1 && a.MaxJev >= 0 && a.MaxTurns >= 1 &&
		a.MaxConcurrent >= 1 && a.MaxConcurrent <= 4
}

func failResearch(w http.ResponseWriter, err error) {
	var contractErr *researchcontract.Error
	if errors.As(err, &contractErr) {
		switch contractErr.Code {
		case researchcontract.OutcomeInvalid:
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		case researchcontract.OutcomeNotFound:
			fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Research run or resource not found.")
		case researchcontract.OutcomeForbidden:
			fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Research authority is no longer active or the operation is outside its scope.")
		case researchcontract.OutcomeBudgetExhausted, researchcontract.OutcomeRateLimited:
			fail(w, http.StatusTooManyRequests, generated.ApiErrorCodeRateLimited, "Research allowance exhausted.")
		case researchcontract.OutcomeConflict, researchcontract.OutcomeRevisionConflict,
			researchcontract.OutcomeStale, researchcontract.OutcomeClaimedElsewhere,
			researchcontract.OutcomeIdentityAmbiguous, researchcontract.OutcomeCaptureIncomplete,
			researchcontract.OutcomeUncertain, researchcontract.OutcomeStopped:
			fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Research state changed; refresh before retrying.")
		default:
			fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Research request failed.")
		}
		return
	}
	switch {
	case errors.Is(err, rounds.ErrNotReady), errors.Is(err, musewire.ErrContributorUnavailable):
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Research supervision is unavailable.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Research run or resource not found.")
	case errors.Is(err, store.ErrAllowance):
		fail(w, http.StatusTooManyRequests, generated.ApiErrorCodeRateLimited, "Research allowance exhausted.")
	case errors.Is(err, store.ErrFenced):
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Research authority is no longer active or the operation is outside its scope.")
	case errors.Is(err, store.ErrActiveRound), errors.Is(err, store.ErrRoundIdempotencyConflict),
		errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrExpired), errors.Is(err, store.ErrUncertain):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Research state changed; refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Research request failed.")
	}
}

func (h *Handler) researchService(w http.ResponseWriter) (ResearchService, bool) {
	if h.research == nil {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Research supervision is not connected yet.")
		return nil, false
	}
	return h.research, true
}

func (h *Handler) commissionResearchRun(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	var body generated.CommissionResearchRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	in := CommissionResearchInput{Actor: store.Actor{Kind: p.Kind, ID: p.ID}}
	if body.BriefText != nil {
		if len(*body.BriefText) > 20000 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
			return
		}
		in.BriefText = *body.BriefText
	}
	if body.CorrectionsRef != nil {
		if !validID(*body.CorrectionsRef, 128) {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
			return
		}
		in.CorrectionsRef = *body.CorrectionsRef
	}
	if body.Allowance != nil {
		if !validAllowance(*body.Allowance) {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
			return
		}
		in.Allowance = body.Allowance
	}
	if body.IdempotencyKey != nil {
		if !validID(*body.IdempotencyKey, 128) {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
			return
		}
		in.IdempotencyKey = *body.IdempotencyKey
	}
	out, err := svc.CommissionResearch(r.Context(), in)
	if err != nil {
		failResearch(w, err)
		return
	}
	if out.Created {
		writeJSON(w, http.StatusCreated, out.View)
		return
	}
	writeJSON(w, http.StatusOK, out.View)
}

func (h *Handler) getResearchRun(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(id, 128) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	view, err := svc.ResearchRun(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) steerResearchRun(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(id, 128) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	var body generated.SteerResearchRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Body) == "" || len(body.Body) > 20000 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	in := SteerResearchInput{Actor: store.Actor{Kind: p.Kind, ID: p.ID}, RunID: id, Body: body.Body}
	if body.IdempotencyKey != nil {
		if !validID(*body.IdempotencyKey, 128) {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
			return
		}
		in.IdempotencyKey = *body.IdempotencyKey
	}
	msg, err := svc.SteerResearch(r.Context(), in)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) listResearchActivity(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(id, 128) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
			return
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 512 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	page, err := svc.ResearchActivity(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id, cursor, limit)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) getResearchReport(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(id, 128) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	view, err := svc.ResearchReport(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) getResearchCapture(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validID(id, 128) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	view, err := svc.ResearchCapture(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, id)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) explainResearchIdentity(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("subjectKind")
	if kind != "employer" && kind != "vacancy" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	subjectID := r.URL.Query().Get("subjectId")
	if !validID(subjectID, 128) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	view, err := svc.ResearchIdentity(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, kind, subjectID)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
