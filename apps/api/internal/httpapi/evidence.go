package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const (
	evidenceSourceBodyLimit = 32 * 1024
	evidenceClaimBodyLimit  = 8 * 1024
)

type createSourceRequest struct {
	ExpectedContextVersion *int64 `json:"expectedContextVersion"`
	VacancySnapshot        *struct {
		RecordChangeAuditID string `json:"recordChangeAuditId"`
	} `json:"vacancySnapshot"`
	Statement *struct {
		SpeakerAffiliation  string `json:"speakerAffiliation"`
		SpeakerName         string `json:"speakerName"`
		SpeakerRole         string `json:"speakerRole"`
		SpeakerOrganisation string `json:"speakerOrganisation"`
		Channel             string `json:"channel"`
		OccurredAt          string `json:"occurredAt"`
		OriginalText        string `json:"originalText"`
		SourceURL           string `json:"sourceUrl"`
	} `json:"statement"`
	OwnerObservation *struct {
		OccurredAt                 string `json:"occurredAt"`
		OriginalText               string `json:"originalText"`
		ExpectedPreferencesVersion *int64 `json:"expectedPreferencesVersion"`
	} `json:"ownerObservation"`
}

type writeEvidenceRequest struct {
	SourceID                   string `json:"sourceId"`
	Criterion                  string `json:"criterion"`
	RoleCriterionID            string `json:"roleCriterionId"`
	RolePresence               string `json:"rolePresence"`
	ExpectedPreferencesVersion *int64 `json:"expectedPreferencesVersion"`
	OfferOptionID              string `json:"offerOptionId"`
	Finding                    string `json:"finding"`
	ObservedValue              string `json:"observedValue"`
	SpanStart                  *int   `json:"spanStart"`
	SpanEnd                    *int   `json:"spanEnd"`
	ExpectedEvidenceVersion    *int64 `json:"expectedEvidenceVersion"`
	Hours                      *struct {
		MinWeekly  string `json:"minWeekly"`
		MaxWeekly  string `json:"maxWeekly"`
		HardBounds *bool  `json:"hardBounds"`
	} `json:"hours"`
	Arrangement *struct {
		Pattern         string  `json:"pattern"`
		BaseLocation    *string `json:"baseLocation"`
		RemoteGeography *string `json:"remoteGeography"`
		OnsiteDays      *string `json:"onsiteDays"`
	} `json:"arrangement"`
	OwnerWorkableForEvidenceID *string `json:"ownerWorkableForEvidenceId"`
	Salary                     *struct {
		Currency          string `json:"currency"`
		Period            string `json:"period"`
		Basis             string `json:"basis"`
		AmountCents       *int64 `json:"amountCents"`
		ActualWeeklyHours string `json:"actualWeeklyHours"`
		AnnualConversion  string `json:"annualConversion"`
	} `json:"salary"`
}

// Decode a single strict JSON object. encoding/json replaces malformed UTF-8 and
// accepts duplicate keys; neither is acceptable for byte-offset source quotes.
func decodeEvidenceJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "JSON content type is required.")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil || !utf8.Valid(body) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Request body is too large or invalid UTF-8.")
		return false
	}
	check := json.NewDecoder(bytes.NewReader(body))
	if err := consumeStrictValue(check, true); err != nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid JSON structure, duplicate key or null.")
		return false
	}
	if _, err := check.Token(); !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Only one JSON object is allowed.")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid JSON request or unknown field.")
		return false
	}
	return true
}

func consumeStrictValue(decoder *json.Decoder, root bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null is not accepted")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if root {
			return errors.New("object required")
		}
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate key")
			}
			seen[key] = true
			if err := consumeStrictValue(decoder, false); err != nil {
				return err
			}
		}
	case '[':
		if root {
			return errors.New("object required")
		}
		for decoder.More() {
			if err := consumeStrictValue(decoder, false); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') && delim == '{' || end != json.Delim(']') && delim == '[' {
		return errors.New("unexpected JSON closing delimiter")
	}
	return nil
}

func evidencePageOptions(w http.ResponseWriter, r *http.Request, allowSuperseded bool) (int, string, bool, bool) {
	allowed := []string{"limit", "cursor"}
	if allowSuperseded {
		allowed = append(allowed, "includeSuperseded")
	}
	if !rejectUnknownQuery(w, r, allowed...) {
		return 0, "", false, false
	}
	query := r.URL.Query()
	limit := 25
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Limit must be between 1 and 100.")
			return 0, "", false, false
		}
		limit = value
	}
	include := false
	if allowSuperseded && query.Has("includeSuperseded") {
		raw := query.Get("includeSuperseded")
		if raw != "true" && raw != "false" {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "includeSuperseded must be true or false.")
			return 0, "", false, false
		}
		include = raw == "true"
	}
	return limit, query.Get("cursor"), include, true
}

