package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func nonzeroTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func collectorBoardModel(value store.CollectorBoard) generated.CollectorBoard {
	return generated.CollectorBoard{
		Id: value.ID, Provider: generated.CollectorBoardProvider(value.Provider), Site: value.Site,
		DisplayName: value.DisplayName, OfficialCareersUrl: nonemptyString(value.OfficialCareersURL),
		VerifiedAt: optionalTime(value.VerifiedAt),
		Region:     generated.CollectorBoardRegion(value.Region), Enabled: value.Enabled,
		IntervalMinutes: value.IntervalMinutes, NextScanAt: value.NextScanAt,
		LastRunAt: nonzeroTime(value.LastRunAt), LastSuccessAt: nonzeroTime(value.LastSuccessAt),
		LastErrorCode: nonemptyString(value.LastErrorCode), Revision: value.Revision,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func collectorBoardFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Check the Lever site, region and scan interval.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "This board already exists or changed; refresh before saving.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not save collection board.")
	}
}

func (h *Handler) listCollectorBoards(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	boards, err := h.database.ListCollectorBoards(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not list collection boards.")
		return
	}
	items := make([]generated.CollectorBoard, 0, len(boards))
	for _, board := range boards {
		items = append(items, collectorBoardModel(board))
	}
	writeJSON(w, http.StatusOK, generated.CollectorBoardList{Items: items})
}

func (h *Handler) createCollectorBoard(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, principal) {
		return
	}
	var request struct {
		Provider        *string `json:"provider"`
		Site            *string `json:"site"`
		DisplayName     *string `json:"displayName"`
		Region          *string `json:"region"`
		Enabled         *bool   `json:"enabled"`
		IntervalMinutes *int    `json:"intervalMinutes"`
	}
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	if request.Provider == nil || request.Site == nil || request.Region == nil || request.Enabled == nil || request.IntervalMinutes == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Board details are required.")
		return
	}
	board, err := h.database.CreateCollectorBoard(r.Context(), principal.Actor(), store.CollectorBoardInput{
		Provider: *request.Provider, Site: *request.Site, Region: *request.Region,
		DisplayName: optionalString(request.DisplayName),
		Enabled:     *request.Enabled, IntervalMinutes: *request.IntervalMinutes,
	})
	if err != nil {
		collectorBoardFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, collectorBoardModel(board))
}

func (h *Handler) updateCollectorBoard(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, principal) {
		return
	}
	var request struct {
		ExpectedRevision *int64 `json:"expectedRevision"`
		Enabled          *bool  `json:"enabled"`
		IntervalMinutes  *int   `json:"intervalMinutes"`
	}
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	if request.ExpectedRevision == nil || request.Enabled == nil || request.IntervalMinutes == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Board revision, enabled state and interval are required.")
		return
	}
	board, err := h.database.UpdateCollectorBoard(r.Context(), principal.Actor(), r.PathValue("id"),
		*request.ExpectedRevision, *request.Enabled, *request.IntervalMinutes)
	if err != nil {
		collectorBoardFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, collectorBoardModel(board))
}
