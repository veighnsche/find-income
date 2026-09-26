package httpapi

import (
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// saveOpportunityHandoff records the owner's explicit manual-Handoff
// save (prepared → handoff_saved). The save is terminal and
// replay-safe; it never fills, attaches, sends or submits anything.
func (h *Handler) saveOpportunityHandoff(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.HandoffSave
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	workflow, created, err := h.database.SaveRoleHandoff(r.Context(),
		r.PathValue("id"), body.ExpectedWorkflowRevision)
	if err != nil {
		failHandoff(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, workflow)
}

func failHandoff(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected), errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role or Handoff not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Only a prepared role saves its Handoff.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "The role changed. Refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Handoff request failed.")
	}
}