func versionsModel(value store.QualificationInputVersions) map[string]any {
	return map[string]any{
		"opportunityId": value.OpportunityID, "opportunityRevision": value.OpportunityRevision,
		"companyId": value.CompanyID, "opportunityKind": value.OpportunityKind,
		"materialVersion": value.MaterialVersion, "evidenceVersion": value.EvidenceVersion,
		"contextVersion": value.ContextVersion, "preferencesVersion": value.PreferencesVersion,
		"rulesVersion": value.RulesVersion,
	}
}

func sourceModel(value store.EvidenceSource) map[string]any {
	model := map[string]any{
		"id": value.ID, "opportunityId": value.OpportunityID, "companyId": value.CompanyID,
		"opportunityKind": value.OpportunityKind, "contextVersion": value.ContextVersion,
		"sourceKind": value.SourceKind, "originalText": value.OriginalText,
		"contentSha256": value.ContentSHA256, "recordedAt": value.RecordedAt,
		"actorKind": value.Actor.Kind, "actorId": value.Actor.ID,
	}
	if value.RecordChangeAuditID != "" {
		model["recordChangeAuditId"] = value.RecordChangeAuditID
	}
	if value.SourceURL != "" {
		model["sourceUrl"] = value.SourceURL
	}
	if value.SpeakerName != "" {
		model["speakerName"] = value.SpeakerName
	}
	if value.SpeakerRole != "" {
		model["speakerRole"] = value.SpeakerRole
	}
	if value.SpeakerOrganisation != "" {
		model["speakerOrganisation"] = value.SpeakerOrganisation
	}
	if value.Channel != "" {
		model["channel"] = value.Channel
	}
	if value.OccurredAt != "" {
		model["occurredAt"] = value.OccurredAt
	}
	if value.OwnerPreferencesVersion != 0 {
		model["ownerPreferencesVersion"] = value.OwnerPreferencesVersion
	}
	return model
}

func evidenceModel(value store.Evidence) map[string]any {
	model := map[string]any{
		"id": value.ID, "opportunityId": value.OpportunityID, "sourceId": value.SourceID,
		"criterion": value.Criterion, "finding": value.Finding, "observedValue": value.ObservedValue,
		"sourceExcerpt": value.SourceExcerpt, "excerptSha256": value.ExcerptSHA256,
		"observedAt": value.ObservedAt, "createdAt": value.CreatedAt,
		"hasSpan": value.HasSpan,
	}
	if value.HasSpan {
		model["spanStart"], model["spanEnd"] = value.SpanStart, value.SpanEnd
	}
	if value.SourceKind != "" {
		model["sourceKind"] = value.SourceKind
	}
	if value.SourceURL != "" {
		model["sourceUrl"] = value.SourceURL
	}
	if value.SourceContactText != "" {
		model["sourceContactText"] = value.SourceContactText
	}
	if value.SupersedesID != "" {
		model["supersedesId"] = value.SupersedesID
	}
	if value.RoleCriterionID != "" {
		model["roleCriterionId"] = value.RoleCriterionID
		model["roleDefinitionHash"] = value.RoleDefinitionHash
		model["rolePreferencesVersion"] = value.RolePreferencesVersion
		model["rolePresence"] = value.RolePresence
		if value.RoleDefinition != nil {
			model["roleDefinition"] = value.RoleDefinition
		}
	}
	if value.OfferOptionID != "" {
		model["offerOptionId"] = value.OfferOptionID
	}
	if value.Hours != nil {
		model["hours"] = map[string]any{"minWeekly": formatHundredths(value.Hours.MinHundredths),
			"maxWeekly": formatHundredths(value.Hours.MaxHundredths), "hardBounds": value.Hours.HardBounds}
	}
	if value.Arrangement != nil {
		arrangement := map[string]any{"pattern": value.Arrangement.Pattern, "baseLocation": value.Arrangement.BaseLocation, "remoteGeography": value.Arrangement.RemoteGeography}
		if value.Arrangement.OnsiteDaysHundredths != nil {
			arrangement["onsiteDays"] = formatHundredths(*value.Arrangement.OnsiteDaysHundredths)
		}
		model["arrangement"] = arrangement
	}
	if value.OwnerWorkableForEvidenceID != "" {
		model["ownerWorkableForEvidenceId"] = value.OwnerWorkableForEvidenceID
	}
	if value.OwnerPreferencesVersion != 0 {
		model["ownerPreferencesVersion"] = value.OwnerPreferencesVersion
	}
	if value.Salary != nil {
		salary := map[string]any{"currency": value.Salary.Currency, "period": value.Salary.Period,
			"basis": value.Salary.Basis, "amountCents": value.Salary.AmountCents,
			"actualWeeklyHours": formatHundredths(value.Salary.ActualWeeklyHoursHundredths)}
		if value.Salary.AnnualConversion != "" {
			salary["annualConversion"] = value.Salary.AnnualConversion
		}
		model["salary"] = salary
	}
	return model
}

