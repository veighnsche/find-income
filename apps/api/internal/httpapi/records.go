package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/auth"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Vacancy text can be 200 KiB after decoding. Keep its transport budget local
// to these routes; the login/token request budget remains 4 KiB.
const recordBodyLimit = 256 * 1024

func (h *Handler) recordPrincipal(w http.ResponseWriter, r *http.Request, scope string, mutation bool) (auth.Principal, bool) {
	p, ok := h.principal(w, r)
	if !ok {
		return auth.Principal{}, false
	}
	if !p.HasScope(scope) {
		fail(w, http.StatusForbidden, generated.ApiErrorCodeForbidden, "Scope is required.")
		return auth.Principal{}, false
	}
	if mutation && p.IsOwner() && !h.mutationAllowed(w, r, p) {
		return auth.Principal{}, false
	}
	return p, true
}

func decodeRecordJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "JSON content type is required.")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, recordBodyLimit))
	if err != nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Request body is too large or incomplete.")
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "A JSON object is required.")
		return false
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Use an empty string to clear an optional field; null is not accepted.")
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid JSON request or unknown field.")
		return false
	}
	return true
}

func rejectUnknownQuery(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	known := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		known[key] = true
	}
	for key, values := range r.URL.Query() {
		if !known[key] || len(values) != 1 || values[0] == "" {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Unknown or repeated query parameter.")
			return false
		}
	}
	return true
}

func recordPageOptions(w http.ResponseWriter, r *http.Request, allowed ...string) (int, string, bool, bool) {
	keys := append([]string{"limit", "cursor", "includeArchived"}, allowed...)
	if !rejectUnknownQuery(w, r, keys...) {
		return 0, "", false, false
	}
	query := r.URL.Query()
	limit := 0
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Limit must be between 1 and 100.")
			return 0, "", false, false
		}
		limit = value
	}
	includeArchived := false
	if raw := query.Get("includeArchived"); raw != "" {
		if raw != "true" && raw != "false" {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "includeArchived must be true or false.")
			return 0, "", false, false
		}
		includeArchived = raw == "true"
	}
	return limit, query.Get("cursor"), includeArchived, true
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nonemptyString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func recordedTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func optionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed := recordedTime(value)
	return &parsed
}

func companyModel(value store.Company) generated.Company {
	return generated.Company{Id: value.ID, Name: value.Name, Website: value.Website, Notes: value.Notes,
		ArchivedAt: optionalTime(value.ArchivedAt), Revision: value.Revision,
		CreatedAt: recordedTime(value.CreatedAt), UpdatedAt: recordedTime(value.UpdatedAt)}
}

func compensationInput(value *generated.AdvertisedCompensation) (store.AdvertisedCompensation, bool) {
	result := store.AdvertisedCompensation{}
	if value == nil {
		return result, true
	}
	if value.Currency != nil {
		result.Currency = *value.Currency
	}
	if value.Period != nil {
		result.Period = string(*value.Period)
	}
	if value.Basis != nil {
		result.Basis = string(*value.Basis)
	}
	result.MinAmountCents = value.MinAmountCents
	result.MaxAmountCents = value.MaxAmountCents
	if value.ReferenceHours != nil {
		hours, ok := parseHundredths(*value.ReferenceHours, 100, 16800)
		if !ok {
			return result, false
		}
		result.ReferenceHoursHundredths = &hours
	}
	if value.AnnualConversion != nil {
		result.AnnualConversion = string(*value.AnnualConversion)
	}
	result.AnnualConversionSpanStart = value.AnnualConversionSpanStart
	result.AnnualConversionSpanEnd = value.AnnualConversionSpanEnd
	result.BenefitsText = optionalString(value.BenefitsText)
	return result, true
}

func compensationModel(value store.AdvertisedCompensation) generated.AdvertisedCompensation {
	basis := generated.AdvertisedCompensationBasis(value.Basis)
	period := generated.AdvertisedCompensationPeriod(value.Period)
	result := generated.AdvertisedCompensation{Currency: &value.Currency,
		MinAmountCents: value.MinAmountCents, MaxAmountCents: value.MaxAmountCents,
		Period: &period, Basis: &basis,
		BenefitsText: &value.BenefitsText}
	if value.ReferenceHoursHundredths != nil {
		hours := formatHundredths(*value.ReferenceHoursHundredths)
		result.ReferenceHours = &hours
	}
	if value.AnnualConversion != "" {
		converted := generated.AdvertisedCompensationAnnualConversion(value.AnnualConversion)
		result.AnnualConversion = &converted
		result.AnnualConversionSpanStart = value.AnnualConversionSpanStart
		result.AnnualConversionSpanEnd = value.AnnualConversionSpanEnd
	}
	return result
}

