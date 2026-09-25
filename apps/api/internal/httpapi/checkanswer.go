package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/musewire"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Check/question/answer reads and writes. Saved checks (D1), the answer
// library (E1), Jev matching (E2, in answermatch.go), and exact answer
// values (E3) are live.
func failCheck(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role is not selected.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Opportunity or check not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid check request.")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrRoundIdempotencyConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Check state changed; refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Check request failed.")
	}
}

func checkViewModel(value store.CheckView) generated.CheckView {
	model := generated.CheckView{Id: value.ID, OpportunityId: value.OpportunityID,
		OpportunityRevision: value.OpportunityRevision, WorkflowRevision: value.WorkflowRevision,
		Status: generated.CheckViewStatus(value.Status), CreatedAt: recordedTime(value.CreatedAt),
		QuestionSetSha256: value.QuestionSetSHA256, QuestionSetVersion: value.QuestionSetVersion,
		CompletedAt: optionalTime(value.CompletedAt)}
	model.CreatedBy.ActorKind, model.CreatedBy.ActorId = value.CreatedBy.Kind, value.CreatedBy.ID
	if value.ActivityCursor != "" {
		model.ActivityCursor = &value.ActivityCursor
	}
	if value.BlockedReason != nil {
		model.BlockedReason = &generated.CheckBlockedReason{
			Code: generated.CheckBlockedReasonCode(value.BlockedReason.Code), Detail: value.BlockedReason.Detail}
	}
	model.Vacancy = generated.CheckVacancy{CaptureIds: value.Vacancy.CaptureIDs,
		EvidenceSourceIds: value.Vacancy.EvidenceSourceIDs,
		Completeness:      generated.CheckVacancyCompleteness(value.Vacancy.Completeness),
		SourceUrl:         value.Vacancy.SourceURL, RetrievedAt: recordedTime(value.Vacancy.RetrievedAt)}
	if model.Vacancy.CaptureIds == nil {
		model.Vacancy.CaptureIds = []string{}
	}
	if model.Vacancy.EvidenceSourceIds == nil {
		model.Vacancy.EvidenceSourceIds = []string{}
	}
	model.RequestedDocuments = []generated.RequestedDocument{}
	for _, document := range value.RequestedDocuments {
		model.RequestedDocuments = append(model.RequestedDocuments, generated.RequestedDocument{
			Label: document.Label, Required: document.Required, SourceExcerpt: document.SourceExcerpt,
			SourceSpan: generated.CheckSourceSpan{CaptureId: document.SourceSpan.CaptureID,
				Start: document.SourceSpan.Start, End: document.SourceSpan.End}})
	}
	model.Route = generated.CheckRoute{Judgment: generated.CheckRouteJudgment(value.Route.Judgment),
		SourceExcerpt: value.Route.SourceExcerpt, ObservedAt: recordedTime(value.Route.ObservedAt)}
	if value.Route.RouteID != "" {
		model.Route.RouteId = &value.Route.RouteID
	}
	if value.Route.Kind != "" {
		kind := generated.CheckRouteKind(value.Route.Kind)
		model.Route.Kind = &kind
	}
	if value.Route.DestinationText != "" {
		model.Route.DestinationText = &value.Route.DestinationText
	}
	model.Gaps = []generated.CheckGap{}
	for _, gap := range value.Gaps {
		model.Gaps = append(model.Gaps, generated.CheckGap{Id: gap.ID,
			Description: gap.Description, Consequential: gap.Consequential,
			Kind: generated.CheckGapKind(gap.Kind)})
	}
	model.Questions = []generated.CheckQuestion{}
	for _, question := range value.Questions {
		item := generated.CheckQuestion{Id: question.ID, CheckId: question.CheckID,
			Ordinal: question.Ordinal, Text: question.Text,
			Required: generated.CheckQuestionRequired(question.Required),
			SourceSpan: generated.CheckSourceSpan{CaptureId: question.SourceSpan.CaptureID,
				Start: question.SourceSpan.Start, End: question.SourceSpan.End},
			SourceExcerpt: question.SourceExcerpt, TextSha256: question.TextSHA256}
		if question.Kind != "" {
			kind := generated.CheckQuestionKind(question.Kind)
			item.Kind = &kind
		}
		model.Questions = append(model.Questions, item)
	}
	return model
}

func checkStatusModel(value store.CheckStatusView) generated.CheckStatusView {
	model := generated.CheckStatusView{Status: generated.CheckStatusViewStatus(value.Status)}
	if value.Check != nil {
		view := checkViewModel(*value.Check)
		model.Check = &view
	}
	return model
}

func checkActivitySummary(kind, outcome string) string {
	switch kind {
	case store.CheckActivityStarted:
		return "Check started."
	case store.CheckActivityCompleted:
		return "Check completed."
	case store.CheckActivityBlocked:
		return "Check blocked."
	}
	if outcome == "" {
		return kind + "."
	}
	return kind + " (" + outcome + ")."
}