func criteriaModels(items []fit.CriterionResult) []map[string]any {
	criteria := make([]map[string]any, 0, len(items))
	for _, item := range items {
		criterion := map[string]any{"criterion": item.Criterion, "state": item.State,
			"reason": item.Reason, "conflicting": item.Conflicting,
			"blocking": item.Blocking, "relevant": item.Relevant, "sourceBasis": item.SourceBasis}
		if item.ID != "" {
			criterion["criterionId"] = item.ID
		}
		if item.Label != "" {
			criterion["label"] = item.Label
		}
		if item.Description != "" {
			criterion["description"] = item.Description
		}
		if item.Kind != "" {
			criterion["kind"] = item.Kind
		}
		if item.Mode != "" {
			criterion["mode"] = item.Mode
		}
		if len(item.EvidenceIDs) > 0 {
			criterion["evidenceIds"] = item.EvidenceIDs
		}
		criteria = append(criteria, criterion)
	}
	return criteria
}

func salaryModel(value fit.SalaryResult) map[string]any {
	salary := map[string]any{"state": value.State, "reason": value.Reason,
		"confirmedActual": value.ConfirmedActual, "conflicting": value.Conflicting,
		"applicable": value.Applicable, "concern": value.Concern,
		"currency": value.Currency, "targetHours": formatHundredths(value.TargetHoursHundredths),
		"sourceBasis": value.SourceBasis, "annualConversion": value.AnnualConversion}
	if value.Estimate != nil {
		salary["estimate"] = map[string]any{"minDisplayCents": value.Estimate.MinDisplayCents,
			"maxDisplayCents":  value.Estimate.MaxDisplayCents,
			"targetHours":      formatHundredths(value.Estimate.TargetHoursHundredths),
			"referenceHours":   formatHundredths(value.Estimate.ReferenceHoursHundredths),
			"currency":         value.Estimate.Currency,
			"annualConversion": value.Estimate.AnnualConversion}
	}
	return salary
}

func evaluationModel(value store.Evaluation) map[string]any {
	refs := append([]store.EvidenceRef{}, value.SourceRefs...)
	options := make([]map[string]any, 0, len(value.OptionResults))
	for _, item := range value.OptionResults {
		options = append(options, map[string]any{"optionId": item.OptionID, "label": item.Label,
			"overall": item.Overall, "criteria": criteriaModels(item.Criteria), "salary": salaryModel(item.Salary)})
	}
	setIDs := append([]string{}, value.OptionSetIDs...)
	return map[string]any{
		"id": value.ID, "opportunityId": value.OpportunityID, "opportunityRevision": value.OpportunityRevision,
		"materialVersion": value.MaterialVersion, "evidenceVersion": value.EvidenceVersion,
		"contextVersion": value.ContextVersion, "preferencesVersion": value.PreferencesVersion,
		"rulesVersion": value.RulesVersion, "overall": value.Overall,
		"criteria": criteriaModels(value.Criteria), "salary": salaryModel(value.Salary),
		"optionSetStatus": value.OptionSetStatus, "optionSetIds": setIDs, "optionResults": options,
		"sourceRefs": refs, "createdAt": value.CreatedAt,
		"actorKind": value.Actor.Kind, "actorId": value.Actor.ID,
	}
}

func evaluationMatchesInputs(value store.Evaluation, inputs store.QualificationInputVersions) bool {
	return value.OpportunityID == inputs.OpportunityID &&
		value.MaterialVersion == inputs.MaterialVersion && value.EvidenceVersion == inputs.EvidenceVersion &&
		value.ContextVersion == inputs.ContextVersion && value.PreferencesVersion == inputs.PreferencesVersion &&
		value.RulesVersion == inputs.RulesVersion
}

