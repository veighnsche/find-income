package httpapi

import (
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/agency"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Owner clarification endpoints (K4): preparation's focused questions
// for genuinely unknown personal facts, answered verbatim by the owner.

func (h *Handler) listClarifications(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	items, err := h.database.ListOwnerClarifications(r.Context(), r.PathValue("id"))
	if err != nil {
		failClarification(w, err)
		return
	}
	models := make([]generated.Clarification, 0, len(items))
	for _, item := range items {
		models = append(models, clarificationModel(item))
	}
	writeJSON(w, http.StatusOK, struct {
		Items []generated.Clarification `json:"items"`
	}{models})
}

func (h *Handler) openClarification(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.ClarificationOpen
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	refs := make([]agency.ClarificationWorkRef, 0, len(body.AffectedWork))
	for _, ref := range body.AffectedWork {
		refs = append(refs, agency.ClarificationWorkRef{Kind: ref.Kind, ID: ref.Id})
	}
	item, created, err := agency.OpenClarification(r.Context(),
		&agency.StoreClarifications{DB: h.database},
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"),
		agency.ClarificationOpenInput{RequestKey: body.RequestKey, CheckID: body.CheckId,
			Requirement: agency.ClarificationRequirement{Statement: body.Requirement.Statement,
				CaptureID: body.Requirement.CaptureId,
				SpanStart: body.Requirement.SpanStart, SpanEnd: body.Requirement.SpanEnd},
			Prompt: body.Prompt, AffectedWork: refs})
	if err != nil {
		failClarification(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	value, err := h.database.GetOwnerClarification(r.Context(), item.ID)
	if err != nil {
		failClarification(w, err)
		return
	}
	writeJSON(w, status, clarificationModel(value))
}

func (h *Handler) answerClarification(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.ClarificationAnswer
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	item, err := agency.AnswerClarification(r.Context(),
		&agency.StoreClarifications{DB: h.database},
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("clarificationId"),
		agency.ClarificationAnswer{RequestKey: body.RequestKey, Text: body.Text})
	if err != nil {
		failClarification(w, err)
		return
	}
	if item.OpportunityID != r.PathValue("id") {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Clarification not found on this job.")
		return
	}
	value, err := h.database.GetOwnerClarification(r.Context(), item.ID)
	if err != nil {
		failClarification(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clarificationModel(value))
}

func failClarification(w http.ResponseWriter, err error) {
	var clarificationErr *agency.ClarificationError
	if errors.As(err, &clarificationErr) {
		switch clarificationErr.Reason {
		case agency.ClarifyAlreadyAnswered:
			fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "This clarification is already answered.")
			return
		default:
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid clarification request.")
			return
		}
	}
	switch {
	case errors.Is(err, store.ErrRoleNotSelected), errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or clarification not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid clarification request.")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrRoundIdempotencyConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "The clarification changed. Refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Clarification request failed.")
	}
}

func clarificationModel(value store.Clarification) generated.Clarification {
	model := generated.Clarification{Id: value.ID, OpportunityId: value.OpportunityID,
		CheckId: value.CheckID, Origin: generated.ClarificationOrigin(value.Origin),
		Requirement: generated.ClarificationRequirement{Statement: value.Requirement.Statement,
			CaptureId: value.Requirement.CaptureID, SpanStart: value.Requirement.SpanStart,
			SpanEnd: value.Requirement.SpanEnd},
		Prompt: value.Prompt, Status: generated.ClarificationStatus(value.Status),
		CreatedAt: recordedTime(value.CreatedAt)}
	model.AffectedWork = make([]generated.ClarificationWorkRef, 0, len(value.AffectedWork))
	for _, ref := range value.AffectedWork {
		model.AffectedWork = append(model.AffectedWork,
			generated.ClarificationWorkRef{Kind: ref.Kind, Id: ref.ID})
	}
	if value.Answer != "" {
		model.Answer = &value.Answer
	}
	if value.AnsweredAt != "" {
		at := recordedTime(value.AnsweredAt)
		model.AnsweredAt = &at
	}
	if value.AnsweredBy.Kind != "" || value.AnsweredBy.ID != "" {
		model.AnsweredBy = &struct {
			ActorId   string `json:"actorId"`
			ActorKind string `json:"actorKind"`
		}{ActorId: value.AnsweredBy.ID, ActorKind: value.AnsweredBy.Kind}
	}
	return model
}
