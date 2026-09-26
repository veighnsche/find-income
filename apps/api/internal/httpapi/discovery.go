package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Discovery and screening reads. Brief, catalog, and saved findings serve
// versioned stores; nothing here classifies or writes.
func (h *Handler) discoveryUnavailable(w http.ResponseWriter, operation string) {
	fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable,
		operation+" is unavailable until screening is implemented.")
}

func (h *Handler) getSearchBrief(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	ctx := r.Context()
	brief, err := codexservice.CurrentOwnerBrief(ctx, h.database)
	if err != nil {
		failResearch(w, err)
		return
	}
	prefs, err := h.database.PreferenceVersion(ctx, brief.ProfileVersion)
	if err != nil {
		failResearch(w, err)
		return
	}
	requirements := make([]generated.RoleCriterionView, 0, len(prefs.RoleCriteria))
	for _, c := range prefs.RoleCriteria {
		hash := c.DefinitionHash()
		requirements = append(requirements, generated.RoleCriterionView{
			DefinitionHash: &hash,
			Description:    c.Description,
			Id:             c.ID,
			Kind:           generated.RoleCriterionViewKind(c.Kind),
			Label:          c.Label,
			Mode:           generated.RoleCriterionViewMode(c.Mode),
		})
	}
	view := generated.SearchBriefView{
		ProfileVersion: brief.ProfileVersion,
		RubricVersion:  brief.RubricVersion,
		RubricSource:   brief.Source,
		Requirements:   requirements,
	}
	for _, f := range brief.Facts {
		view.Facts = append(view.Facts, struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}{Key: f.Key, Value: f.Value})
	}
	if view.Facts == nil {
		view.Facts = []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}{}
	}
	if view.Requirements == nil {
		view.Requirements = []generated.RoleCriterionView{}
	}
	if catalog, err := h.database.CurrentReasonCatalog(ctx); err == nil {
		view.CatalogVersion = &catalog.CatalogVersion
	} else if !errors.Is(err, store.ErrNotFound) {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) getReasonCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 1 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Brief version must be a positive integer.")
		return
	}
	catalog, err := h.database.ReasonCatalog(r.Context(), version)
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, generated.ReasonCatalogView{
		ProfileVersion:     catalog.ProfileVersion,
		RubricVersion:      catalog.RubricVersion,
		CatalogVersion:     catalog.CatalogVersion,
		Positive:           reasonChoiceViews(catalog.Positive),
		Negative:           reasonChoiceViews(catalog.Negative),
		MissingInformation: reasonChoiceViews(catalog.MissingInformation),
	})
}

// OwnerIdentityView is the authenticated owner behind a sourced-context
// read. The backend stores no display name, so kind/id is the full
// supported identity.
type OwnerIdentityView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// OwnerCareerSourceView is one approved private career source (CV or
// supporting career document) with its provenance. It is served
// separately from the reusable answer library: sources ground drafts and
// panels, answers never silently become wants.
type OwnerCareerSourceView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Approved bool   `json:"approved"`
	Body     string `json:"body"`
}

// SourcedOwnerContextView is the D4 owner-context read: owner identity
// plus the approved career sources with provenance. SourcesConnected is
// false when the server has no career loader wired; the owner identity
// still serves.
type SourcedOwnerContextView struct {
	Owner            OwnerIdentityView       `json:"owner"`
	SourcesConnected bool                    `json:"sourcesConnected"`
	Sources          []OwnerCareerSourceView `json:"sources"`
}

// ownerCareerLoader supplies the approved career sources through the
// existing pinned source loading. Server wiring connects it once at
// startup; nil means career sources are not connected on this server.
var ownerCareerLoader func() ([]applicationpacks.Source, error)

// SetOwnerCareerLoader connects the approved career sources read. Production
// passes a loader over applicationpacks.LoadApprovedCareerSources; tests
// swap in fakes and restore afterwards.
func SetOwnerCareerLoader(loader func() ([]applicationpacks.Source, error)) {
	ownerCareerLoader = loader
}

// getSourcedOwnerContext serves the D4 sourced owner context: approved
// CV/source experience with provenance plus the supported owner identity.
// Pure GET-only read with zero model calls: bodies serve verbatim from the
// pinned loader, and no summary extraction happens on panel reads. Route
// registration is I-owned (proposed: GET /api/v1/research/owner-context).
func (h *Handler) getSourcedOwnerContext(w http.ResponseWriter, r *http.Request) {
	p, ok := h.owner(w, r)
	if !ok {
		return
	}
	view := SourcedOwnerContextView{
		Owner:   OwnerIdentityView{Kind: p.Kind, ID: p.ID},
		Sources: []OwnerCareerSourceView{},
	}
	if ownerCareerLoader == nil {
		writeJSON(w, http.StatusOK, view)
		return
	}
	sources, err := ownerCareerLoader()
	if err != nil {
		fail(w, http.StatusServiceUnavailable, generated.ApiErrorCodeUnavailable, "Approved career sources failed to load.")
		return
	}
	view.SourcesConnected = true
	for _, source := range sources {
		view.Sources = append(view.Sources, OwnerCareerSourceView{
			ID: source.ID, Name: source.Name, SHA256: source.SHA256,
			Approved: source.Approved, Body: source.Body,
		})
	}
	writeJSON(w, http.StatusOK, view)
}

