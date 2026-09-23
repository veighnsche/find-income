package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func ingestionModel(item store.IngestionRequest) generated.IngestionRequest {
	return generated.IngestionRequest{
		Id: item.ID, Origin: item.Origin,
		SourceUrl: item.SourceURL, OriginalText: item.OriginalText,
		Status: generated.IngestionRequestStatus(item.Status), JobId: item.JobID,
		JobState:        generated.IngestionRequestJobState(item.JobState),
		AttemptsStarted: item.AttemptsStarted,
		ConnectorId:     nonemptyString(item.ConnectorID), ExternalId: nonemptyString(item.ExternalID),
		DiscoveredAt: optionalTime(item.DiscoveredAt), OpportunityId: nonemptyString(item.OpportunityID),
		RecordChangeId: nonemptyString(item.RecordChangeID), SafeErrorCode: nonemptyString(item.SafeErrorCode),
		CreatedAt: recordedTime(item.CreatedAt), UpdatedAt: recordedTime(item.UpdatedAt),
	}
}

func ingestionFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Provide a valid job URL or full vacancy text.")
	case errors.Is(err, store.ErrJobIdempotencyConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "This submission key was used with different content.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "This submission is not ready for retry.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Submission not found.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not access the submission.")
	}
}

func (h *Handler) listIngestions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	if !rejectUnknownQuery(w, r, "limit", "cursor") {
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Limit must be between 1 and 100.")
			return
		}
		limit = parsed
	}
	page, err := h.database.ListIngestions(r.Context(), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		ingestionFailure(w, err)
		return
	}
	items := make([]generated.IngestionRequest, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, ingestionModel(item))
	}
	writeJSON(w, http.StatusOK, generated.IngestionPage{Items: items, NextCursor: nonemptyString(page.NextCursor)})
}

func (h *Handler) submitIngestion(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.recordPrincipal(w, r, "openings:ingest", true)
	if !ok {
		return
	}
	var request generated.SubmitIngestionRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	origin := "agent"
	if principal.IsOwner() {
		origin = "owner"
	}
	item, _, err := h.database.SubmitIngestion(r.Context(), principal.Actor(), store.IngestionInput{
		Origin: origin, SourceURL: optionalString(request.SourceUrl),
		OriginalText: optionalString(request.OriginalText), IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		ingestionFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, ingestionModel(item))
}

func (h *Handler) getIngestion(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.recordPrincipal(w, r, "openings:ingest", false)
	if !ok {
		return
	}
	item, err := h.database.Ingestion(r.Context(), r.PathValue("id"))
	if err != nil {
		ingestionFailure(w, err)
		return
	}
	if !principal.IsOwner() && item.Actor != principal.Actor() {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Submission not found.")
		return
	}
	writeJSON(w, http.StatusOK, ingestionModel(item))
}

func (h *Handler) retryIngestion(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.recordPrincipal(w, r, "openings:ingest", true)
	if !ok {
		return
	}
	if !principal.IsOwner() {
		item, err := h.database.Ingestion(r.Context(), r.PathValue("id"))
		if err != nil || item.Actor != principal.Actor() {
			fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Submission not found.")
			return
		}
	}
	var request generated.RetryIngestionRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	item, err := h.database.RetryIngestion(r.Context(), principal.Actor(), r.PathValue("id"), request.OriginalText)
	if err != nil {
		ingestionFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, ingestionModel(item))
}