func (h *Handler) startOpportunityCheck(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var input store.CheckStartInput
	if !decodeRecordJSON(w, r, &input) {
		return
	}
	value, created, err := h.database.StartJobCheck(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), input)
	if err != nil {
		failCheck(w, err)
		return
	}
	if h.museCheck != nil && h.museCheck.Authorized() && value.Status == store.CheckStatusChecking {
		if _, err := h.museCheck.PerformCheck(r.Context(), r.PathValue("id"), value.ID); err != nil {
			if !errors.Is(err, musewire.ErrCheckNotMuse) {
				failCheck(w, err)
				return
			}
		} else if current, err := h.database.CurrentJobCheck(r.Context(), r.PathValue("id")); err != nil {
			failCheck(w, err)
			return
		} else {
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, checkStatusModel(current))
			return
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, checkStatusModel(store.CheckStatusView{Status: value.Status, Check: &value}))
}

func (h *Handler) getCurrentOpportunityCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	value, err := h.database.CurrentJobCheck(r.Context(), r.PathValue("id"))
	if err != nil {
		failCheck(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkStatusModel(value))
}

func (h *Handler) listOpportunityCheckActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid activity request.")
			return
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 512 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid activity request.")
		return
	}
	events, next, err := h.database.ListCheckActivity(r.Context(), r.PathValue("id"), cursor, limit)
	if err != nil {
		failCheck(w, err)
		return
	}
	page := generated.CheckActivityPage{Events: []generated.ResearchActivityEvent{}}
	for _, event := range events {
		item := generated.ResearchActivityEvent{EventId: event.ID, At: event.RecordedAt,
			Kind: event.Kind, Summary: checkActivitySummary(event.Kind, string(event.Outcome))}
		if event.CaptureID != "" || event.ObservationID != "" {
			item.Refs = &struct {
				AssessmentId  *string `json:"assessmentId,omitempty"`
				CaptureId     *string `json:"captureId,omitempty"`
				ObservationId *string `json:"observationId,omitempty"`
				RecordId      *string `json:"recordId,omitempty"`
			}{}
			if event.CaptureID != "" {
				item.Refs.CaptureId = &event.CaptureID
			}
			if event.ObservationID != "" {
				item.Refs.ObservationId = &event.ObservationID
			}
		}
		page.Events = append(page.Events, item)
	}
	if next != "" {
		page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) getOpportunityCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	value, err := h.database.GetJobCheck(r.Context(), r.PathValue("id"), r.PathValue("checkId"))
	if err != nil {
		failCheck(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkViewModel(value))
}

func failAnswer(w http.ResponseWriter, err error, action string) {
	if errors.Is(err, store.ErrRoundIdempotencyConflict) {
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "The request key was already used with different input.")
		return
	}
	failStore(w, err, action)
}

func savedAnswerModel(value store.SavedAnswer) generated.SavedAnswer {
	versions := make([]generated.SavedAnswerVersion, 0, len(value.Versions))
	for _, version := range value.Versions {
		item := generated.SavedAnswerVersion{Version: version.Version, Text: version.Text,
			TextSha256: version.TextSHA256, ApprovedAt: recordedTime(version.ApprovedAt),
			ApprovalRequestKey: version.ApprovalRequestKey}
		item.ApprovedBy.ActorKind = version.ApprovedBy.ActorKind
		item.ApprovedBy.ActorId = version.ApprovedBy.ActorID
		if version.ChangeNote != "" {
			note := version.ChangeNote
			item.ChangeNote = &note
		}
		if version.Supersedes != nil {
			supersedes := *version.Supersedes
			item.Supersedes = &supersedes
		}
		if len(version.SourceRefs) > 0 {
			refs := make([]generated.SavedAnswerSourceRef, 0, len(version.SourceRefs))
			for _, ref := range version.SourceRefs {
				entry := generated.SavedAnswerSourceRef{Kind: generated.SavedAnswerSourceRefKind(ref.Kind), Ref: ref.Ref}
				if ref.Excerpt != "" {
					excerpt := ref.Excerpt
					entry.Excerpt = &excerpt
				}
				refs = append(refs, entry)
			}
			item.SourceRefs = &refs
		}
		versions = append(versions, item)
	}
	return generated.SavedAnswer{Id: value.ID, CurrentVersion: value.CurrentVersion,
		ScopeTags: value.ScopeTags, ContextNote: nonemptyString(value.ContextNote), Versions: versions}
}