// reasonChoiceViews renders saved catalog text verbatim (never null: an
// unauthored group is an empty array, not a missing one).
func reasonChoiceViews(choices []store.ReasonChoice) []generated.ReasonChoice {
	out := make([]generated.ReasonChoice, 0, len(choices))
	for _, c := range choices {
		out = append(out, generated.ReasonChoice{Id: c.ID, Label: c.Label, Detail: c.Detail})
	}
	return out
}

func (h *Handler) listRunFindings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	limit := store.FindingDefaultListLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > store.FindingMaxListLimit {
			fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Finding limit must be 1..100.")
			return
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 512 {
		fail(w, http.StatusBadRequest, generated.ApiErrorCodeValidationError, "Invalid findings cursor.")
		return
	}
	page, err := h.database.ListRunFindings(r.Context(), id, r.URL.Query().Get("group"), cursor, limit)
	if err != nil {
		failResearch(w, err)
		return
	}
	out := generated.FindingList{Items: make([]generated.FindingEntry, 0, len(page.Items))}
	for _, f := range page.Items {
		out.Items = append(out.Items, findingEntryView(f))
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getOpportunityFinding(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.owner(w, r); !ok {
		return
	}
	f, err := h.database.GetOpportunityFinding(r.Context(), r.PathValue("id"))
	if err != nil {
		failResearch(w, err)
		return
	}
	writeJSON(w, http.StatusOK, findingEntryView(f))
}

// findingEntryView renders saved finding state verbatim: catalog reason text
// is copied byte-for-byte, jevSupport travels as the raw recorded signal
// (never a correctness claim), and no model call or write happens here.
func findingEntryView(f store.Finding) generated.FindingEntry {
	view := generated.FindingEntry{
		AssessmentId: f.AssessmentID, CatalogVersion: f.CatalogVersion,
		Group:         generated.FindingEntryGroup(f.Group),
		OpportunityId: f.OpportunityID, OpportunityRevision: f.OpportunityRevision,
		ProfileVersion: f.ProfileVersion, RubricVersion: f.RubricVersion,
		Stale: f.Stale,
	}
	view.Reasons = make([]generated.FindingReason, 0, len(f.Reasons))
	for _, reason := range f.Reasons {
		view.Reasons = append(view.Reasons, generated.FindingReason{
			Detail: reason.Detail, JevSupport: float32(reason.JevSupport),
			Kind:  generated.FindingReasonKind(reason.Kind),
			Label: reason.Label, ReasonId: reason.ReasonID,
		})
	}
	view.EvidenceLinks = make([]generated.FindingEvidenceLink, 0, len(f.EvidenceLinks))
	for _, link := range f.EvidenceLinks {
		view.EvidenceLinks = append(view.EvidenceLinks, generated.FindingEvidenceLink{
			CaptureId: link.CaptureID, ExcerptSha256: link.ExcerptSHA256,
			SpanEnd: int(link.SpanEnd), SpanStart: int(link.SpanStart),
		})
	}
	if f.Conflict != nil {
		view.Conflict = &generated.ReasonChoice{Id: f.Conflict.ID, Label: f.Conflict.Label, Detail: f.Conflict.Detail}
	}
	if f.MissingFact != nil {
		view.MissingFact = &generated.ReasonChoice{Id: f.MissingFact.ID, Label: f.MissingFact.Label, Detail: f.MissingFact.Detail}
	}
	if f.SourceRef != nil {
		view.SourceRef = &struct {
			ObservedUrl    *string `json:"observedUrl,omitempty"`
			SourceId       string  `json:"sourceId"`
			SourceRevision string  `json:"sourceRevision"`
		}{SourceId: f.SourceRef.SourceID, SourceRevision: f.SourceRef.SourceRevision}
		if f.SourceRef.ObservedURL != "" {
			view.SourceRef.ObservedUrl = &f.SourceRef.ObservedURL
		}
	}
	if f.UnknownBasis != "" {
		view.UnknownBasis = &f.UnknownBasis
	}
	if f.StaleBasis != "" {
		view.StaleBasis = &f.StaleBasis
	}
	return view
}
