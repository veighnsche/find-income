package httpapi

import (
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
)

func (h *Handler) codexStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	if h.codex == nil {
		writeJSON(w, http.StatusOK, codexservice.Status{State: "unavailable", Code: "runner_not_configured"})
		return
	}
	writeJSON(w, http.StatusOK, h.codex.Status(r.Context()))
}
func (h *Handler) codexConnect(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.codex == nil {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeInternalError, "An isolated Codex runner is not configured.")
		return
	}
	connection, err := h.codex.Connect(r.Context())
	if err != nil {
		if errors.Is(err, codexservice.ErrBusy) {
			fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "An ingestion is running.")
		} else {
			fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeInternalError, "Codex sign-in is unavailable; check connection status.")
		}
		return
	}
	writeJSON(w, http.StatusOK, connection)
}
func (h *Handler) codexCancelConnect(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.codex != nil {
		if err := h.codex.CancelConnect(r.Context()); err != nil {
			fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Could not cancel this connection attempt.")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) codexMCP(w http.ResponseWriter, r *http.Request) {
	if h.codex == nil {
		http.Error(w, "Bridge unavailable", http.StatusServiceUnavailable)
		return
	}
	h.codex.MCPHandler().ServeHTTP(w, r)
}
