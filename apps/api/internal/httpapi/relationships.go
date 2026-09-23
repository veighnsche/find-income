package httpapi

import (
	"net/http"
)

// Relationship reads are private and read-only. Codex writes records through
// the scoped round mutation endpoint, so the browser has no contact form API.
func (h *Handler) listRelationshipCounterparties(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok || !rejectUnknownQuery(w, r) {
		return
	}
	items, err := h.database.ListRelationshipCounterparties(r.Context())
	if err != nil {
		failStore(w, err, "list relationship counterparties")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) listRelationshipEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok || !rejectUnknownQuery(w, r) {
		return
	}
	items, err := h.database.ListRelationshipEvents(r.Context())
	if err != nil {
		failStore(w, err, "list relationship events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) listOpportunityRoutes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok || !rejectUnknownQuery(w, r) {
		return
	}
	items, err := h.database.ListOpportunityRoutes(r.Context(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "list opportunity routes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
