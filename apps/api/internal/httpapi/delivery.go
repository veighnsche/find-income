package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/veighnsche/find-income-dashboard/api/internal/deliveryservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (h *Handler) deliveryCapability(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	configured := h.delivery != nil && h.delivery.Sender != nil && h.delivery.From != ""
	reason := "SMTP submission has no verified employer receipt lookup."
	if !configured {
		reason = "An authenticated SMTP sender is not configured."
	}
	writeJSON(w, http.StatusOK, struct {
		SubmissionAvailable bool   `json:"submissionAvailable"`
		ReceiptLookup       bool   `json:"receiptLookup"`
		Reason              string `json:"reason,omitempty"`
	}{configured, false, reason})
}

func (h *Handler) stopAnyRound(w http.ResponseWriter, r *http.Request) {
	round, err := h.database.Round(r.Context(), r.PathValue("id"))
	if err != nil || round.Outcome != "deliver" {
		h.stopRound(w, r)
		return
	}
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.delivery == nil {
		failDelivery(w, deliveryservice.ErrUnavailable)
		return
	}
	stopped, err := h.delivery.StopRound(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, round.ID)
	if err != nil {
		failRound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roundModel(stopped))
}

func (h *Handler) resumeAnyRound(w http.ResponseWriter, r *http.Request) {
	round, err := h.database.Round(r.Context(), r.PathValue("id"))
	if err == nil && round.Outcome == "deliver" {
		p, ok := h.owner(w, r)
		if !ok || !h.mutationAllowed(w, r, p) {
			return
		}
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "This SMTP adapter has no read-only receipt lookup; delivery cannot resume or resend.")
		return
	}
	h.resumeRound(w, r)
}

func failDelivery(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Delivery review not found.")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrFenced), errors.Is(err, store.ErrActiveRound), errors.Is(err, store.ErrAllowance):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Delivery review is stale, already in progress, or outside its commission.")
	case errors.Is(err, deliveryservice.ErrUnavailable):
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeInternalError, "An authenticated sender is not configured.")
	case errors.Is(err, deliveryservice.ErrUnsupportedRoute):
		fail(w, http.StatusUnprocessableEntity, generated.ApiErrorCodeValidationError, "No single evidenced email application route is available.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid delivery review material.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Delivery operation could not complete.")
	}
}

func (h *Handler) prepareDeliveryReview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var input struct {
		RequestKey string   `json:"requestKey"`
		PackIDs    []string `json:"packIds"`
	}
	if !decodeRecordJSON(w, r, &input) {
		return
	}
	if h.delivery == nil {
		failDelivery(w, deliveryservice.ErrUnavailable)
		return
	}
	review, err := h.delivery.PrepareReview(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, input.RequestKey, input.PackIDs)
	if err != nil {
		failDelivery(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, review)
}

func (h *Handler) getDeliveryReview(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	review, err := h.database.DeliveryReview(r.Context(), r.PathValue("id"))
	if err != nil {
		failDelivery(w, err)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func (h *Handler) approveDeliveryReview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var input struct {
		MaterialSHA256 string `json:"materialSha256"`
	}
	if !decodeRecordJSON(w, r, &input) {
		return
	}
	if h.delivery == nil {
		failDelivery(w, deliveryservice.ErrUnavailable)
		return
	}
	review, err := h.delivery.ApproveReview(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), input.MaterialSHA256)
	if err != nil {
		failDelivery(w, err)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func (h *Handler) sendDeliveryReview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.delivery == nil {
		failDelivery(w, deliveryservice.ErrUnavailable)
		return
	}
	result, err := h.delivery.SendReview(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"))
	if err != nil {
		failDelivery(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Review store.DeliveryReview `json:"review"`
		Round  roundResponse        `json:"round"`
	}{Review: result.Review, Round: roundModel(result.Round)})
}

func (h *Handler) reconcileDeliveryReview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	// SMTP has no authenticated read-only receipt query. Keep the item uncertain
	// and report capability truthfully; never retry or infer a received application.
	if _, err := h.database.DeliveryReview(r.Context(), r.PathValue("id")); err != nil {
		failDelivery(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Supported bool   `json:"supported"`
		Reason    string `json:"reason"`
	}{false, "This SMTP adapter cannot verify employer receipt or reconcile an unknown DATA outcome."})
}

func (h *Handler) closeDeliveryReview(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	review, err := h.database.DeliveryReview(r.Context(), r.PathValue("id"))
	if err != nil {
		failDelivery(w, err)
		return
	}
	roundID := ""
	for _, item := range review.Items {
		if item.State == "sending" {
			fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Wait for the in-flight submission to finish or restart recovery before closing this commission.")
			return
		}
		if item.RoundID != "" {
			if roundID != "" && roundID != item.RoundID {
				failDelivery(w, store.ErrConflict)
				return
			}
			roundID = item.RoundID
		}
	}
	if roundID == "" {
		prior, err := h.database.RoundByRequest(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, "delivery:"+review.ID)
		if err != nil {
			failDelivery(w, err)
			return
		}
		roundID = prior.ID
	}
	round, err := h.database.Round(r.Context(), roundID)
	if err != nil {
		failDelivery(w, err)
		return
	}
	if round.Outcome != "deliver" || round.State != store.RoundPaused || len(round.Scope.InputRefs) != 1 || round.Scope.InputRefs[0] != "delivery_review:"+review.ID {
		failDelivery(w, store.ErrFenced)
		return
	}
	closed, err := h.database.FinishRound(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, round.ID,
		store.RoundCompleted, "unresolved_delivery_closed", "submission_unverified", json.RawMessage(`{"employerReceiptVerified":false,"uncertainDeliveryRemains":true}`))
	if err != nil {
		failDelivery(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roundModel(closed))
}