func (h *Handler) opportunityVersions(w http.ResponseWriter, r *http.Request) (store.QualificationInputVersions, bool) {
	versions, err := h.database.QualificationInputVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "load qualification inputs")
		return store.QualificationInputVersions{}, false
	}
	return versions, true
}

func (h *Handler) evidenceMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, store.ErrConflict) {
		failStore(w, err, "save evidence")
		return
	}
	versions, readErr := h.database.QualificationInputVersions(r.Context(), r.PathValue("id"))
	if readErr != nil {
		failStore(w, readErr, "load qualification inputs")
		return
	}
	details := map[string]any{"currentInputVersions": versionsModel(versions)}
	writeJSON(w, http.StatusConflict, generated.ErrorEnvelope{Error: generated.ApiError{
		Code: generated.ApiErrorCodeConflict, Message: "Evidence inputs changed. Refresh and reconcile before retrying.", Details: &details}})
}

func (h *Handler) sourceInOpportunity(w http.ResponseWriter, r *http.Request, id string) (store.EvidenceSource, bool) {
	value, err := h.database.EvidenceSource(r.Context(), id)
	if err != nil {
		failStore(w, err, "load evidence source")
		return store.EvidenceSource{}, false
	}
	if value.OpportunityID != r.PathValue("id") {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Record not found.")
		return store.EvidenceSource{}, false
	}
	return value, true
}

func (h *Handler) evidenceInOpportunity(w http.ResponseWriter, r *http.Request, id string) (store.Evidence, bool) {
	value, err := h.database.Evidence(r.Context(), id)
	if err != nil {
		failStore(w, err, "load evidence")
		return store.Evidence{}, false
	}
	if value.OpportunityID != r.PathValue("id") {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Record not found.")
		return store.Evidence{}, false
	}
	return value, true
}

func (h *Handler) listEvidenceSources(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	limit, cursor, _, ok := evidencePageOptions(w, r, false)
	if !ok {
		return
	}
	page, err := h.database.ListEvidenceSources(r.Context(), r.PathValue("id"), cursor, limit)
	if err != nil {
		failStore(w, err, "list evidence sources")
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, sourceModel(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor})
}

func (h *Handler) getEvidenceSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	value, ok := h.sourceInOpportunity(w, r, r.PathValue("sourceId"))
	if ok {
		writeJSON(w, http.StatusOK, sourceModel(value))
	}
}

func (h *Handler) createEvidenceSource(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "evidence:write", true)
	if !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	var request createSourceRequest
	if !decodeEvidenceJSON(w, r, evidenceSourceBodyLimit, &request) {
		return
	}
	variants := 0
	if request.VacancySnapshot != nil {
		variants++
	}
	if request.Statement != nil {
		variants++
	}
	if request.OwnerObservation != nil {
		variants++
	}
	if request.ExpectedContextVersion == nil || *request.ExpectedContextVersion < 1 || variants != 1 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "One source variant and expected context version are required.")
		return
	}
	input := store.SourceInput{OpportunityID: r.PathValue("id"), ExpectedContextVersion: *request.ExpectedContextVersion}
	if request.VacancySnapshot != nil {
		id := request.VacancySnapshot.RecordChangeAuditID
		if id == "" {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Vacancy change ID is required.")
			return
		}
		change, err := h.database.RecordChange(r.Context(), id)
		if err != nil {
			failStore(w, err, "load vacancy change")
			return
		}
		if change.EntityKind != "opportunity" || change.EntityID != r.PathValue("id") {
			fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Record not found.")
			return
		}
		input.VacancyChangeID = &id
	}
	if request.Statement != nil {
		value := request.Statement
		input.Statement = &store.StatementInput{SpeakerAffiliation: value.SpeakerAffiliation,
			SpeakerName: value.SpeakerName, SpeakerRole: value.SpeakerRole,
			SpeakerOrganisation: value.SpeakerOrganisation, Channel: value.Channel,
			OccurredAt: value.OccurredAt, OriginalText: value.OriginalText, SourceURL: value.SourceURL}
	}
	if request.OwnerObservation != nil {
		if !p.IsOwner() {
			fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Owner session required.")
			return
		}
		value := request.OwnerObservation
		if value.ExpectedPreferencesVersion == nil || *value.ExpectedPreferencesVersion < 1 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Expected preference version is required.")
			return
		}
		input.OwnerObservation = &store.OwnerInput{OccurredAt: value.OccurredAt,
			OriginalText: value.OriginalText, ExpectedPreferencesVersion: *value.ExpectedPreferencesVersion}
	}
	value, changeID, err := h.database.AddEvidenceSource(r.Context(), p.Actor(), input)
	if err != nil {
		h.evidenceMutationError(w, r, err)
		return
	}
	versions, ok := h.opportunityVersions(w, r)
	if !ok {
		return
	}
	w.Header().Set("Location", "/api/v1/opportunities/"+r.PathValue("id")+"/evidence-sources/"+value.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"source": sourceModel(value), "changeId": changeID, "currentInputVersions": versionsModel(versions)})
}

