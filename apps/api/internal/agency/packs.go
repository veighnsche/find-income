package agency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type PackSourceLoader interface {
	LoadPackSources(context.Context) ([]applicationpacks.Source, error)
}

type LocalPackSources struct{ ProjectRoot string }

func (local LocalPackSources) LoadPackSources(ctx context.Context) ([]applicationpacks.Source, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sources, _, err := applicationpacks.LoadApprovedCareerSources(local.ProjectRoot, []string{"cv-vince-liem.typ", "cv-vince-liem.md", "github-evidence-review.md"})
	return sources, err
}

func (e *Engine) checkPrepare(ctx context.Context) error {
	if e == nil || e.Store == nil || e.Runtime == nil || e.PackSources == nil {
		return errors.New("application preparation unavailable")
	}
	if err := e.Runtime.CheckRound(ctx, "prepare"); err != nil {
		return err
	}
	sources, err := e.PackSources.LoadPackSources(ctx)
	if err != nil || len(sources) < 3 {
		return errors.New("approved career material unavailable")
	}
	return nil
}

func (e *Engine) launchPrepare(r store.Round) error {
	if r.State != store.RoundRunning || r.Outcome != "prepare" {
		return store.ErrInvalid
	}
	return e.launchPrepareWithCorrection(r, nil)
}

// LaunchPackCorrection is the process_input dispatch seam for one exact owner
// instruction. The pack tool rechecks it before any guarded write.
func (e *Engine) LaunchPackCorrection(r store.Round, priorPackID, instructionID string) error {
	if r.State != store.RoundRunning || r.Outcome != "process_input" || priorPackID == "" || instructionID == "" ||
		!slices.Contains(r.Scope.InputRefs, "instruction:"+instructionID) {
		return store.ErrFenced
	}
	return e.launchPrepareWithCorrection(r, &packCorrectionTarget{PriorPackID: priorPackID, InstructionID: instructionID})
}

type packCorrectionTarget struct {
	PriorPackID   string
	InstructionID string
}

func (e *Engine) launchPrepareWithCorrection(r store.Round, correction *packCorrectionTarget) error {
	if _, err := packOpportunityScope(r.Scope); err != nil {
		return err
	}
	base := e.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithDeadline(base, r.Deadline)
	if err := e.checkPrepare(ctx); err != nil {
		cancel()
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]*activeWorker{}
	}
	if _, exists := e.active[r.ID]; exists {
		cancel()
		return store.ErrConflict
	}
	worker := &activeWorker{cancel: cancel, done: make(chan struct{})}
	e.active[r.ID] = worker
	go func() {
		defer cancel()
		defer e.workerDone(r.ID, worker)
		e.runPrepareWithCorrection(ctx, r, correction)
	}()
	return nil
}

func packOpportunityScope(scope store.RoundScope) (string, error) {
	if len(scope.Resources) != 2 || scope.Resources[1] != "campaign:active" ||
		!strings.HasPrefix(scope.Resources[0], "opportunity:") || len(scope.Resources[0]) <= len("opportunity:") {
		return "", store.ErrInvalid
	}
	return strings.TrimPrefix(scope.Resources[0], "opportunity:"), nil
}

type packSourceExcerpt struct {
	ID           string `json:"id"`
	SHA256       string `json:"sha256"`
	Excerpt      string `json:"excerpt,omitempty"`
	OmittedBytes int    `json:"omittedBytes"`
}

type packTurnEvidence struct {
	OpportunityID       string                  `json:"opportunityId"`
	OpportunityRevision int64                   `json:"opportunityRevision"`
	Title               string                  `json:"title"`
	Company             string                  `json:"company"`
	SourceURL           string                  `json:"sourceUrl"`
	OriginalText        string                  `json:"originalText"` // Complete saved role text; no silent truncation.
	ProfileRevision     int64                   `json:"profileRevision"`
	Preferences         packPreferences         `json:"preferences"`
	CareerSources       []packSourceExcerpt     `json:"careerSources"`
	Correction          *packCorrectionEvidence `json:"correction,omitempty"`
}

type packCorrectionEvidence struct {
	PriorPackID                      string                 `json:"priorPackId"`
	PriorVersion                     int64                  `json:"priorVersion"`
	PriorContentSHA256               string                 `json:"priorContentSha256"`
	OwnerInstructionID               string                 `json:"ownerInstructionId"`
	OwnerInstructionExpectedRevision int64                  `json:"ownerInstructionExpectedRevision"`
	OwnerInstructionText             string                 `json:"ownerInstructionText"`
	PriorDraft                       applicationpacks.Draft `json:"priorDraft"`
}

