package httpapi

import (
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func failWorkflow(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role is not selected.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Opportunity not found.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not read role workflow.")
	}
}

func (h *Handler) listRoleWorkflows(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	items, err := h.database.ListRoleWorkflows(r.Context())
	if err != nil {
		failWorkflow(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []store.RoleWorkflow `json:"items"`
	}{items})
}

func (h *Handler) getRoleWorkflow(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	value, err := h.database.RoleWorkflow(r.Context(), r.PathValue("id"))
	if err != nil {
		failWorkflow(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
