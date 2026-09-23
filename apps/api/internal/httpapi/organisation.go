package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func categorySetModel(value store.OrganisationCategorySet) generated.OrganisationCategorySet {
	categories := make([]generated.OrganisationCategory, 0, len(value.Categories))
	for _, category := range value.Categories {
		categories = append(categories, generated.OrganisationCategory{Id: category.ID, Description: category.Description})
	}
	return generated.OrganisationCategorySet{Version: value.Version, Categories: categories, CreatedAt: optionalTime(value.CreatedAt)}
}

func organisationAssessmentModel(value store.OrganisationAssessment) (generated.OrganisationAssessment, error) {
	var input jev.OrganisationInput
	var result jev.OrganisationResult
	if err := json.Unmarshal(value.InputJSON, &input); err != nil {
		return generated.OrganisationAssessment{}, err
	}
	if err := json.Unmarshal(value.ResultJSON, &result); err != nil {
		return generated.OrganisationAssessment{}, err
	}
	facts := make([]generated.OrganisationSourceFact, 0, len(input.Facts))
	for _, fact := range input.Facts {
		facts = append(facts, generated.OrganisationSourceFact{
			Id: fact.ID, SourceId: fact.SourceID, SourceRevision: fact.SourceRevision,
			SourceKind: fact.SourceKind, ObservedAt: nonemptyString(fact.ObservedAt), Excerpt: fact.Excerpt,
		})
	}
	refs := make([]generated.OrganisationSourceRef, 0, len(result.SourceRefs))
	for _, ref := range result.SourceRefs {
		refs = append(refs, generated.OrganisationSourceRef{
			FactId: ref.FactID, SourceId: ref.SourceID, SourceRevision: ref.SourceRevision, SourceKind: ref.SourceKind,
		})
	}
	var description string
	for _, category := range input.Categories {
		if category.ID == value.CategoryID {
			description = category.Description
			break
		}
	}
	return generated.OrganisationAssessment{
		Id: value.ID, CategorySetVersion: value.CategoryVersion,
		Disposition: generated.OrganisationAssessmentDisposition(value.Disposition),
		CategoryId:  nonemptyString(value.CategoryID), CategoryDescription: nonemptyString(description),
		SourceFacts: facts, SourceRefs: refs,
		RequestedModel: value.RequestedModel, ReturnedModel: value.ReturnedModel,
		CreatedAt: recordedTime(value.CreatedAt),
	}, nil
}

func (h *Handler) runtimeStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	ingestionAvailable := h.ingestionAvailable
	if h.codex != nil {
		ingestionAvailable = h.codex.Status(r.Context()).IngestionAvailable
	}
	writeJSON(w, http.StatusOK, generated.RuntimeStatus{
		IngestionAvailable: ingestionAvailable, OrganisationAvailable: h.organisationAvailable,
		CollectionAvailable: h.collectionAvailable,
	})
}

func (h *Handler) organisationCategories(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	set, err := h.database.CurrentOrganisationCategories(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read organisation categories.")
		return
	}
	writeJSON(w, http.StatusOK, categorySetModel(set))
}

func (h *Handler) updateOrganisationCategories(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, principal) {
		return
	}
	var request struct {
		ExpectedVersion *int64                            `json:"expectedVersion"`
		Categories      *[]generated.OrganisationCategory `json:"categories"`
	}
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	if request.ExpectedVersion == nil || request.Categories == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Expected version and categories are required.")
		return
	}
	definitions := make([]store.OrganisationCategory, 0, len(*request.Categories))
	for _, category := range *request.Categories {
		definitions = append(definitions, store.OrganisationCategory{ID: category.Id, Description: category.Description})
	}
	set, err := h.database.UpdateOrganisationCategories(r.Context(), *request.ExpectedVersion, definitions, principal.Actor())
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Check the category IDs and descriptions.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Categories changed; reload before saving.")
	case err != nil:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not save organisation categories.")
	default:
		writeJSON(w, http.StatusOK, categorySetModel(set))
	}
}

func (h *Handler) opportunityOrganisation(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := h.database.Opportunity(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Opportunity not found.")
		} else {
			fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read opportunity.")
		}
		return
	}
	view, err := h.database.Organisation(r.Context(), id)
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read organisation.")
		return
	}
	model := generated.OrganisationView{Status: generated.OrganisationViewStatus(view.Status), JobId: nonemptyString(view.JobID)}
	if view.Current != nil {
		value, err := organisationAssessmentModel(*view.Current)
		if err != nil {
			fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read organisation result.")
			return
		}
		model.Current = &value
	}
	if view.LatestHistorical != nil {
		value, err := organisationAssessmentModel(*view.LatestHistorical)
		if err != nil {
			fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read organisation history.")
			return
		}
		model.LatestHistorical = &value
	}
	writeJSON(w, http.StatusOK, model)
}

func (h *Handler) organisationSummaries(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if !rejectUnknownQuery(w, r, "ids") {
		return
	}
	raw := r.URL.Query().Get("ids")
	ids := strings.Split(raw, ",")
	if raw == "" || len(ids) > 100 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Provide one to 100 opportunity IDs.")
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id || seen[id] {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Opportunity IDs must be unique and nonempty.")
			return
		}
		seen[id] = true
	}
	summaries, err := h.database.OrganisationSummaries(r.Context(), ids)
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read organisation summaries.")
		return
	}
	items := make([]generated.OrganisationSummary, 0, len(summaries))
	for _, id := range ids {
		value, ok := summaries[id]
		if !ok {
			continue
		}
		items = append(items, generated.OrganisationSummary{
			OpportunityId: id, Status: generated.OrganisationSummaryStatus(value.Status),
			CategoryId: nonemptyString(value.CategoryID),
		})
	}
	writeJSON(w, http.StatusOK, generated.OrganisationSummaryList{Items: items})
}