type packPreferences struct {
	PreferredLocation     string                `json:"preferredLocation"`
	AllowRemote           bool                  `json:"allowRemote"`
	AllowHybrid           bool                  `json:"allowHybrid"`
	TargetHoursHundredths int64                 `json:"targetHoursHundredths"`
	MinMonthlyBaseCents   int64                 `json:"minMonthlyBaseCents"`
	SalaryCurrency        string                `json:"salaryCurrency"`
	RoleCriteria          []store.RoleCriterion `json:"roleCriteria"`
}

// Only approved source excerpts enter the remote turn. Full source snapshots
// stay local for the pack tool, and every omission is counted explicitly.
func buildPackTurnEvidence(opportunity store.Opportunity, company store.Company, profile store.Preferences, sources []applicationpacks.Source, correction ...*packCorrectionEvidence) (string, error) {
	if opportunity.ID == "" || opportunity.Revision < 1 || opportunity.SourceURL == "" || strings.TrimSpace(opportunity.OriginalText) == "" || len(sources) < 3 {
		return "", store.ErrInvalid
	}
	if len(correction) > 1 {
		return "", store.ErrInvalid
	}
	evidence := packTurnEvidence{OpportunityID: opportunity.ID, OpportunityRevision: opportunity.Revision,
		Title: opportunity.Title, Company: company.Name, SourceURL: opportunity.SourceURL,
		OriginalText: opportunity.OriginalText, ProfileRevision: profile.Version,
		Preferences: packPreferences{PreferredLocation: profile.PreferredLocation, AllowRemote: profile.AllowRemote,
			AllowHybrid: profile.AllowHybrid, TargetHoursHundredths: profile.TargetHoursHundredths,
			MinMonthlyBaseCents: profile.MinMonthlyBaseCents, SalaryCurrency: profile.SalaryCurrency,
			RoleCriteria: profile.RoleCriteria}}
	if len(correction) == 1 {
		evidence.Correction = correction[0]
	}
	for _, source := range sources {
		limit := len(source.Body)
		if source.ID == "github-evidence-review.md" && limit > 6500 {
			limit = 6500
		}
		if source.ID == "cv-vince-liem.typ" {
			limit = 0
		}
		excerpt := prefixUTF8(source.Body, limit)
		evidence.CareerSources = append(evidence.CareerSources, packSourceExcerpt{ID: source.ID, SHA256: source.SHA256, Excerpt: excerpt, OmittedBytes: len(source.Body) - len(excerpt)})
	}
	encode := func() ([]byte, error) { return json.Marshal(evidence) }
	data, err := encode()
	if err != nil {
		return "", err
	}
	if len(data) > 30000 {
		for i := range evidence.CareerSources {
			if evidence.CareerSources[i].ID == "github-evidence-review.md" {
				original := sourceBodyByID(sources, evidence.CareerSources[i].ID)
				evidence.CareerSources[i].Excerpt = prefixUTF8(original, 2500)
				evidence.CareerSources[i].OmittedBytes = len(original) - len(evidence.CareerSources[i].Excerpt)
			}
		}
		data, err = encode()
		if err != nil {
			return "", err
		}
	}
	if len(data) > 30000 {
		return "", fmt.Errorf("%w: complete role and minimum approved career context exceed turn bound", store.ErrInvalid)
	}
	return string(data), nil
}

func sourceBodyByID(sources []applicationpacks.Source, id string) string {
	for _, source := range sources {
		if source.ID == id {
			return source.Body
		}
	}
	return ""
}

func (e *Engine) loadPackCorrection(ctx context.Context, round store.Round, target packCorrectionTarget,
	opportunity store.Opportunity, company store.Company, profile store.Preferences, sources []applicationpacks.Source) (*packCorrectionEvidence, error) {
	prior, err := e.Store.ApplicationPack(ctx, target.PriorPackID)
	if err != nil {
		return nil, err
	}
	if prior.OpportunityID != opportunity.ID || prior.OpportunityRevision != opportunity.Revision || prior.ProfileRevision != profile.Version {
		return nil, store.ErrConflict
	}
	instruction, err := e.Store.OwnerInstruction(ctx, round.Actor, target.InstructionID)
	if err != nil {
		return nil, err
	}
	if instruction.TargetKind != "application_pack" || instruction.TargetID != prior.ID ||
		instruction.ExpectedRevision != prior.Version || instruction.RevokedAt != "" ||
		!slices.Contains(round.Scope.InputRefs, "instruction:"+instruction.ID) ||
		(instruction.RoundID != "" && instruction.RoundID != round.ID) {
		return nil, store.ErrFenced
	}
	var manifest struct {
		Role    applicationpacks.Role     `json:"role"`
		Sources []applicationpacks.Source `json:"sources"`
		Draft   applicationpacks.Draft    `json:"draft"`
	}
	if err := json.Unmarshal(prior.ManifestJSON, &manifest); err != nil {
		return nil, store.ErrInvalid
	}
	role := manifest.Role
	if role.OpportunityID != opportunity.ID || role.OpportunityRevision != opportunity.Revision || role.ProfileRevision != profile.Version ||
		role.Title != opportunity.Title || role.Company != company.Name || role.SourceURL != opportunity.SourceURL ||
		role.Description != opportunity.OriginalText || !applicationpacks.SameApprovedSourceSnapshots(manifest.Sources, sources) {
		return nil, store.ErrConflict
	}
	return &packCorrectionEvidence{PriorPackID: prior.ID, PriorVersion: prior.Version, PriorContentSHA256: prior.ContentSHA256,
		OwnerInstructionID: instruction.ID, OwnerInstructionExpectedRevision: instruction.ExpectedRevision,
		OwnerInstructionText: instruction.Text, PriorDraft: manifest.Draft}, nil
}

