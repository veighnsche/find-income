package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type actionDueRequest struct {
	Date     string `json:"date"`
	At       string `json:"at"`
	Timezone string `json:"timezone"`
}

func (d actionDueRequest) storeDue() store.ActionDue {
	return store.ActionDue{Date: d.Date, At: d.At, Timezone: d.Timezone}
}

type createActionRequest struct {
	OpportunityID string            `json:"opportunityId"`
	Description   string            `json:"description"`
	Due           *actionDueRequest `json:"due"`
}

type patchActionRequest struct {
	ExpectedRevision int64             `json:"expectedRevision"`
	Description      *string           `json:"description"`
	Due              *actionDueRequest `json:"due"`
}

type rescheduleActionRequest struct {
	ExpectedRevision int64             `json:"expectedRevision"`
	Due              *actionDueRequest `json:"due"`
}

type transitionActionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

func actionModel(value store.Action) map[string]any {
	due := value.Due()
	dueModel := map[string]any{}
	if due.Date != "" {
		dueModel["date"] = due.Date
	} else {
		dueModel["at"] = due.At
		dueModel["timezone"] = due.Timezone
	}
	model := map[string]any{
		"id": value.ID, "opportunityId": value.OpportunityID, "description": value.Description,
		"due": dueModel, "status": value.Status, "revision": value.Revision,
		"createdAt": value.CreatedAt, "updatedAt": value.UpdatedAt,
	}
	if value.DueAt != "" {
		model["dueAtUtc"] = value.DueAt
	}
	if value.CompletedAt != "" {
		model["completedAt"] = value.CompletedAt
	}
	return model
}

func actionPageModel(page store.ActionPage) map[string]any {
	items := make([]any, 0, len(page.Items))
	for _, value := range page.Items {
		items = append(items, actionModel(value))
	}
	model := map[string]any{"items": items}
	if page.NextCursor != "" {
		model["nextCursor"] = page.NextCursor
	}
	return model
}

func (h *Handler) actionMutationError(w http.ResponseWriter, r *http.Request, id string, err error) {
	if errors.Is(err, store.ErrConflict) {
		if current, loadErr := h.database.Action(r.Context(), id); loadErr == nil {
			conflictWithCurrent(w, current.Revision, "currentAction", actionModel(current))
			return
		}
	}
	failStore(w, err, "save action")
}

func actionPageOptions(w http.ResponseWriter, r *http.Request, additional ...string) (int, string, bool) {
	allowed := append([]string{"limit", "cursor"}, additional...)
	if !rejectUnknownQuery(w, r, allowed...) {
		return 0, "", false
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Limit must be between 1 and 100.")
			return 0, "", false
		}
		limit = value
	}
	return limit, r.URL.Query().Get("cursor"), true
}

func (h *Handler) listActions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "actions:read", false); !ok {
		return
	}
	limit, cursor, ok := actionPageOptions(w, r, "status", "opportunityId")
	if !ok {
		return
	}
	page, err := h.database.ListActions(r.Context(), store.ActionListOptions{Limit: limit, Cursor: cursor,
		Status: r.URL.Query().Get("status"), OpportunityID: r.URL.Query().Get("opportunityId")})
	if err != nil {
		failStore(w, err, "list actions")
		return
	}
	writeJSON(w, http.StatusOK, actionPageModel(page))
}

func (h *Handler) getAction(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "actions:read", false); !ok || !rejectUnknownQuery(w, r) {
		return
	}
	action, err := h.database.Action(r.Context(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "load action")
		return
	}
	writeJSON(w, http.StatusOK, actionModel(action))
}