func (h *Handler) listEvidence(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	limit, cursor, include, ok := evidencePageOptions(w, r, true)
	if !ok {
		return
	}
	page, err := h.database.ListEvidence(r.Context(), r.PathValue("id"), include, cursor, limit)
	if err != nil {
		failStore(w, err, "list evidence")
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, evidenceModel(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor})
}

func (h *Handler) getEvidence(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	value, ok := h.evidenceInOpportunity(w, r, r.PathValue("evidenceId"))
	if ok {
		writeJSON(w, http.StatusOK, evidenceModel(value))
	}
}

func (h *Handler) writeEvidence(w http.ResponseWriter, r *http.Request, supersede bool) {
	p, ok := h.recordPrincipal(w, r, "evidence:write", true)
	if !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	var request writeEvidenceRequest
	if !decodeEvidenceJSON(w, r, evidenceClaimBodyLimit, &request) {
		return
	}
	if request.SpanStart == nil || request.SpanEnd == nil || request.ExpectedEvidenceVersion == nil ||
		*request.ExpectedEvidenceVersion < 0 || request.ExpectedPreferencesVersion == nil ||
		*request.ExpectedPreferencesVersion < 1 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Quote offsets and current evidence/preference versions are required.")
		return
	}
	if request.SourceID == "" || request.Hours != nil && request.Hours.HardBounds == nil ||
		request.Arrangement != nil && (request.Arrangement.BaseLocation == nil || request.Arrangement.RemoteGeography == nil) ||
		request.Salary != nil && request.Salary.AmountCents == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Required source or criterion facts are missing.")
		return
	}
	if request.Criterion == "role_criterion" &&
		(request.RoleCriterionID == "" || request.RolePresence == "") {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Current role criterion, presence and preference version are required.")
		return
	}
	if request.Criterion == "location_workable" && !p.IsOwner() {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Owner session required.")
		return
	}
	if _, ok := h.sourceInOpportunity(w, r, request.SourceID); !ok {
		return
	}
	if supersede {
		prior, ok := h.evidenceInOpportunity(w, r, r.PathValue("evidenceId"))
		if !ok {
			return
		}
		if prior.Criterion != request.Criterion ||
			request.Criterion == "role_criterion" && prior.RoleCriterionID != request.RoleCriterionID {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Replacement criterion must match prior claim.")
			return
		}
	}
	if request.OwnerWorkableForEvidenceID != nil {
		arrangement, ok := h.evidenceInOpportunity(w, r, *request.OwnerWorkableForEvidenceID)
		if !ok {
			return
		}
		if arrangement.Criterion != "location_arrangement" {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Referenced claim is not a work arrangement.")
			return
		}
	}
	input := store.EvidenceInput{OpportunityID: r.PathValue("id"), SourceID: request.SourceID,
		Criterion: request.Criterion, Finding: request.Finding, ObservedValue: request.ObservedValue,
		SpanStart: *request.SpanStart, SpanEnd: *request.SpanEnd,
		ExpectedEvidenceVersion:    *request.ExpectedEvidenceVersion,
		OwnerWorkableForEvidenceID: request.OwnerWorkableForEvidenceID}
	input.CriterionID = request.RoleCriterionID
	input.Presence = request.RolePresence
	input.OfferOptionID = request.OfferOptionID
	if request.ExpectedPreferencesVersion != nil {
		input.ExpectedPreferencesVersion = *request.ExpectedPreferencesVersion
	}
	if request.Hours != nil {
		minimum, minOK := parseHundredths(request.Hours.MinWeekly, 100, 16800)
		maximum, maxOK := parseHundredths(request.Hours.MaxWeekly, 100, 16800)
		if !minOK || !maxOK || maximum < minimum {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Hours must be exact decimals from 1 to 168 in increasing order.")
			return
		}
		input.Hours = &store.HoursAvailability{MinHundredths: minimum, MaxHundredths: maximum, HardBounds: *request.Hours.HardBounds}
	}
	if request.Arrangement != nil {
		input.Arrangement = &store.WorkArrangement{Pattern: request.Arrangement.Pattern, BaseLocation: *request.Arrangement.BaseLocation, RemoteGeography: *request.Arrangement.RemoteGeography}
		if request.Arrangement.OnsiteDays != nil {
			days, ok := parseHundredths(*request.Arrangement.OnsiteDays, 0, 700)
			if !ok {
				fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Onsite days must be an exact decimal from 0 to 7.")
				return
			}
			input.Arrangement.OnsiteDaysHundredths = &days
		}
	}
	if request.Salary != nil {
		hours, ok := parseHundredths(request.Salary.ActualWeeklyHours, 100, 16800)
		if !ok {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Actual hours must be an exact decimal from 1 to 168.")
			return
		}
		input.Salary = &store.ActualSalaryFacts{Currency: request.Salary.Currency, Period: request.Salary.Period,
			Basis: request.Salary.Basis, AmountCents: *request.Salary.AmountCents,
			ActualWeeklyHoursHundredths: hours, AnnualConversion: request.Salary.AnnualConversion}
	}
	var value store.Evidence
	var changeID string
	var err error
	if supersede {
		value, changeID, err = h.database.SupersedeEvidence(r.Context(), p.Actor(), r.PathValue("evidenceId"), input)
	} else {
		value, changeID, err = h.database.AddEvidence(r.Context(), p.Actor(), input)
	}
	if err != nil {
		h.evidenceMutationError(w, r, err)
		return
	}
	// The mutation return omits some source/span projection fields. Load the
	// immutable committed record before constructing the public DTO.
	value, err = h.database.Evidence(r.Context(), value.ID)
	if err != nil {
		failStore(w, err, "load saved evidence")
		return
	}
	versions, ok := h.opportunityVersions(w, r)
	if !ok {
		return
	}
	w.Header().Set("Location", "/api/v1/opportunities/"+r.PathValue("id")+"/evidence/"+value.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"evidence": evidenceModel(value), "changeId": changeID, "currentInputVersions": versionsModel(versions)})
}