type packReport struct {
	Code             string               `json:"code"`
	OpportunityID    string               `json:"opportunityId,omitempty"`
	PackID           string               `json:"packId,omitempty"`
	Version          int64                `json:"version,omitempty"`
	MaterialUnknowns []string             `json:"materialUnknowns,omitempty"`
	Remaining        store.RoundAllowance `json:"remaining"`
	Recommendation   *homeRecommendation  `json:"recommendation,omitempty"`
}

func (e *Engine) finishPrepare(ctx context.Context, initial store.Round, detail packReport, partial bool) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	r, err := e.Store.Round(cleanup, initial.ID)
	if err != nil || r.State != store.RoundRunning {
		return
	}
	if !time.Now().Before(r.Deadline) {
		_, _ = e.Store.ExpireRound(cleanup, r.ID)
		return
	}
	facts := outcomeRecommendationFacts{Outcome: r.Outcome, Code: detail.Code, ResultID: detail.PackID, ResultRevision: detail.Version}
	detail.Recommendation = e.computeOutcomeRecommendation(ctx, r, facts)
	if current, readErr := e.Store.Round(cleanup, r.ID); readErr == nil {
		r = current
	}
	detail.Remaining = store.RoundAllowance{Requests: r.Limits.Requests - r.Used.Requests, Items: r.Limits.Items - r.Used.Items,
		Tools: r.Limits.Tools - r.Used.Tools, Turns: r.Limits.Turns - r.Used.Turns}
	status := "complete"
	if partial {
		status = "partial"
	}
	data, _ := json.Marshal(detail)
	_, _ = e.Store.FinishRound(cleanup, initial.Actor, r.ID, store.RoundCompleted, detail.Code, status, data)
}

func (e *Engine) runPrepare(ctx context.Context, initial store.Round) {
	e.runPrepareWithCorrection(ctx, initial, nil)
}

