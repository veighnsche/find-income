package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type updatePreferencesRequest struct {
	ExpectedVersion     *int64                 `json:"expectedVersion"`
	PreferredLocation   *string                `json:"preferredLocation"`
	AllowRemote         *bool                  `json:"allowRemote"`
	AllowHybrid         *bool                  `json:"allowHybrid"`
	TargetHours         *string                `json:"targetHours"`
	MinMonthlyBaseCents *int64                 `json:"minMonthlyBaseCents"`
	SalaryCurrency      *string                `json:"salaryCurrency"`
	Timezone            *string                `json:"timezone"`
	RoleCriteria        *[]store.RoleCriterion `json:"roleCriteria"`
}

// parseHundredths keeps weekly-hour and onsite-day comparisons exact, with no
// binary floating-point conversion at the HTTP boundary.
func parseHundredths(value string, minimum, maximum int64) (int64, bool) {
	if value == "" || strings.TrimSpace(value) != value || strings.Count(value, ".") > 1 {
		return 0, false
	}
	parts := strings.Split(value, ".")
	if len(parts[0]) == 0 || len(parts[0]) > 3 {
		return 0, false
	}
	for _, part := range parts {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return 0, false
			}
		}
	}
	if len(parts) == 2 && (len(parts[1]) == 0 || len(parts[1]) > 2) {
		return 0, false
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	result := whole * 100
	if len(parts) == 2 {
		fraction := parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
		cents, err := strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, false
		}
		result += cents
	}
	return result, result >= minimum && result <= maximum
}

func formatHundredths(value int64) string {
	whole, fraction := value/100, value%100
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	if fraction%10 == 0 {
		return strconv.FormatInt(whole, 10) + "." + strconv.FormatInt(fraction/10, 10)
	}
	if fraction < 10 {
		return strconv.FormatInt(whole, 10) + ".0" + strconv.FormatInt(fraction, 10)
	}
	return strconv.FormatInt(whole, 10) + "." + strconv.FormatInt(fraction, 10)
}

func preferencesModel(value store.Preferences) map[string]any {
	criteria := make([]map[string]any, 0, len(value.RoleCriteria))
	for _, criterion := range value.RoleCriteria {
		criteria = append(criteria, map[string]any{"id": criterion.ID, "label": criterion.Label,
			"description": criterion.Description, "kind": criterion.Kind, "mode": criterion.Mode,
			"definitionHash": criterion.DefinitionHash()})
	}
	return map[string]any{
		"version":             value.Version,
		"preferredLocation":   value.PreferredLocation,
		"allowRemote":         value.AllowRemote,
		"allowHybrid":         value.AllowHybrid,
		"targetHours":         formatHundredths(value.TargetHoursHundredths),
		"minMonthlyBaseCents": value.MinMonthlyBaseCents,
		"salaryCurrency":      value.SalaryCurrency,
		"timezone":            value.Timezone,
		"roleCriteria":        criteria,
	}
}

func (h *Handler) preferences(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	if !principal.HasScope("preferences:read") {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Scope is required.")
		return
	}
	p, err := h.database.CurrentPreferences(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not load preferences.")
		return
	}
	writeJSON(w, http.StatusOK, preferencesModel(p))
}

func (h *Handler) updatePreferences(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, principal) {
		return
	}
	var request updatePreferencesRequest
	if !decodeEvidenceJSON(w, r, 16*1024, &request) {
		return
	}
	if request.ExpectedVersion == nil || *request.ExpectedVersion < 1 || request.PreferredLocation == nil ||
		request.AllowRemote == nil || request.AllowHybrid == nil || request.TargetHours == nil ||
		request.MinMonthlyBaseCents == nil || request.SalaryCurrency == nil || request.Timezone == nil ||
		request.RoleCriteria == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "A complete preference profile and expected version are required.")
		return
	}
	hours, valid := parseHundredths(*request.TargetHours, 100, 16800)
	if !valid {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Target hours must be an exact decimal from 1 to 168 with at most two places.")
		return
	}
	next := store.Preferences{
		PreferredLocation: *request.PreferredLocation, AllowRemote: *request.AllowRemote,
		AllowHybrid: *request.AllowHybrid, TargetHoursHundredths: hours,
		MinMonthlyBaseCents: *request.MinMonthlyBaseCents, SalaryCurrency: *request.SalaryCurrency,
		Timezone: *request.Timezone, RoleCriteria: *request.RoleCriteria,
	}
	saved, changeID, err := h.database.UpdatePreferences(r.Context(), *request.ExpectedVersion, next, principal.Actor())
	if errors.Is(err, store.ErrConflict) {
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Preferences changed. Review the current version before retrying.")
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid preference profile.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not save preferences.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": preferencesModel(saved), "changeId": changeID})
}