func (h *Handler) createEvidence(w http.ResponseWriter, r *http.Request) {
	h.writeEvidence(w, r, false)
}
func (h *Handler) supersedeEvidence(w http.ResponseWriter, r *http.Request) {
	h.writeEvidence(w, r, true)
}

func (h *Handler) getQualification(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	view, refreshErr := h.database.Qualification(r.Context(), r.PathValue("id"))
	versions, ok := h.opportunityVersions(w, r)
	if !ok {
		return
	}
	if refreshErr != nil && errors.Is(refreshErr, store.ErrNotFound) {
		failStore(w, refreshErr, "load qualification")
		return
	}
	status := view.Status
	var current any
	var historical any
	if view.Current != nil && refreshErr == nil && evaluationMatchesInputs(*view.Current, versions) {
		current = evaluationModel(*view.Current)
		status = "current"
	} else if status == "current" || refreshErr != nil {
		status = "outdated"
	}
	if view.LatestHistorical != nil {
		historical = evaluationModel(*view.LatestHistorical)
	} else if view.Current != nil && current == nil {
		historical = evaluationModel(*view.Current)
	}
	response := map[string]any{"status": status, "current": current, "latestHistorical": historical,
		"currentInputVersions": versionsModel(versions)}
	if refreshErr != nil {
		response["refreshError"] = "Local qualification refresh failed. Retry the read."
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) listQualificationHistory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	limit, cursor, _, ok := evidencePageOptions(w, r, false)
	if !ok {
		return
	}
	page, err := h.database.ListQualificationHistory(r.Context(), r.PathValue("id"), cursor, limit)
	if err != nil {
		failStore(w, err, "list qualification history")
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, evaluationModel(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor})
}

func (h *Handler) reevaluateQualification(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "evidence:write", true)
	if !ok {
		return
	}
	if _, ok := h.opportunityVersions(w, r); !ok {
		return
	}
	if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1)); err != nil || len(body) != 0 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "This operation takes no request body.")
		return
	}
	value, changeID, err := h.database.EvaluateCurrent(r.Context(), p.Actor(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "reevaluate qualification")
		return
	}
	versions, ok := h.opportunityVersions(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evaluation": evaluationModel(value),
		"changeId": changeID, "currentInputVersions": versionsModel(versions)})
}