func opportunityModel(value store.Opportunity) generated.Opportunity {
	return generated.Opportunity{Id: value.ID, CompanyId: value.CompanyID, Title: value.Title,
		Kind: generated.OpportunityKind(value.Kind), SourceUrl: value.SourceURL,
		OriginalText: value.OriginalText, Notes: value.Notes, Stage: value.Stage,
		WorkPattern: generated.OpportunityWorkPattern(value.WorkPattern), LocationText: value.LocationText,
		PostedOn: value.PostedOn, DeadlineOn: value.DeadlineOn, ArchivedAt: optionalTime(value.ArchivedAt),
		Revision: value.Revision, CreatedAt: recordedTime(value.CreatedAt), UpdatedAt: recordedTime(value.UpdatedAt),
		Compensation: compensationModel(value.Compensation)}
}

func changeModel(value store.RecordChange) generated.RecordChange {
	result := generated.RecordChange{Sequence: value.Sequence, ChangeId: value.ChangeID,
		EntityKind: generated.RecordChangeEntityKind(value.EntityKind), EntityId: value.EntityID,
		Operation: value.Operation, ActorKind: generated.RecordChangeActorKind(value.Actor.Kind),
		ActorId: value.Actor.ID, RevisionBefore: value.RevisionBefore, RevisionAfter: value.RevisionAfter,
		OccurredAt: recordedTime(value.OccurredAt), SnapshotState: generated.RecordChangeSnapshotState(value.SnapshotState)}
	if value.Snapshot != nil {
		var snapshot map[string]interface{}
		_ = json.Unmarshal(value.Snapshot, &snapshot)
		result.Snapshot = &snapshot
	}
	return result
}

func failStore(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid "+action+" input.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Record not found.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "The record changed. Refresh and reconcile your edits.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Could not "+action+".")
	}
}

func conflictWithCurrent(w http.ResponseWriter, currentRevision int64, field string, current any) {
	details := map[string]interface{}{"currentRevision": currentRevision, field: current}
	writeJSON(w, http.StatusConflict, generated.ErrorEnvelope{Error: generated.ApiError{
		Code: generated.ApiErrorCodeConflict, Message: "The record changed. Refresh and reconcile your edits.", Details: &details}})
}

func (h *Handler) companyMutationError(w http.ResponseWriter, r *http.Request, id string, err error) {
	if errors.Is(err, store.ErrConflict) {
		if current, loadErr := h.database.Company(r.Context(), id); loadErr == nil {
			conflictWithCurrent(w, current.Revision, "currentCompany", companyModel(current))
			return
		}
	}
	failStore(w, err, "save company")
}

func (h *Handler) opportunityMutationError(w http.ResponseWriter, r *http.Request, id string, err error) {
	if errors.Is(err, store.ErrConflict) {
		if current, loadErr := h.database.Opportunity(r.Context(), id); loadErr == nil {
			conflictWithCurrent(w, current.Revision, "currentOpportunity", opportunityModel(current))
			return
		}
	}
	failStore(w, err, "save opportunity")
}

func (h *Handler) companyView(r *http.Request, value store.Company) (generated.CompanyView, error) {
	duplicates, err := h.database.LikelyDuplicateCompanies(r.Context(), store.CompanyInput{
		Name: value.Name, Website: value.Website, Notes: value.Notes}, value.ID)
	if err != nil {
		return generated.CompanyView{}, err
	}
	view := generated.CompanyView{Company: companyModel(value), LikelyDuplicates: make([]generated.CompanyDuplicate, 0, len(duplicates))}
	for _, duplicate := range duplicates {
		view.LikelyDuplicates = append(view.LikelyDuplicates, generated.CompanyDuplicate{
			Company: companyModel(duplicate.Company), Reason: generated.CompanyDuplicateReason(duplicate.Reason)})
	}
	return view, nil
}

func (h *Handler) opportunityView(r *http.Request, value store.Opportunity) (generated.OpportunityView, error) {
	duplicates, err := h.database.LikelyDuplicateOpportunities(r.Context(), opportunityInput(value), value.ID)
	if err != nil {
		return generated.OpportunityView{}, err
	}
	view := generated.OpportunityView{Opportunity: opportunityModel(value), LikelyDuplicates: make([]generated.OpportunityDuplicate, 0, len(duplicates))}
	for _, duplicate := range duplicates {
		view.LikelyDuplicates = append(view.LikelyDuplicates, generated.OpportunityDuplicate{
			Opportunity: opportunityModel(duplicate.Opportunity), Reason: generated.OpportunityDuplicateReason(duplicate.Reason)})
	}
	return view, nil
}