func (e *Engine) runPrepareWithCorrection(ctx context.Context, initial store.Round, target *packCorrectionTarget) {
	detail := packReport{Code: "pack_unavailable"}
	partial := true
	defer func() {
		if !errors.Is(ctx.Err(), context.Canceled) {
			e.finishPrepare(ctx, initial, detail, partial)
		}
	}()
	r, err := e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		detail.Code = terminalCode(err)
		return
	}
	opportunityID, err := packOpportunityScope(r.Scope)
	if err != nil {
		detail.Code = "invalid_pack_scope"
		return
	}
	detail.OpportunityID = opportunityID
	opportunity, err := e.Store.Opportunity(ctx, opportunityID)
	if err != nil || opportunity.ArchivedAt != "" {
		detail.Code = "selected_role_unavailable"
		return
	}
	if err := e.requireSelectedPackOpportunity(ctx, opportunity); err != nil {
		detail.Code = "selected_role_decision_changed"
		return
	}
	if target == nil {
		if found := e.findPackForRound(ctx, r.ID, opportunityID, opportunity.Revision, nil); found.PackID != "" {
			detail = found
			partial = false
			return
		}
	}
	company, err := e.Store.Company(ctx, opportunity.CompanyID)
	if err != nil {
		detail.Code = "selected_employer_unavailable"
		return
	}
	profile, err := e.Store.CurrentPreferences(ctx)
	if err != nil || profile.Version != r.ProfileVersion {
		detail.Code = "profile_changed"
		return
	}
	sources, err := e.PackSources.LoadPackSources(ctx)
	if err != nil {
		detail.Code = "approved_career_material_unavailable"
		return
	}
	var correction *packCorrectionEvidence
	if target != nil {
		correction, err = e.loadPackCorrection(ctx, r, *target, opportunity, company, profile, sources)
		if err != nil {
			detail.Code = "pack_correction_fenced"
			return
		}
		if found := e.findPackForRound(ctx, r.ID, opportunityID, opportunity.Revision, correction); found.PackID != "" {
			detail = found
			partial = false
			return
		}
	}
	evidence, err := buildPackTurnEvidence(opportunity, company, profile, sources, correction)
	if err != nil {
		detail.Code = "role_or_career_context_unbounded"
		return
	}
	if _, err := e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion); err != nil {
		detail.Code = terminalCode(err)
		return
	}
	if err := e.requireSelectedPackOpportunity(ctx, opportunity); err != nil {
		detail.Code = "selected_role_decision_changed"
		return
	}
	brief := "Prepare one reviewable application pack for the selected saved opportunity. Use application_pack_prepare exactly once with this round capability and a fresh request key. The evidence includes the complete saved opening, current owner preferences, and approved career excerpts with exact source IDs/digests and omitted-byte counts. Quote one actual role requirement exactly from originalText and cite only exact career excerpts shown here. Distinguish dated employment from personal projects. Do not invent tenure, employment type, proficiency, deployment or current interests; follow current stated preferences instead of inferring a desired role from historic skills. Draft concise focus, cover and any supported answers. If the application destination or employer questions are absent, leave destination empty and record material unknowns; do not invent them. Do not send an application."
	turnKey := "prepare:" + opportunity.ID + ":" + fmt.Sprint(opportunity.Revision)
	if correction != nil {
		brief = "Correct the one owner-selected prior application pack identified in correction, using the saved owner instruction as the requested change. Call application_pack_prepare with correction.priorPackId and correction.ownerInstructionId exactly as supplied. Revise the prior draft, but verify every factual statement against the current approved career excerpts and cite exact source text. Keep recorded Jev relevance visible for review and allow the pack tool to reassess the revised citations. Do not choose another pack, turn an opportunity-wide note into pack authority, invent experience, or send an application."
		turnKey = "correct-pack:" + correction.PriorPackID + ":" + correction.OwnerInstructionID
	}
	_, err = e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, codexservice.RoundTurnInput{RequestKey: turnKey, ResourceID: "opportunity:" + opportunity.ID, Brief: brief, Evidence: evidence})
	if err != nil {
		detail.Code = terminalCode(err)
		return
	}
	if _, err := e.live(ctx, r.ID, initial.Generation, initial.ProfileVersion); err != nil {
		detail.Code = terminalCode(err)
		return
	}
	current, err := e.Store.Opportunity(ctx, opportunityID)
	if err != nil || current.Revision != opportunity.Revision {
		detail.Code = "selected_role_changed"
		return
	}
	if err := e.requireSelectedPackOpportunity(ctx, current); err != nil {
		detail.Code = "selected_role_decision_changed"
		return
	}
	if found := e.findPackForRound(ctx, r.ID, opportunityID, opportunity.Revision, correction); found.PackID != "" {
		detail = found
		partial = false
		return
	}
	detail.Code = "pack_not_prepared"
}

func (e *Engine) requireSelectedPackOpportunity(ctx context.Context, opportunity store.Opportunity) error {
	decision, err := e.Store.OwnerOpportunityDecision(ctx, opportunity.ID)
	if err != nil {
		return err
	}
	if decision.Decision != "selected" || decision.OpportunityRevision != opportunity.Revision {
		return store.ErrFenced
	}
	return nil
}

func (e *Engine) findPackForRound(ctx context.Context, roundID, opportunityID string, revision int64, correction *packCorrectionEvidence) packReport {
	result := packReport{Code: "pack_unavailable", OpportunityID: opportunityID}
	events, err := e.Store.RoundHistory(ctx, roundID)
	if err != nil {
		return result
	}
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Operation != store.RoundPrepareApplicationPack || event.EntityKind != "application_pack" {
			continue
		}
		pack, err := e.Store.ApplicationPack(ctx, event.EntityID)
		if err != nil || pack.OpportunityID != opportunityID || pack.OpportunityRevision != revision {
			continue
		}
		var manifest struct {
			Draft struct {
				MaterialUnknowns []string `json:"materialUnknowns"`
			} `json:"draft"`
			Correction *applicationpacks.Correction `json:"correction"`
		}
		if json.Unmarshal(pack.ManifestJSON, &manifest) != nil {
			continue
		}
		if correction != nil && (manifest.Correction == nil || manifest.Correction.PriorPackID != correction.PriorPackID ||
			manifest.Correction.PriorVersion != correction.PriorVersion || manifest.Correction.OwnerInstructionID != correction.OwnerInstructionID ||
			manifest.Correction.OwnerInstructionExpectedRevision != correction.OwnerInstructionExpectedRevision) {
			continue
		}
		return packReport{Code: "pack_ready", OpportunityID: opportunityID, PackID: pack.ID, Version: pack.Version, MaterialUnknowns: manifest.Draft.MaterialUnknowns}
	}
	return result
}