func (h *Handler) listSavedAnswers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	if !rejectUnknownQuery(w, r, "limit", "cursor", "scope") {
		return
	}
	query := r.URL.Query()
	limit := 0
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Limit must be between 1 and 100.")
			return
		}
		limit = value
	}
	page, err := h.database.ListSavedAnswers(r.Context(), store.SavedAnswerListOptions{
		Limit: limit, Cursor: query.Get("cursor"), Scope: query.Get("scope")})
	if err != nil {
		failAnswer(w, err, "list answers")
		return
	}
	response := generated.SavedAnswerList{Items: make([]generated.SavedAnswer, 0, len(page.Items)),
		NextCursor: nonemptyString(page.NextCursor)}
	for _, item := range page.Items {
		response.Items = append(response.Items, savedAnswerModel(item))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) createSavedAnswer(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.SavedAnswerCreate
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	input := store.SavedAnswerCreateInput{RequestKey: body.RequestKey, Text: body.Text,
		ScopeTags: body.ScopeTags, ContextNote: optionalString(body.ContextNote)}
	if body.SourceRefs != nil {
		for _, ref := range *body.SourceRefs {
			input.SourceRefs = append(input.SourceRefs, store.SavedAnswerSourceRef{
				Kind: string(ref.Kind), Ref: ref.Ref, Excerpt: optionalString(ref.Excerpt)})
		}
	}
	value, created, err := h.database.CreateSavedAnswer(r.Context(), store.Actor{Kind: p.Kind, ID: p.ID}, input)
	if err != nil {
		failAnswer(w, err, "approve answer")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, savedAnswerModel(value))
}

func (h *Handler) getSavedAnswer(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	value, err := h.database.SavedAnswer(r.Context(), r.PathValue("answerId"))
	if err != nil {
		failAnswer(w, err, "load answer")
		return
	}
	writeJSON(w, http.StatusOK, savedAnswerModel(value))
}

func (h *Handler) approveSavedAnswerVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.SavedAnswerVersionCreate
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	value, created, err := h.database.ApproveSavedAnswerVersion(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("answerId"), store.SavedAnswerVersionCreateInput{
			RequestKey: body.RequestKey, ExpectedVersion: body.ExpectedVersion,
			Text: body.Text, ChangeNote: optionalString(body.ChangeNote)})
	if err != nil {
		failAnswer(w, err, "approve answer version")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, savedAnswerModel(value))
}

func (h *Handler) getCurrentQuestionAnswers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	value, err := h.database.CurrentQuestionAnswers(r.Context(), r.PathValue("id"))
	if err != nil {
		failAnswerValue(w, err)
		return
	}
	writeJSON(w, http.StatusOK, questionAnswerListModel(value))
}

func (h *Handler) saveQuestionAnswer(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.AnswerValueSave
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	opportunityID := r.PathValue("id")
	check, err := h.database.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		failAnswerValue(w, err)
		return
	}
	if check.Check == nil {
		failConflict(w, "check_not_started")
		return
	}
	if check.Status != string(store.CheckOverallChecked) {
		failConflict(w, "check_not_complete")
		return
	}
	value, err := h.database.SaveAnswerValue(ctx,
		store.Actor{Kind: p.Kind, ID: p.ID}, opportunityID, r.PathValue("questionId"),
		store.AnswerValueSaveInput{ExpectedAnswerVersion: body.ExpectedAnswerVersion, Text: body.Text})
	if err != nil {
		failAnswerValue(w, err)
		return
	}
	writeJSON(w, http.StatusOK, questionAnswerValueModel(value))
}

func failAnswerValue(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected), errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or question not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid answer request.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "The answer changed. Refresh and reconcile your edits.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Answer request failed.")
	}
}

func questionAnswerValueModel(value store.QuestionAnswerValue) generated.QuestionAnswerValue {
	model := generated.QuestionAnswerValue{QuestionId: value.QuestionID,
		QuestionTextSha256: value.QuestionTextSHA256,
		Required:           generated.QuestionAnswerValueRequired(value.Required),
		Version:            value.Version,
		State:              generated.QuestionAnswerValueState(value.State),
		Text:               value.Text,
		UpdatedAt:          recordedTime(value.UpdatedAt)}
	if value.TextSHA256 != "" {
		model.TextSha256 = &value.TextSHA256
	}
	model.Provenance.Origin = generated.QuestionAnswerValueProvenanceOrigin(value.Provenance.Origin)
	model.Provenance.EditedAt = recordedTime(value.Provenance.EditedAt)
	model.Provenance.EditedBy.ActorKind = value.Provenance.EditedBy.Kind
	model.Provenance.EditedBy.ActorId = value.Provenance.EditedBy.ID
	if value.Provenance.MatchRunID != "" {
		model.Provenance.MatchId = &value.Provenance.MatchRunID
	}
	if value.Provenance.MatchChoice != nil {
		choice := generated.AnswerMatchChoice{}
		choice.AnswerId = &value.Provenance.MatchChoice.AnswerID
		choice.AnswerVersion = &value.Provenance.MatchChoice.AnswerVersion
		choice.TextSha256 = &value.Provenance.MatchChoice.AnswerTextSHA256
		model.Provenance.MatchChoice = &choice
	}
	return model
}

func questionAnswerListModel(value store.QuestionAnswerList) generated.QuestionAnswerList {
	model := generated.QuestionAnswerList{CheckId: value.CheckID,
		QuestionSetSha256: value.QuestionSetSHA256, Values: []generated.QuestionAnswerValue{}}
	for _, item := range value.Values {
		model.Values = append(model.Values, questionAnswerValueModel(item))
	}
	return model
}
