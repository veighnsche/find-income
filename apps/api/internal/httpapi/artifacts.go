package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Per-type artifact operations (A4 contract). The readiness set maps the
// verified route to required/held/not-required/unresolved states; exact
// edits persist literally without a model call. form_values is derived at
// read time and rejected on write.
func (h *Handler) draftOpportunityArtifacts(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	if h.materials == nil {
		h.materialUnavailable(w, "Artifact drafting")
		return
	}
	var body generated.MaterialPrepareRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	set, created, err := h.materials.DraftOpportunityArtifacts(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), body.RequestKey,
		body.ExpectedCheckId, body.ExpectedQuestionSetSha256, body.ExpectedWorkflowRevision)
	if err != nil {
		failArtifactDraft(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, artifactReadinessSetModel(set))
}

func (h *Handler) listPrepareActivity(w http.ResponseWriter, r *http.Request) {
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
	events, next, err := h.database.ListPrepareActivity(r.Context(), r.PathValue("id"), cursor, limit)
	if err != nil {
		failArtifact(w, err)
		return
	}
	page := generated.CheckActivityPage{Events: []generated.ResearchActivityEvent{}}
	for _, event := range events {
		page.Events = append(page.Events, generated.ResearchActivityEvent{EventId: event.ID,
			At: event.RecordedAt, Kind: event.Kind,
			Summary: prepareActivitySummary(event.Kind, string(event.Outcome), event.Payload)})
	}
	if next != "" {
		page.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, page)
}

// artifactFactDigests maps approved career source ids to current
// digests for freshness reads. A nil loader or load failure yields a
// nil map, which skips the fact check without failing the read.
func artifactFactDigests() map[string]string {
	if ownerCareerLoader == nil {
		return nil
	}
	sources, err := ownerCareerLoader()
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(sources))
	for _, source := range sources {
		if source.Approved && source.ID != "" && source.SHA256 != "" {
			out[source.ID] = source.SHA256
		}
	}
	return out
}

func (h *Handler) listArtifactReadiness(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	set, err := h.database.ArtifactReadinessWithFacts(r.Context(), r.PathValue("id"), artifactFactDigests())
	if err != nil {
		failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifactReadinessSetModel(set))
}

func (h *Handler) getArtifactReadiness(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	artifactType := r.PathValue("artifactType")
	set, err := h.database.ArtifactReadinessWithFacts(r.Context(), r.PathValue("id"), artifactFactDigests())
	if err != nil {
		failArtifact(w, err)
		return
	}
	for _, entry := range set.Entries {
		if entry.Type == artifactType {
			writeJSON(w, http.StatusOK, artifactReadinessEntryModel(entry))
			return
		}
	}
	fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Unknown artifact type.")
}

// rewriteOpportunityArtifact runs one explicit targeted rewrite of a
// stored artifact type (M5; proposed route POST
// /opportunities/{id}/artifacts/{artifactType}/rewrite, owned by the
// integration lane). Pins come from server truth; the body carries
// the request key, the fenced expected version and the owner
// instruction.
func (h *Handler) rewriteOpportunityArtifact(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	rewriter, ok := h.materials.(MaterialRewriter)
	if h.materials == nil || !ok {
		h.materialUnavailable(w, "Artifact rewrite")
		return
	}
	var body generated.MaterialRewriteRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	instruction := ""
	if body.Instruction != nil {
		instruction = *body.Instruction
	}
	set, created, err := rewriter.RewriteOpportunityArtifacts(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), body.RequestKey,
		materialprep.RewriteInput{
			Items:       []materialprep.RewriteItem{{Type: r.PathValue("artifactType"), ExpectedVersion: body.ExpectedVersion}},
			Instruction: instruction,
		})
	if err != nil {
		failArtifactDraft(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, artifactReadinessSetModel(set))
}