func (h *Handler) createAction(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "actions:write", true)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	var request createActionRequest
	if !decodeEvidenceJSON(w, r, 8*1024, &request) {
		return
	}
	if request.Due == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Due deadline is required.")
		return
	}
	action, changeID, err := h.database.CreateAction(r.Context(), p.Actor(), store.ActionInput{
		OpportunityID: request.OpportunityID, Description: request.Description, Due: request.Due.storeDue()})
	if err != nil {
		failStore(w, err, "create action")
		return
	}
	w.Header().Set("Location", "/api/v1/actions/"+action.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"action": actionModel(action), "changeId": changeID})
}

func (h *Handler) patchAction(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "actions:write", true)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	var request patchActionRequest
	if !decodeEvidenceJSON(w, r, 8*1024, &request) {
		return
	}
	patch := store.ActionPatch{ExpectedRevision: request.ExpectedRevision, Description: request.Description}
	if request.Due != nil {
		due := request.Due.storeDue()
		patch.Due = &due
	}
	id := r.PathValue("id")
	action, changeID, err := h.database.PatchAction(r.Context(), p.Actor(), id, patch)
	if err != nil {
		h.actionMutationError(w, r, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": actionModel(action), "changeId": changeID})
}

func (h *Handler) rescheduleAction(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "actions:write", true)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	var request rescheduleActionRequest
	if !decodeEvidenceJSON(w, r, 8*1024, &request) {
		return
	}
	if request.Due == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Due deadline is required.")
		return
	}
	id := r.PathValue("id")
	action, changeID, err := h.database.RescheduleAction(r.Context(), p.Actor(), id, request.ExpectedRevision, request.Due.storeDue())
	if err != nil {
		h.actionMutationError(w, r, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": actionModel(action), "changeId": changeID})
}

func (h *Handler) transitionAction(w http.ResponseWriter, r *http.Request, complete bool) {
	p, ok := h.recordPrincipal(w, r, "actions:write", true)
	if !ok || !rejectUnknownQuery(w, r) {
		return
	}
	var request transitionActionRequest
	if !decodeEvidenceJSON(w, r, 8*1024, &request) {
		return
	}
	id := r.PathValue("id")
	var action store.Action
	var changeID string
	var err error
	if complete {
		action, changeID, err = h.database.CompleteAction(r.Context(), p.Actor(), id, request.ExpectedRevision)
	} else {
		action, changeID, err = h.database.CancelAction(r.Context(), p.Actor(), id, request.ExpectedRevision)
	}
	if err != nil {
		h.actionMutationError(w, r, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": actionModel(action), "changeId": changeID})
}

func (h *Handler) completeAction(w http.ResponseWriter, r *http.Request) {
	h.transitionAction(w, r, true)
}

func (h *Handler) cancelAction(w http.ResponseWriter, r *http.Request) {
	h.transitionAction(w, r, false)
}

func (h *Handler) dueActions(w http.ResponseWriter, r *http.Request, overdue bool) {
	if _, ok := h.recordPrincipal(w, r, "actions:read", false); !ok {
		return
	}
	limit, cursor, ok := actionPageOptions(w, r, "at", "calendarTimezone")
	if !ok {
		return
	}
	query := r.URL.Query()
	clock, err := time.Parse(time.RFC3339Nano, query.Get("at"))
	if err != nil || query.Get("calendarTimezone") == "" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Explicit RFC3339 clock and calendar timezone are required.")
		return
	}
	options := store.ActionDueListOptions{At: clock, CalendarTimezone: query.Get("calendarTimezone"), Cursor: cursor, Limit: limit}
	var page store.ActionPage
	if overdue {
		page, err = h.database.ListOverdueActions(r.Context(), options)
	} else {
		page, err = h.database.ListDueActions(r.Context(), options)
	}
	if err != nil {
		failStore(w, err, "list due actions")
		return
	}
	writeJSON(w, http.StatusOK, actionPageModel(page))
}

func (h *Handler) listDueActions(w http.ResponseWriter, r *http.Request) {
	h.dueActions(w, r, false)
}

func (h *Handler) listOverdueActions(w http.ResponseWriter, r *http.Request) {
	h.dueActions(w, r, true)
}
