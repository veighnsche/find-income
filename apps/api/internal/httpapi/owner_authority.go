package httpapi

import (
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (h *Handler) ownerInstructions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	items, err := h.database.OwnerInstructions(r.Context(), r.URL.Query().Get("roundId"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []store.OwnerInstruction `json:"items"`
	}{items})
}

func (h *Handler) addOwnerInstruction(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var input store.OwnerInstructionInput
	if !decodeRecordJSON(w, r, &input) {
		return
	}
	value, created, err := h.database.AddOwnerInstruction(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, value)
}

func (h *Handler) revokeOwnerInstruction(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if err := h.database.RevokeOwnerInstruction(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id")); err != nil {
		failRound(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getOwnerOpportunityDecision(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	value, err := h.database.OwnerOpportunityDecision(r.Context(), r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *Handler) setOwnerOpportunityDecision(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var input store.OwnerDecisionInput
	if !decodeRecordJSON(w, r, &input) {
		return
	}
	value, created, err := h.database.SetOwnerOpportunityDecision(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, value)
}

func (h *Handler) roundCards(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	items, err := h.database.RoundCards(r.Context(), r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []store.RoundCard `json:"items"`
	}{items})
}

func (h *Handler) roundCandidates(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	leads, searches, err := h.database.RoundCandidateLeads(r.Context(), r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Leads    []store.RoundCandidateLead   `json:"leads"`
		Searches []store.RoundCandidateSearch `json:"searches"`
	}{leads, searches})
}

func (h *Handler) roundHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	items, err := h.database.RoundHistory(r.Context(), r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []store.RoundHistoryEvent `json:"items"`
	}{items})
}