// listArtifactVersions reads every stored version of one artifact
// type, oldest first (M6; proposed route GET
// /opportunities/{id}/artifacts/{artifactType}/versions). Passive:
// no model call, no write, no transition.
func (h *Handler) listArtifactVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	artifactType := r.PathValue("artifactType")
	if !storedArtifactType(artifactType) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Unknown stored artifact type.")
		return
	}
	views, err := h.database.ListArtifactVersions(r.Context(), r.PathValue("id"), artifactType)
	if err != nil {
		failArtifact(w, err)
		return
	}
	items := make([]generated.ArtifactView, 0, len(views))
	for _, view := range views {
		items = append(items, artifactViewModel(view))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// exportOpportunityArtifact downloads one artifact version rendered
// from the actual stored content (M6; proposed route GET
// /opportunities/{id}/artifacts/{artifactType}/export with optional
// ?version=N, default current). form_values exports the derived
// field text. Passive: no model call, no write, no transition, and
// downloading never means applied.
func (h *Handler) exportOpportunityArtifact(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	if !rejectUnknownQuery(w, r, "version") {
		return
	}
	opportunityID := r.PathValue("id")
	artifactType := r.PathValue("artifactType")
	version := int64(0)
	if raw := r.URL.Query().Get("version"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid artifact version.")
			return
		}
		version = parsed
	}
	opportunity, err := h.database.Opportunity(r.Context(), opportunityID)
	if err != nil {
		failArtifact(w, err)
		return
	}
	if opportunity.ArchivedAt != "" {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or artifact not found.")
		return
	}
	company, err := h.database.Company(r.Context(), opportunity.CompanyID)
	if err != nil {
		failArtifact(w, err)
		return
	}
	if artifactType == store.ArtifactFormValues {
		if version != 0 {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Form values are derived live and have no versions.")
			return
		}
		set, err := h.database.ArtifactReadinessWithFacts(r.Context(), opportunityID, artifactFactDigests())
		if err != nil {
			failArtifact(w, err)
			return
		}
		for _, entry := range set.Entries {
			if entry.Type != store.ArtifactFormValues {
				continue
			}
			export := materialprep.ExportFormValues(opportunity.Title, company.Name, set.CheckID, entry.FormValues)
			writeExport(w, export)
			return
		}
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or artifact not found.")
		return
	}
	if !storedArtifactType(artifactType) {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Unknown artifact type.")
		return
	}
	views, err := h.database.ListArtifactVersions(r.Context(), opportunityID, artifactType)
	if err != nil {
		failArtifact(w, err)
		return
	}
	pick := -1
	for i := range views {
		if version == 0 && (pick < 0 || views[i].Version > views[pick].Version) {
			pick = i
		}
		if version != 0 && views[i].Version == version {
			pick = i
		}
	}
	if pick < 0 {
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or artifact not found.")
		return
	}
	writeExport(w, materialprep.ExportArtifact(views[pick], opportunity.Title, company.Name))
}

