package httpapi

import (
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type createOfferOptionSetRequest struct {
	SourceID                string   `json:"sourceId"`
	ExpectedContextVersion  *int64   `json:"expectedContextVersion"`
	ExpectedEvidenceVersion *int64   `json:"expectedEvidenceVersion"`
	SpanStart               *int     `json:"spanStart"`
	SpanEnd                 *int     `json:"spanEnd"`
	Labels                  []string `json:"labels"`
	SupersedesID            string   `json:"supersedesId"`
}

func offerOptionSetModel(value store.OfferOptionSet) map[string]any {
	options := make([]map[string]string, 0, len(value.Options))
	for _, item := range value.Options {
		options = append(options, map[string]string{"id": item.ID, "label": item.Label})
	}
	model := map[string]any{"id": value.ID, "opportunityId": value.OpportunityID,
		"sourceId": value.SourceID, "contextVersion": value.ContextVersion,
		"spanStart": value.SpanStart, "spanEnd": value.SpanEnd,
		"sourceExcerpt": value.SourceExcerpt, "excerptSha256": value.ExcerptSHA256,
		"createdAt": value.CreatedAt, "actorKind": value.Actor.Kind,
		"actorId": value.Actor.ID, "options": options}
	if value.SupersedesID != "" {
		model["supersedesId"] = value.SupersedesID
	}
	return model
}

func (h *Handler) listOfferOptionSets(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	if !rejectUnknownQuery(w, r) {
		return
	}
	sets, err := h.database.CurrentOfferOptionSets(r.Context(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "list offer options")
		return
	}
	items := make([]map[string]any, 0, len(sets))
	for _, set := range sets {
		items = append(items, offerOptionSetModel(set))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createOfferOptionSet(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "evidence:write", true)
	if !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	var request createOfferOptionSetRequest
	if !decodeEvidenceJSON(w, r, evidenceClaimBodyLimit, &request) {
		return
	}
	if request.SourceID == "" || request.ExpectedContextVersion == nil ||
		request.ExpectedEvidenceVersion == nil || request.SpanStart == nil || request.SpanEnd == nil ||
		len(request.Labels) < 2 || len(request.Labels) > 8 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "A current source, exact quote, versions and two to eight option labels are required.")
		return
	}
	if _, ok := h.sourceInOpportunity(w, r, request.SourceID); !ok {
		return
	}
	input := store.OfferOptionSetInput{OpportunityID: r.PathValue("id"), SourceID: request.SourceID,
		ExpectedContextVersion:  *request.ExpectedContextVersion,
		ExpectedEvidenceVersion: *request.ExpectedEvidenceVersion,
		SpanStart:               *request.SpanStart, SpanEnd: *request.SpanEnd,
		Labels: request.Labels, SupersedesID: request.SupersedesID}
	set, changeID, err := h.database.CreateOfferOptionSet(r.Context(), p.Actor(), input)
	if err != nil {
		h.evidenceMutationError(w, r, err)
		return
	}
	versions, err := h.database.QualificationInputVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read current input versions.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"set": offerOptionSetModel(set),
		"changeId": changeID, "currentInputVersions": versionsModel(versions)})
}