func opportunityInput(value store.Opportunity) store.OpportunityInput {
	return store.OpportunityInput{CompanyID: value.CompanyID, Title: value.Title, Kind: value.Kind,
		SourceURL: value.SourceURL, OriginalText: value.OriginalText, Notes: value.Notes,
		Stage: value.Stage, WorkPattern: value.WorkPattern, LocationText: value.LocationText,
		PostedOn: value.PostedOn, DeadlineOn: value.DeadlineOn, Compensation: value.Compensation}
}

func (h *Handler) listCompanies(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	limit, cursor, archived, ok := recordPageOptions(w, r)
	if !ok {
		return
	}
	page, err := h.database.ListCompanies(r.Context(), store.CompanyListOptions{Limit: limit, Cursor: cursor, IncludeArchived: archived})
	if err != nil {
		failStore(w, err, "list companies")
		return
	}
	response := generated.CompanyPage{Items: make([]generated.CompanyView, 0, len(page.Items)), NextCursor: nonemptyString(page.NextCursor)}
	for _, item := range page.Items {
		view, err := h.companyView(r, item)
		if err != nil {
			failStore(w, err, "load company duplicates")
			return
		}
		response.Items = append(response.Items, view)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getCompany(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	company, err := h.database.Company(r.Context(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "load company")
		return
	}
	view, err := h.companyView(r, company)
	if err != nil {
		failStore(w, err, "load company duplicates")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) createCompany(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "opportunities:write", true)
	if !ok {
		return
	}
	var request generated.CreateCompanyRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	company, changeID, err := h.database.CreateCompany(r.Context(), p.Actor(), store.CompanyInput{
		Name: request.Name, Website: optionalString(request.Website), Notes: optionalString(request.Notes)})
	if err != nil {
		failStore(w, err, "create company")
		return
	}
	w.Header().Set("Location", "/api/v1/companies/"+company.ID)
	writeJSON(w, http.StatusCreated, generated.CompanyMutation{Company: companyModel(company), ChangeId: changeID})
}

func (h *Handler) patchCompany(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "opportunities:write", true)
	if !ok {
		return
	}
	var request generated.PatchCompanyRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	company, changeID, err := h.database.PatchCompany(r.Context(), p.Actor(), r.PathValue("id"), store.CompanyPatch{
		ExpectedRevision: request.ExpectedRevision, Name: request.Name, Website: request.Website, Notes: request.Notes})
	if err != nil {
		h.companyMutationError(w, r, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, generated.CompanyMutation{Company: companyModel(company), ChangeId: changeID})
}

func (h *Handler) archiveCompany(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "opportunities:write", true)
	if !ok {
		return
	}
	var request generated.ArchiveRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	company, changeID, err := h.database.ArchiveCompany(r.Context(), p.Actor(), r.PathValue("id"), request.ExpectedRevision)
	if err != nil {
		h.companyMutationError(w, r, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, generated.CompanyMutation{Company: companyModel(company), ChangeId: changeID})
}

func (h *Handler) listOpportunities(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	limit, cursor, archived, ok := recordPageOptions(w, r, "companyId", "stage", "kind")
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.database.ListOpportunities(r.Context(), store.OpportunityListOptions{Limit: limit,
		Cursor: cursor, IncludeArchived: archived, CompanyID: query.Get("companyId"), Stage: query.Get("stage"), Kind: query.Get("kind")})
	if err != nil {
		failStore(w, err, "list opportunities")
		return
	}
	response := generated.OpportunityPage{Items: make([]generated.OpportunityView, 0, len(page.Items)), NextCursor: nonemptyString(page.NextCursor)}
	for _, item := range page.Items {
		view, err := h.opportunityView(r, item)
		if err != nil {
			failStore(w, err, "load opportunity duplicates")
			return
		}
		response.Items = append(response.Items, view)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getOpportunity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	opportunity, err := h.database.Opportunity(r.Context(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "load opportunity")
		return
	}
	view, err := h.opportunityView(r, opportunity)
	if err != nil {
		failStore(w, err, "load opportunity duplicates")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) createOpportunity(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "opportunities:write", true)
	if !ok {
		return
	}
	var request generated.CreateOpportunityRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	compensation, valid := compensationInput(request.Compensation)
	if !valid {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Reference hours must be an exact decimal from 1 to 168.")
		return
	}
	opportunity, changeID, err := h.database.CreateOpportunity(r.Context(), p.Actor(), store.OpportunityInput{
		CompanyID: request.CompanyId, Title: request.Title, Kind: string(request.Kind),
		SourceURL: optionalString(request.SourceUrl), OriginalText: optionalString(request.OriginalText),
		Notes: optionalString(request.Notes), Stage: request.Stage,
		WorkPattern: stringValue(request.WorkPattern), LocationText: optionalString(request.LocationText),
		PostedOn: optionalString(request.PostedOn), DeadlineOn: optionalString(request.DeadlineOn),
		Compensation: compensation})
	if err != nil {
		failStore(w, err, "create opportunity")
		return
	}
	w.Header().Set("Location", "/api/v1/opportunities/"+opportunity.ID)
	writeJSON(w, http.StatusCreated, generated.OpportunityMutation{Opportunity: opportunityModel(opportunity), ChangeId: changeID})
}

func stringValue[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func stringPointer[T ~string](value *T) *string {
	if value == nil {
		return nil
	}
	converted := string(*value)
	return &converted
}

func (h *Handler) patchOpportunity(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "opportunities:write", true)
	if !ok {
		return
	}
	var request generated.PatchOpportunityRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	patch := store.OpportunityPatch{ExpectedRevision: request.ExpectedRevision,
		CompanyID: request.CompanyId, Title: request.Title, Kind: stringPointer(request.Kind),
		SourceURL: request.SourceUrl, OriginalText: request.OriginalText, Notes: request.Notes,
		Stage: request.Stage, WorkPattern: stringPointer(request.WorkPattern),
		LocationText: request.LocationText, PostedOn: request.PostedOn, DeadlineOn: request.DeadlineOn}
	if request.Compensation != nil {
		value, valid := compensationInput(request.Compensation)
		if !valid {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Reference hours must be an exact decimal from 1 to 168.")
			return
		}
		patch.Compensation = &value
	}
	opportunity, changeID, err := h.database.PatchOpportunity(r.Context(), p.Actor(), r.PathValue("id"), patch)
	if err != nil {
		h.opportunityMutationError(w, r, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, generated.OpportunityMutation{Opportunity: opportunityModel(opportunity), ChangeId: changeID})
}

func (h *Handler) archiveOpportunity(w http.ResponseWriter, r *http.Request) {
	p, ok := h.recordPrincipal(w, r, "opportunities:write", true)
	if !ok {
		return
	}
	var request generated.ArchiveRequest
	if !decodeRecordJSON(w, r, &request) {
		return
	}
	opportunity, changeID, err := h.database.ArchiveOpportunity(r.Context(), p.Actor(), r.PathValue("id"), request.ExpectedRevision)
	if err != nil {
		h.opportunityMutationError(w, r, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, generated.OpportunityMutation{Opportunity: opportunityModel(opportunity), ChangeId: changeID})
}

func (h *Handler) listChanges(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	if !rejectUnknownQuery(w, r, "limit", "cursor", "after", "entityKind") {
		return
	}
	query := r.URL.Query()
	if _, present := query["after"]; present && query.Get("cursor") != "" {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Use after or cursor, not both.")
		return
	}
	limit, after := 0, int64(0)
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Limit must be between 1 and 100.")
			return
		}
		limit = value
	}
	if raw := query.Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "after must be a non-negative sequence.")
			return
		}
		after = value
	}
	page, err := h.database.ListRecordChanges(r.Context(), store.RecordChangeOptions{
		After: after, Cursor: query.Get("cursor"), Limit: limit, EntityKind: query.Get("entityKind")})
	if err != nil {
		failStore(w, err, "list changes")
		return
	}
	response := generated.RecordChangePage{Items: make([]generated.RecordChange, 0, len(page.Items)),
		NextCursor: nonemptyString(page.NextCursor), Watermark: page.Watermark}
	for _, item := range page.Items {
		response.Items = append(response.Items, changeModel(item))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getChange(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.recordPrincipal(w, r, "opportunities:read", false); !ok {
		return
	}
	change, err := h.database.RecordChange(r.Context(), r.PathValue("id"))
	if err != nil {
		failStore(w, err, "load change")
		return
	}
	writeJSON(w, http.StatusOK, changeModel(change))
}