// listSavedJobs reads the durable saved-job/artifact index (M6;
// proposed route GET /saved-jobs). Passive: no model call, no write,
// no transition.
func (h *Handler) listSavedJobs(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	entries, err := h.database.ListSavedJobs(r.Context())
	if err != nil {
		failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

// getOpportunityHandoff reads the manual Handoff basis for one role
// (M6; proposed route GET /opportunities/{id}/handoff). Passive: no
// model call, no write, and no terminal transition — saving the
// Handoff is a separate explicit write. Opening, copying or
// downloading never means applied.
func (h *Handler) getOpportunityHandoff(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	view, err := h.database.HandoffProjection(r.Context(), r.PathValue("id"))
	if err != nil {
		failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// storedArtifactType reports whether an artifact type has stored
// versions. form_values derives at read time and is never stored.
func storedArtifactType(artifactType string) bool {
	switch artifactType {
	case store.ArtifactCV, store.ArtifactCoverLetter, store.ArtifactEmailSubject, store.ArtifactEmailBody:
		return true
	default:
		return false
	}
}

// writeExport sends one rendered download with its filename and
// media type. Filenames are server-generated and already safe.
func writeExport(w http.ResponseWriter, export materialprep.Export) {
	w.Header().Set("Content-Type", export.MediaType)
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(export.Filename))
	if export.ContentSHA256 != "" {
		w.Header().Set("X-Content-SHA256", export.ContentSHA256)
	}
	_, _ = w.Write([]byte(export.Body))
}

func (h *Handler) saveOpportunityArtifact(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok || !h.mutationAllowed(w, r, p) {
		return
	}
	var body generated.ArtifactSaveRequest
	if !decodeRecordJSON(w, r, &body) {
		return
	}
	view, created, err := h.database.SaveOpportunityArtifact(r.Context(),
		store.Actor{Kind: p.Kind, ID: p.ID}, r.PathValue("id"), store.ArtifactSaveInput{
			RequestKey: body.RequestKey, ExpectedVersion: body.ExpectedVersion,
			Type: r.PathValue("artifactType"), Content: body.Content, Basis: parseArtifactBasis(body.Basis),
		})
	if err != nil {
		failArtifact(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, artifactViewModel(view))
}

// prepareActivitySummary renders one journal entry as owner-readable text
// naming the facts, answers, produced items, or errors behind it.
func prepareActivitySummary(kind, outcome string, payload json.RawMessage) string {
	var detail map[string]any
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &detail)
	}
	stringsOf := func(key string) []string {
		out := []string{}
		list, _ := detail[key].([]any)
		for _, item := range list {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	join := func(values []string) string {
		if len(values) == 0 {
			return "none"
		}
		return strings.Join(values, ", ")
	}
	rewrite, _ := detail["rewrite"].(bool)
	verb := "Drafting"
	if rewrite {
		verb = "Rewriting"
	}
	switch kind {
	case store.PrepareTurnStarted:
		if rewrite {
			return "Rewriting " + join(stringsOf("targets")) + "."
		}
		return "Drafting " + join(stringsOf("targets")) + " from facts " +
			join(stringsOf("factIds")) + " and answers " + join(stringsOf("answerIds")) + "."
	case store.PrepareArtifactDone:
		name, _ := detail["type"].(string)
		saved := "Saved "
		if rewrite {
			saved = "Rewrote "
		}
		return saved + name + " naming facts " + join(stringsOf("factIds")) +
			" and answers " + join(stringsOf("answerIds")) + "."
	case store.PrepareArtifactHeld:
		name, _ := detail["type"].(string)
		if reason, _ := detail["reason"].(string); reason != "" && reason != "omitted by turn" {
			if id, _ := detail["clarificationId"].(string); id != "" {
				return "Held " + name + ": " + reason + " (question " + id + ")."
			}
			return "Held " + name + ": " + reason + "."
		}
		return "Held " + name + ": omitted by the drafting turn."
	case store.PrepareCompleted:
		return verb + " finished: ready " + join(stringsOf("drafted")) +
			"; held " + join(stringsOf("held")) + "."
	case store.PrepareFailed:
		message, _ := detail["error"].(string)
		if message == "" {
			message = outcome
		}
		return verb + " failed: " + message
	default:
		if outcome == "" {
			return kind + "."
		}
		return kind + " (" + outcome + ")."
	}
}

func failArtifactDraft(w http.ResponseWriter, err error) {
	if errors.Is(err, materialprep.ErrUnavailable) {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Artifact drafting is unavailable.")
		return
	}
	failArtifact(w, err)
}

func failArtifact(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrRoleNotSelected), errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, generated.ApiErrorCodeNotFound, "Role, check, or artifact not found.")
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid artifact request.")
	case errors.Is(err, store.ErrConflict):
		fail(w, http.StatusConflict, generated.ApiErrorCodeConflict, "Artifact state changed; refresh before retrying.")
	default:
		fail(w, http.StatusInternalServerError, generated.ApiErrorCodeInternalError, "Artifact request failed.")
	}
}

func parseArtifactBasis(basis *generated.ArtifactBasis) store.ArtifactBasis {
	out := store.ArtifactBasis{FactIDs: []string{}, AnswerRefs: []store.ArtifactAnswerRef{}, CheckSpans: []store.CheckSourceSpan{}}
	if basis == nil {
		return out
	}
	out.FactIDs = append(out.FactIDs, basis.FactIds...)
	for _, ref := range basis.AnswerRefs {
		out.AnswerRefs = append(out.AnswerRefs, store.ArtifactAnswerRef{QuestionID: ref.QuestionId, AnswerVersion: ref.AnswerVersion})
	}
	for _, span := range basis.CheckSpans {
		out.CheckSpans = append(out.CheckSpans, store.CheckSourceSpan{CaptureID: span.CaptureId, Start: span.Start, End: span.End})
	}
	return out
}

func artifactBasisModel(value store.ArtifactBasis) generated.ArtifactBasis {
	model := generated.ArtifactBasis{FactIds: []string{},
		AnswerRefs: []generated.ArtifactAnswerRef{}, CheckSpans: []generated.CheckSourceSpan{}}
	model.FactIds = append(model.FactIds, value.FactIDs...)
	for _, ref := range value.AnswerRefs {
		model.AnswerRefs = append(model.AnswerRefs, generated.ArtifactAnswerRef{QuestionId: ref.QuestionID, AnswerVersion: ref.AnswerVersion})
	}
	for _, span := range value.CheckSpans {
		model.CheckSpans = append(model.CheckSpans, generated.CheckSourceSpan{CaptureId: span.CaptureID, Start: span.Start, End: span.End})
	}
	return model
}

func artifactViewModel(value store.ArtifactView) generated.ArtifactView {
	model := generated.ArtifactView{Id: value.ID, OpportunityId: value.OpportunityID,
		Type: generated.ArtifactViewType(value.Type), Version: value.Version,
		Content: value.Content, Basis: artifactBasisModel(value.Basis), CreatedAt: recordedTime(value.CreatedAt)}
	model.CreatedBy.ActorKind = value.CreatedBy.Kind
	model.CreatedBy.ActorId = value.CreatedBy.ID
	return model
}

func artifactReadinessEntryModel(value store.ArtifactReadinessEntry) generated.ArtifactReadinessEntry {
	model := generated.ArtifactReadinessEntry{Type: generated.ArtifactReadinessEntryType(value.Type),
		Required: value.Required, State: generated.ArtifactReadinessEntryState(value.State), Reason: value.Reason}
	if value.Basis != "" {
		basis := value.Basis
		model.Basis = &basis
	}
	if value.Current != nil {
		current := artifactViewModel(*value.Current)
		model.Current = &current
	}
	if value.Type == store.ArtifactFormValues && value.FormValues != nil {
		values := make([]generated.ArtifactFormValue, 0, len(value.FormValues))
		for _, item := range value.FormValues {
			values = append(values, generated.ArtifactFormValue{QuestionId: item.QuestionID,
				QuestionText: item.QuestionText, Required: generated.ArtifactFormValueRequired(item.Required),
				Kind: item.Kind, State: item.State, Text: item.Text})
		}
		model.FormValues = &values
	}
	return model
}

func artifactReadinessSetModel(value store.ArtifactReadinessSet) generated.ArtifactReadinessSet {
	model := generated.ArtifactReadinessSet{OpportunityId: value.OpportunityID,
		CheckStatus: value.CheckStatus, Entries: []generated.ArtifactReadinessEntry{}}
	if value.CheckID != "" {
		checkID := value.CheckID
		model.CheckId = &checkID
	}
	for _, entry := range value.Entries {
		model.Entries = append(model.Entries, artifactReadinessEntryModel(entry))
	}
	return model
}
