package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
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

// RunHistoryOptions filters the server run-recovery list. Empty Outcome
// matches every outcome; empty States matches every run state; empty
// Cursor starts from the newest run.
type RunHistoryOptions struct {
	Outcome string
	States  []string
	Limit   int
	Cursor  string
}

// RunHistoryItem is one durable run for server-backed recovery: enough to
// restore deep links (`#/search?run=<id>`), offer the valid Stop/Resume/
// Find-more actions per state, and tell terminal runs apart. Reads make
// zero model calls.
type RunHistoryItem struct {
	RunID       string `json:"runId"`
	RequestKey  string `json:"requestKey"`
	Intent      string `json:"intent"`
	Outcome     string `json:"outcome"`
	State       string `json:"state"`
	StopReason  string `json:"stopReason"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	CompletedAt string `json:"completedAt,omitempty"`
}

// RunHistoryPage is one newest-first recovery page. NextCursor is empty on
// the last page; passing it back resumes strictly older runs.
type RunHistoryPage struct {
	Items      []RunHistoryItem `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// ResearchRecoveryService serves the C1/D3 server run-recovery reads:
// relevant-run lookup across states, latest terminal run, and history.
// It is optional: handlers type-assert the wired ResearchService and
// report honest unavailable when the implementation predates it, so older
// doubles keep compiling and serving their own surface.
type ResearchRecoveryService interface {
	ListResearchRuns(ctx context.Context, actor store.Actor, opts RunHistoryOptions) (RunHistoryPage, error)
	LatestTerminalResearchRun(ctx context.Context, actor store.Actor, outcome string) (RunHistoryItem, error)
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
	// C2/D1: the first explicit commission authors the reason catalog for
	// the current saved goal version; later commissions reuse it. This is
	// the only authoring point: passive reads never call it, and the
	// derivation is deterministic, so concurrent/retried commissions
	// converge on one accepted version per brief.
	brief, err := codexservice.CurrentOwnerBrief(r.Context(), h.database)
	if err != nil {
		failResearch(w, err)
		return
	}
	if _, err := h.database.EnsureReasonCatalog(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, brief.ProfileVersion); err != nil {
		failResearch(w, err)
		return
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

// recoveryService resolves the optional run-recovery surface behind the
// wired research service. Older doubles predate it and honestly report
// unavailable instead of failing to compile.
func (h *Handler) recoveryService(w http.ResponseWriter, svc ResearchService) (ResearchRecoveryService, bool) {
	rec, ok := svc.(ResearchRecoveryService)
	if !ok {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Run recovery reads are not connected yet.")
		return nil, false
	}
	return rec, true
}

// listResearchRuns serves the C1 relevant-run lookup (active, paused,
// stopped, failed, completed and empty runs) plus newest-first history
// for server-backed restoration. Pure read: it starts, resumes and
// commissions nothing. Route registration is I-owned (proposed:
// GET /api/v1/research/runs with outcome/state/limit/cursor).
func (h *Handler) listResearchRuns(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	rec, ok := h.recoveryService(w, svc)
	if !ok {
		return
	}
	query := r.URL.Query()
	opts := RunHistoryOptions{
		Outcome: query.Get("outcome"),
		States:  query["state"],
		Cursor:  query.Get("cursor"),
	}
	if len(opts.Outcome) > 100 || len(opts.Cursor) > 512 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	opts.Limit = 25
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
			return
		}
		opts.Limit = parsed
	}
	page, err := rec.ListResearchRuns(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, opts)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// getLatestTerminalResearchRun serves the newest completed/failed run for
// one outcome (or every outcome when omitted) so a fresh context restores
// the latest terminal work without guessing its id. Pure read. Route
// registration is I-owned (proposed:
// GET /api/v1/research/runs/latest-terminal with outcome).
func (h *Handler) getLatestTerminalResearchRun(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	svc, ok := h.researchService(w)
	if !ok {
		return
	}
	rec, ok := h.recoveryService(w, svc)
	if !ok {
		return
	}
	outcome := r.URL.Query().Get("outcome")
	if len(outcome) > 100 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid research request.")
		return
	}
	item, err := rec.LatestTerminalResearchRun(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, outcome)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
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
