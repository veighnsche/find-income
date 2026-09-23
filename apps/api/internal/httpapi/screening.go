package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Screening is a reviewable Jev proposal. Its support is not an evidence
// claim or a confirmed qualification.
func (h *Handler) opportunityScreening(w http.ResponseWriter, r *http.Request) {
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
	assessment, err := h.database.CurrentRoundJevAssessment(r.Context(), id, "screening")
	status := "not_assessed"
	if errors.Is(err, store.ErrConflict) {
		status = "outdated"
	} else if err == nil {
		status = assessment.Status
	} else if !errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read screening.")
		return
	}
	var current any
	if err == nil {
		current = struct {
			ID                  string          `json:"id"`
			OpportunityRevision int64           `json:"opportunityRevision"`
			ProfileVersion      int64           `json:"profileVersion"`
			SourceID            string          `json:"sourceId"`
			SourceRevision      string          `json:"sourceRevision"`
			OmittedBytes        int             `json:"omittedBytes"`
			Input               json.RawMessage `json:"input"`
			Result              json.RawMessage `json:"result"`
			CreatedAt           string          `json:"createdAt"`
		}{assessment.ID, assessment.OpportunityRevision, assessment.ProfileVersion, assessment.SourceID, assessment.SourceRevision, assessment.OmittedBytes, assessment.InputJSON, assessment.ResultJSON, assessment.CreatedAt}
	}
	writeJSON(w, http.StatusOK, struct {
		Status    string `json:"status"`
		Confirmed bool   `json:"confirmed"`
		Current   any    `json:"current"`
	}{status, false, current})
}
