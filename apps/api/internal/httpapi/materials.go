package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Material/version operations (A6 contract). Grounded preparation and reads
// are live (D3); direct edits and explicit rewrites land with D4. There is
// no delivery step: new material versions produce new pack identities, and
// the owner applies manually from the saved Handoff page.
func (h *Handler) materialUnavailable(w http.ResponseWriter, operation string) {
	fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable,
		operation+" is unavailable until preparation is implemented.")
}

// MaterialPreparer runs grounded preparation: pin check/answers, draft
// required+unset items from verified facts only (bounded Codex turn, skipped
// when nothing needs drafting), validate citations, run Jev relevance,
// Typst-render, and commit via store.PrepareOpportunityMaterials. It performs
// no research, capture, or send. Implemented by materialprep.Service; nil
// until the server wires it.
type MaterialPreparer interface {
	PrepareOpportunityMaterials(ctx context.Context, actor store.Actor, opportunityID, requestKey,
		expectedCheckID, expectedQuestionSetSHA256 string, expectedWorkflowRevision int64) (store.MaterialStatusView, bool, error)
	// EditOpportunityMaterials saves one exact owner edit as a new
	// version. No LLM call.
	EditOpportunityMaterials(ctx context.Context, actor store.Actor, opportunityID, requestKey string,
		expectedVersion int64, text string) (store.MaterialVersionView, bool, error)
	// RewriteOpportunityMaterials runs one explicit one-shot rewrite turn
	// and commits it as a new version. Sole rewrite entry point: never
	// called implicitly.
	RewriteOpportunityMaterials(ctx context.Context, actor store.Actor, opportunityID, requestKey string,
		expectedVersion int64, instruction string) (store.MaterialVersionView, bool, error)
	// DraftOpportunityArtifacts drafts required held artifact types in
	// one bounded Standard turn and returns the stored readiness set.
	DraftOpportunityArtifacts(ctx context.Context, actor store.Actor, opportunityID, requestKey string,
		expectedCheckID, expectedQuestionSetSHA256 string, expectedWorkflowRevision int64) (store.ArtifactReadinessSet, bool, error)
}

func (h *Handler) prepareOpportunityMaterials(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.materials == nil {
		h.materialUnavailable(w, "Material preparation")
		return
	}
	var body generated.MaterialPrepareRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	view, created, err := h.materials.PrepareOpportunityMaterials(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), body.RequestKey,
		body.ExpectedCheckId, body.ExpectedQuestionSetSha256, body.ExpectedWorkflowRevision)
	if err != nil {
		failMaterial(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, materialStatusModel(view))
}

func (h *Handler) getCurrentOpportunityMaterials(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	view, err := h.database.CurrentOpportunityMaterials(r.Context(), r.PathValue("id"))
	if err != nil {
		failMaterial(w, err)
		return
	}
	writeJSON(w, http.StatusOK, materialStatusModel(view))
}

func (h *Handler) editOpportunityMaterials(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.materials == nil {
		h.materialUnavailable(w, "Material edit")
		return
	}
	var body generated.MaterialEditRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	view, created, err := h.materials.EditOpportunityMaterials(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), body.RequestKey,
		body.ExpectedVersion, body.Text)
	if err != nil {
		failMaterial(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, materialVersionModel(view))
}

func (h *Handler) rewriteOpportunityMaterials(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.materials == nil {
		h.materialUnavailable(w, "Material rewrite")
		return
	}
	var body generated.MaterialRewriteRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	instruction := ""
	if body.Instruction != nil {
		instruction = *body.Instruction
	}
	view, created, err := h.materials.RewriteOpportunityMaterials(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), body.RequestKey,
		body.ExpectedVersion, instruction)
	if err != nil {
		failMaterial(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, materialVersionModel(view))
}

func (h *Handler) getOpportunityMaterialVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 1 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Material version must be a positive integer.")
		return
	}
	view, err := h.database.OpportunityMaterialVersion(r.Context(), r.PathValue("id"), version)
	if err != nil {
		failMaterial(w, err)
		return
	}
	writeJSON(w, http.StatusOK, materialVersionModel(view))
}

func failMaterial(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, materialprep.ErrUnavailable):
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Material preparation is unavailable.")
	case errors.Is(err, store.ErrRoleNotSelected), errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or material version not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid material request.")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrRoundIdempotencyConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Material state changed; refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Material request failed.")
	}
}

func materialVersionModel(value store.MaterialVersionView) generated.MaterialVersion {
	answers := make([]generated.MaterialAnswerRef, 0, len(value.Answers))
	for _, ref := range value.Answers {
		answers = append(answers, generated.MaterialAnswerRef{QuestionId: ref.QuestionID,
			QuestionTextSha256: ref.QuestionTextSHA256, AnswerVersion: ref.AnswerVersion, TextSha256: ref.TextSHA256})
	}
	model := generated.MaterialVersion{PackId: value.PackID, Version: value.Version,
		OpportunityId: value.OpportunityID, OpportunityRevision: value.OpportunityRevision,
		ProfileRevision: value.ProfileRevision, CheckId: value.CheckID,
		QuestionSetSha256: value.QuestionSetSHA256, Answers: answers,
		Readiness: generated.MaterialReadiness{Ready: value.Readiness.Ready,
			MissingRequired: value.Readiness.MissingRequired, Held: value.Readiness.Held},
		CreatedAt: recordedTime(value.CreatedAt)}
	model.CreatedBy.ActorKind = value.CreatedBy.Kind
	model.CreatedBy.ActorId = value.CreatedBy.ID
	model.Provenance.Origin = generated.MaterialVersionProvenanceOrigin(value.Provenance.Origin)
	model.Provenance.SourceShas = value.Provenance.SourceShas
	if value.Provenance.RewriteOf != nil {
		rewriteOf := *value.Provenance.RewriteOf
		model.Provenance.RewriteOf = &rewriteOf
	}
	return model
}

func materialStatusModel(value store.MaterialStatusView) generated.MaterialStatusView {
	model := generated.MaterialStatusView{Status: generated.MaterialStatusViewStatus(value.Status)}
	if value.Current != nil {
		current := materialVersionModel(*value.Current)
		model.Current = &current
	}
	return model
}
