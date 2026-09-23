package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type compareOffersRequest struct {
	RequestKey     string   `json:"requestKey"`
	Offers         []string `json:"offers"`
	PrioritiesText string   `json:"prioritiesText,omitempty"`
}

func (h *Handler) compareOffersRound(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body compareOffersRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	if h.rounds == nil || body.RequestKey == "" || len(body.RequestKey) > 180 || strings.TrimSpace(body.RequestKey) != body.RequestKey {
		failRound(w, store.ErrInvalid)
		return
	}
	actor := store.Actor{Kind: p.Kind, ID: p.ID}
	intake, _, err := h.database.CreateOfferIntake(r.Context(), actor, body.RequestKey, body.Offers, body.PrioritiesText)
	if err != nil {
		failRound(w, err)
		return
	}
	resource := "offer_intake:" + intake.ID
	if prior, err := h.database.RoundByRequest(r.Context(), actor, body.RequestKey); err == nil {
		if prior.Outcome != "compare_offers" || len(prior.Scope.Resources) != 2 || prior.Scope.Resources[0] != resource || prior.Scope.Resources[1] != "campaign:active" {
			failRound(w, store.ErrRoundIdempotencyConflict)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"round": roundModel(prior), "intakeId": intake.ID})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		failRound(w, err)
		return
	}
	profile, err := h.database.CurrentPreferences(r.Context())
	if err != nil {
		failRound(w, err)
		return
	}
	input := store.StartRoundInput{RequestKey: body.RequestKey, Intent: "Compare the owner's complete supplied offers and identify a cited qualitative issue to review.", Outcome: "compare_offers", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{resource}, Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundPrepareOfferComparison, store.RoundJevRequest}, Delegates: []string{"codex-runner"}},
		Limits: store.RoundAllowance{Requests: 5, Items: 1, Tools: 3, Turns: 1}, Deadline: time.Now().Add(30 * time.Minute).UTC()}
	round, created, err := h.rounds.Start(r.Context(), actor, input)
	if err != nil {
		failRound(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"round": roundModel(round), "intakeId": intake.ID})
}

func (h *Handler) getOfferComparison(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	value, err := h.database.OfferComparisonForOwner(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *Handler) offerComparisonByRound(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	value, err := h.database.OfferComparisonByRoundForOwner(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("roundId"))
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
