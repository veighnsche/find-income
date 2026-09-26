package httpapi

import (
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Per-type artifact operations (A4 contract). The readiness set maps the
// verified route to required/held/not-required/unresolved states; exact
// edits persist literally without a model call. form_values is derived at
// read time and rejected on write.
func (h *Handler) draftOpportunityArtifacts(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.materials == nil {
		h.materialUnavailable(w, "Artifact drafting")
		return
	}
	var body generated.MaterialPrepareRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	set, created, err := h.materials.DraftOpportunityArtifacts(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), body.RequestKey,
		body.ExpectedCheckId, body.ExpectedQuestionSetSha256, body.ExpectedWorkflowRevision)
	if err != nil {
		failArtifactDraft(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, artifactReadinessSetModel(set))
}

func (h *Handler) listArtifactReadiness(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	set, err := h.database.ArtifactReadiness(r.Context(), r.PathValue("id"))
	if err != nil {
		failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactReadinessSetModel(set))
}

func (h *Handler) getArtifactReadiness(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	artifactType := r.PathValue("artifactType")
	set, err := h.database.ArtifactReadiness(r.Context(), r.PathValue("id"))
	if err != nil {
		failArtifact(w, err)
		return
	}
	for _, entry := range set.Entries {
		if entry.Type == artifactType {
			writeJSON(w, http.StatusOK, artifactReadinessEntryModel(entry))
			return
		}
	}
	fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Unknown artifact type.")
}

func (h *Handler) saveOpportunityArtifact(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.ArtifactSaveRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	view, created, err := h.database.SaveOpportunityArtifact(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), store.ArtifactSaveInput{
			RequestKey: body.RequestKey, ExpectedVersion: body.ExpectedVersion,
			Type: r.PathValue("artifactType"), Content: body.Content, Basis: parseArtifactBasis(body.Basis),
		})
	if err != nil {
		failArtifact(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, artifactViewModel(view))
}

func failArtifactDraft(w http.ResponseWriter, err error) {
	if errors.Is(err, materialprep.ErrUnavailable) {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Artifact drafting is unavailable.")
		return
	}
	failArtifact(w, err)
}

func failArtifact(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected), errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or artifact not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid artifact request.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Artifact state changed; refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Artifact request failed.")
	}
}

func parseArtifactBasis(basis *generated.ArtifactBasis) store.ArtifactBasis {
	out := store.ArtifactBasis{FactIDs: []string{}, AnswerRefs: []store.ArtifactAnswerRef{}, CheckSpans: []store.CheckSourceSpan{}}
	if basis == nil {
		return out
	}
	out.FactIDs = append(out.FactIDs, basis.FactIds...)
	for _, ref := range basis.AnswerRefs {
		out.AnswerRefs = append(out.AnswerRefs, store.ArtifactAnswerRef{QuestionID: ref.QuestionId, AnswerVersion: ref.AnswerVersion})
	}
	for _, span := range basis.CheckSpans {
		out.CheckSpans = append(out.CheckSpans, store.CheckSourceSpan{CaptureID: span.CaptureId, Start: span.Start, End: span.End})
	}
	return out
}

func artifactBasisModel(value store.ArtifactBasis) generated.ArtifactBasis {
	model := generated.ArtifactBasis{FactIds: []string{},
		AnswerRefs: []generated.ArtifactAnswerRef{}, CheckSpans: []generated.CheckSourceSpan{}}
	model.FactIds = append(model.FactIds, value.FactIDs...)
	for _, ref := range value.AnswerRefs {
		model.AnswerRefs = append(model.AnswerRefs, generated.ArtifactAnswerRef{QuestionId: ref.QuestionID, AnswerVersion: ref.AnswerVersion})
	}
	for _, span := range value.CheckSpans {
		model.CheckSpans = append(model.CheckSpans, generated.CheckSourceSpan{CaptureId: span.CaptureID, Start: span.Start, End: span.End})
	}
	return model
}

func artifactViewModel(value store.ArtifactView) generated.ArtifactView {
	model := generated.ArtifactView{Id: value.ID, OpportunityId: value.OpportunityID,
		Type: generated.ArtifactViewType(value.Type), Version: value.Version,
		Content: value.Content, Basis: artifactBasisModel(value.Basis), CreatedAt: recordedTime(value.CreatedAt)}
	model.CreatedBy.ActorKind = value.CreatedBy.Kind
	model.CreatedBy.ActorId = value.CreatedBy.ID
	return model
}

func artifactReadinessEntryModel(value store.ArtifactReadinessEntry) generated.ArtifactReadinessEntry {
	model := generated.ArtifactReadinessEntry{Type: generated.ArtifactReadinessEntryType(value.Type),
		Required: value.Required, State: generated.ArtifactReadinessEntryState(value.State), Reason: value.Reason}
	if value.Basis != "" {
		basis := value.Basis
		model.Basis = &basis
	}
	if value.Current != nil {
		current := artifactViewModel(*value.Current)
		model.Current = &current
	}
	if value.Type == store.ArtifactFormValues && value.FormValues != nil {
		values := make([]generated.ArtifactFormValue, 0, len(value.FormValues))
		for _, item := range value.FormValues {
			values = append(values, generated.ArtifactFormValue{QuestionId: item.QuestionID,
				QuestionText: item.QuestionText, Required: generated.ArtifactFormValueRequired(item.Required),
				Kind: item.Kind, State: item.State, Text: item.Text})
		}
		model.FormValues = &values
	}
	return model
}

func artifactReadinessSetModel(value store.ArtifactReadinessSet) generated.ArtifactReadinessSet {
	model := generated.ArtifactReadinessSet{OpportunityId: value.OpportunityID,
		CheckStatus: value.CheckStatus, Entries: []generated.ArtifactReadinessEntry{}}
	if value.CheckID != "" {
		checkID := value.CheckID
		model.CheckId = &checkID
	}
	for _, entry := range value.Entries {
		model.Entries = append(model.Entries, artifactReadinessEntryModel(entry))
	}
	return model
}
