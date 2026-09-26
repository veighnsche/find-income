package materialprep

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Targeted artifact rewrite (M5). An explicit owner rewrite changes
// chosen current items in one bounded Standard turn over the same
// grounded basis as drafting, and commits each validated rewrite as
// a new inspectable version of the same artifact identity. Exact
// edits (store.SaveOpportunityArtifact via the PUT endpoint) stay
// literal with zero model calls; only this path spends a turn.
//
// The turn validates the artifact payload shape with the same scope,
// bounds and claim-support rules as drafting. The owner's rewrite
// instruction is explicit owner input for this turn — like an exact
// edit, its own words are trusted — so it joins the owner grounding
// corpus alongside the cited evidence.
//
// Seam note: the musewire rewrite discipline still describes the
// legacy answer shape (questionId/texts). This producer validates
// the artifact shape against StandardRewritePurpose; the R/I-owned
// discipline and output schema need the matching artifact-shape
// update before production rewrites flow (proposed, not edited here).

const maxRewriteInstructionRunes = 2000

// RewriteItem is one chosen current artifact to rewrite.
type RewriteItem struct {
	Type            string
	ExpectedVersion int64
	CurrentContent  string
}

// ArtifactRewriteRequest is the verified-fact envelope for one
// rewrite turn: the same vacancy context and owner facts as
// drafting, plus the current texts and the owner instruction.
type ArtifactRewriteRequest struct {
	OpportunityID    string
	OpportunityTitle string
	CompanyName      string
	CheckID          string
	RouteKind        string
	RouteJudgment    string
	RouteDestination string
	Description      string
	Requirements     []ArtifactRequirement
	Documents        []ArtifactDocument
	Items            []RewriteItem
	Instruction      string
	Answered         []AnsweredFact
	Clarifications   []ClarificationFact
	SavedAnswers     []SavedAnswerFact
	CareerSources    []applicationpacks.Source
	Profile          store.Preferences
}

// ArtifactRewriter runs the single bounded rewrite turn. Production
// wires the same StandardDrafter as drafting; tests supply fakes.
type ArtifactRewriter interface {
	RewriteArtifacts(ctx context.Context, request ArtifactRewriteRequest) (DraftOutcome, error)
}

// rewriteScope validates the chosen current items: known stored
// types only, unique, each with current content to rewrite.
func rewriteScope(request ArtifactRewriteRequest) (map[string]struct{}, error) {
	if len(request.Items) == 0 || len(request.Items) > 4 {
		return nil, fmt.Errorf("%w: rewrite scope", store.ErrInvalid)
	}
	if len([]rune(request.Instruction)) > maxRewriteInstructionRunes ||
		!utf8.ValidString(request.Instruction) || strings.ContainsRune(request.Instruction, 0) {
		return nil, fmt.Errorf("%w: rewrite instruction", store.ErrInvalid)
	}
	scope := make(map[string]struct{}, len(request.Items))
	for _, item := range request.Items {
		if !validArtifactTarget(item.Type) || strings.TrimSpace(item.CurrentContent) == "" {
			return nil, fmt.Errorf("%w: rewrite item", store.ErrInvalid)
		}
		if _, dup := scope[item.Type]; dup {
			return nil, fmt.Errorf("%w: duplicate rewrite item", store.ErrInvalid)
		}
		scope[item.Type] = struct{}{}
	}
	return scope, nil
}

// rewritePrompt assembles the verified-fact rewrite prompt: the
// vacancy context, the current texts, the owner instruction, and
// the verified facts grounding every claim.
func rewritePrompt(request ArtifactRewriteRequest) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Rewrite application materials for %q at %q (check %s) from the verified facts below.\n\n",
		request.OpportunityTitle, request.CompanyName, request.CheckID)
	fmt.Fprintf(&prompt, "Verified route: kind %s, judgment %s, destination %q.\n",
		request.RouteKind, request.RouteJudgment, request.RouteDestination)
	prompt.WriteString("\nVacancy description (verified vacancy text):\n")
	if strings.TrimSpace(request.Description) == "" {
		prompt.WriteString("(no vacancy description saved)\n")
	} else {
		prompt.WriteString(request.Description + "\n")
	}
	prompt.WriteString("\nVacancy requirements (employer-stated):\n")
	if len(request.Requirements) == 0 {
		prompt.WriteString("(none stated)\n")
	}
	for _, requirement := range request.Requirements {
		fmt.Fprintf(&prompt, "- %s\n", requirement.Statement)
	}
	prompt.WriteString("\nEmployer-requested documents:\n")
	if len(request.Documents) == 0 {
		prompt.WriteString("(none stated)\n")
	}
	for _, document := range request.Documents {
		fmt.Fprintf(&prompt, "- %s (required: %v)\n", document.Label, document.Required)
	}
	prompt.WriteString("\nCurrent texts to rewrite (rewrite these types only, at most once each):\n")
	for _, item := range request.Items {
		fmt.Fprintf(&prompt, "--- %s ---\n%s\n", item.Type, item.CurrentContent)
	}
	prompt.WriteString("\nOwner instruction:\n")
	if strings.TrimSpace(request.Instruction) == "" {
		prompt.WriteString("(none: improve clarity and fit within the verified facts)\n")
	} else {
		prompt.WriteString(request.Instruction + "\n")
	}
	prompt.WriteString("\nVerified owner answers (reuse, do not contradict):\n")
	if len(request.Answered) == 0 {
		prompt.WriteString("(none)\n")
	}
	for _, fact := range request.Answered {
		fmt.Fprintf(&prompt, "- %s [%s]: %s\n", fact.Question, fact.QuestionID, fact.Text)
	}
	prompt.WriteString("\nAnswered owner clarifications (verified job-scoped facts):\n")
	if len(request.Clarifications) == 0 {
		prompt.WriteString("(none)\n")
	}
	for _, fact := range request.Clarifications {
		fmt.Fprintf(&prompt, "- %s: %s\n", fact.Prompt, fact.Text)
	}
	prompt.WriteString("\nApproved career sources (ground every claim in these bodies):\n")
	for _, source := range request.CareerSources {
		body := source.Body
		mark := ""
		if len([]rune(body)) > maxDraftCareerRunes {
			body = truncateRunes(body, maxDraftCareerRunes)
			mark = "\n[truncated to fit the turn budget]"
		}
		fmt.Fprintf(&prompt, "--- %s (%s) ---\n%s%s\n", source.ID, source.Name, body, mark)
	}
	profile := request.Profile
	fmt.Fprintf(&prompt, "\nProfile: location %q, remote %v, hybrid %v, target %d/100h, min base %d %s, timezone %q.\n",
		profile.PreferredLocation, profile.AllowRemote, profile.AllowHybrid,
		profile.TargetHoursHundredths, profile.MinMonthlyBaseCents, profile.SalaryCurrency, profile.Timezone)
	prompt.WriteString(`
Respond with ONLY this JSON object, no other text:
{"artifacts":[{"type":"...","content":"...","facts":["source-id"],"answers":["question-id"]}]}
Rules: rewrite only the listed types, at most once each; content is
the complete rewritten standalone text for that artifact
(email_subject is one subject line); facts names the career source
ids grounding the claims; answers names the answered question ids
reused; omit any type the facts cannot support and never invent
owner claims, dates, or credentials.`)
	return prompt.String()
}

// buildStandardRewriteInput packages verified facts into a private
// Standard session input. Purpose is fixed, BundleRef is the verified
// CheckID, and Targets are the chosen current types.
func buildStandardRewriteInput(request ArtifactRewriteRequest) musecode.StandardInput {
	targets := make([]string, 0, len(request.Items))
	for _, item := range request.Items {
		targets = append(targets, item.Type)
	}
	return musecode.StandardInput{
		Purpose:   StandardRewritePurpose,
		BundleRef: request.CheckID,
		Context:   map[string]string{"prompt": rewritePrompt(request)},
		Targets:   targets,
	}
}

// RewriteArtifacts rewrites the chosen current items in one bounded
// Standard turn. Validation mirrors drafting: scope, bounds,
// citation ids and per-sentence claim support, with the owner
// instruction trusted as turn input. Unsupported rewrites hold with
// the offending sentence named; structural failures fail the turn.
func (d *StandardDrafter) RewriteArtifacts(ctx context.Context, request ArtifactRewriteRequest) (DraftOutcome, error) {
	if d == nil || d.Runner == nil {
		return DraftOutcome{}, ErrUnavailable
	}
	scope, err := rewriteScope(request)
	if err != nil {
		return DraftOutcome{}, err
	}
	sources, err := draftSources(request.CareerSources)
	if err != nil {
		return DraftOutcome{}, err
	}
	answered := make(map[string]string, len(request.Answered))
	for _, fact := range request.Answered {
		if strings.TrimSpace(fact.QuestionID) == "" || strings.TrimSpace(fact.Text) == "" {
			return DraftOutcome{}, fmt.Errorf("%w: rewrite answer", store.ErrInvalid)
		}
		answered[fact.QuestionID] = fact.Text
	}
	roleTexts := []string{request.OpportunityTitle, request.CompanyName,
		request.Description, request.RouteDestination}
	for _, requirement := range request.Requirements {
		roleTexts = append(roleTexts, requirement.Statement, requirement.Excerpt)
	}
	for _, document := range request.Documents {
		roleTexts = append(roleTexts, document.Label)
	}
	owner := make([]string, 0, 2*len(request.Clarifications)+1)
	for _, fact := range request.Clarifications {
		owner = append(owner, fact.Prompt, fact.Text)
	}
	if strings.TrimSpace(request.Instruction) != "" {
		owner = append(owner, request.Instruction)
	}
	result, err := d.Runner.RunStandard(ctx, buildStandardRewriteInput(request))
	if err != nil {
		return DraftOutcome{}, err
	}
	return checkModelArtifactDrafts(scope, sources, answered, owner, corpusOf(roleTexts...), result.Messages)
}

// RewriteInput is one explicit rewrite operation: the chosen current
// items with their fenced versions plus the owner instruction.
type RewriteInput struct {
	Items       []RewriteItem
	Instruction string
}

// RewriteOpportunityArtifacts runs one explicit rewrite of chosen
// current items and commits each validated rewrite as a new version
// of the same identity. Pins come from server truth: the current
// check, answers and workflow are read (never trusted from the
// caller), each item fences on its expected version, and pins are
// confirmed again before commit. Rewriting a version whose basis is
// stale conflicts instead of papering over moved inputs; request
// keys replay committed versions exactly like drafting.
func (s *Service) RewriteOpportunityArtifacts(ctx context.Context, actor store.Actor, opportunityID, requestKey string, input RewriteInput) (store.ArtifactReadinessSet, bool, error) {
	if s == nil || s.Store == nil || s.Career == nil {
		return store.ArtifactReadinessSet{}, false, ErrUnavailable
	}
	if actor.Kind != "administrator" || actor.ID == "" || !validRequestKey(requestKey) {
		return store.ArtifactReadinessSet{}, false, store.ErrInvalid
	}
	if len(input.Items) == 0 || len(input.Items) > 4 {
		return store.ArtifactReadinessSet{}, false, store.ErrInvalid
	}
	seen := map[string]int64{}
	for _, item := range input.Items {
		if !validArtifactTarget(item.Type) || item.ExpectedVersion < 1 {
			return store.ArtifactReadinessSet{}, false, store.ErrInvalid
		}
		if _, dup := seen[item.Type]; dup {
			return store.ArtifactReadinessSet{}, false, store.ErrInvalid
		}
		seen[item.Type] = item.ExpectedVersion
	}
	if s.Rewrites == nil {
		return store.ArtifactReadinessSet{}, false, ErrUnavailable
	}
	sources, _, err := s.Career()
	if err != nil {
		return store.ArtifactReadinessSet{}, false, fmt.Errorf("materialprep: career sources: %w", err)
	}
	digests := make(map[string]string, len(sources))
	for _, source := range sources {
		if source.Approved && source.ID != "" && source.SHA256 != "" {
			digests[source.ID] = source.SHA256
		}
	}
	readiness, err := s.Store.ArtifactReadinessWithFacts(ctx, opportunityID, digests)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	if readiness.CheckStatus != store.CheckOverallChecked || readiness.CheckID == "" {
		return store.ArtifactReadinessSet{}, false, store.ErrConflict
	}
	items := make([]RewriteItem, 0, len(input.Items))
	for _, item := range input.Items {
		var found *store.ArtifactReadinessEntry
		for i := range readiness.Entries {
			if readiness.Entries[i].Type == item.Type {
				found = &readiness.Entries[i]
				break
			}
		}
		if found == nil || found.Current == nil {
			return store.ArtifactReadinessSet{}, false, store.ErrConflict
		}
		if _, err := s.Store.GetOpportunityArtifactByRequestKey(ctx, opportunityID,
			item.Type, requestKey+":"+item.Type); err == nil {
			// Lost-response retry: the key already committed.
			// Fencing and staleness are skipped; the save
			// replays the accepted version (or conflicts when
			// the retried payload actually changed).
			items = append(items, RewriteItem{Type: item.Type,
				ExpectedVersion: item.ExpectedVersion, CurrentContent: found.Current.Content})
			continue
		}
		if strings.HasPrefix(found.Reason, "outdated:") {
			return store.ArtifactReadinessSet{}, false, store.ErrConflict
		}
		if found.Current.Version != item.ExpectedVersion {
			return store.ArtifactReadinessSet{}, false, store.ErrConflict
		}
		items = append(items, RewriteItem{Type: item.Type,
			ExpectedVersion: item.ExpectedVersion, CurrentContent: found.Current.Content})
	}
	status, err := s.Store.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	workflow, err := s.Store.RoleWorkflow(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	if workflow.Stage != store.RoleStageAnswered && workflow.Stage != store.RoleStagePreparing &&
		workflow.Stage != store.RoleStagePrepared {
		return store.ArtifactReadinessSet{}, false, store.ErrConflict
	}
	resolved, err := s.verifyPins(ctx, opportunityID, status.Check.ID,
		status.Check.QuestionSetSHA256, workflow.Revision)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	saved, err := s.savedAnswerContext(ctx)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	answered := make([]AnsweredFact, 0)
	for _, question := range resolved.check.Questions {
		if value, ok := resolved.values[question.ID]; ok && value.State == store.AnswerValueStateAnswered &&
			strings.TrimSpace(value.Text) != "" {
			answered = append(answered, AnsweredFact{QuestionID: question.ID,
				Question: question.Text, Text: value.Text, AnswerVersion: value.Version})
		}
	}
	documents := make([]ArtifactDocument, 0, len(resolved.check.RequestedDocuments))
	for _, document := range resolved.check.RequestedDocuments {
		documents = append(documents, ArtifactDocument{Label: document.Label, Required: document.Required})
	}
	requirements := make([]ArtifactRequirement, 0, len(resolved.check.Requirements))
	for _, requirement := range resolved.check.Requirements {
		requirements = append(requirements, ArtifactRequirement{Statement: requirement.Statement,
			Excerpt: requirement.SourceExcerpt})
	}
	clarified, _, _, err := s.clarificationState(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	targetNames := make([]string, 0, len(items))
	for _, item := range items {
		targetNames = append(targetNames, item.Type)
	}
	if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
		store.PrepareTurnStarted, "started", map[string]any{
			"rewrite": true, "targets": targetNames,
			"instruction": truncateRunes(input.Instruction, 500)}); err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	outcome, err := s.Rewrites.RewriteArtifacts(ctx, ArtifactRewriteRequest{
		OpportunityID: resolved.opportunity.ID, OpportunityTitle: resolved.opportunity.Title,
		CompanyName: resolved.company.Name, CheckID: resolved.check.ID,
		RouteKind: resolved.check.Route.Kind, RouteJudgment: resolved.check.Route.Judgment,
		RouteDestination: resolved.check.Route.DestinationText, Description: resolved.description,
		Requirements: requirements, Documents: documents, Items: items,
		Instruction: input.Instruction, Answered: answered, Clarifications: clarified,
		SavedAnswers: saved, CareerSources: sources, Profile: resolved.profile})
	if err != nil {
		_ = s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareFailed, "error", map[string]any{"rewrite": true,
				"error": truncateRunes(err.Error(), 500)})
		return store.ArtifactReadinessSet{}, false, err
	}
	cited := make([]string, 0)
	for _, draft := range outcome.Drafts {
		cited = append(cited, draft.AnswerIDs...)
	}
	if err := s.confirmPins(ctx, opportunityID, resolved, cited); err != nil {
		_ = s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareFailed, "error", map[string]any{"rewrite": true,
				"error": truncateRunes(err.Error(), 500)})
		return store.ArtifactReadinessSet{}, false, err
	}
	byID := make(map[string]applicationpacks.Source, len(sources))
	for _, source := range sources {
		byID[source.ID] = source
	}
	created := false
	done := make(map[string]bool, len(outcome.Drafts))
	for _, draft := range outcome.Drafts {
		refs := make([]store.ArtifactAnswerRef, 0, len(draft.AnswerIDs))
		for _, id := range draft.AnswerIDs {
			refs = append(refs, store.ArtifactAnswerRef{QuestionID: id, AnswerVersion: resolved.values[id].Version})
		}
		factDigests := make(map[string]string, len(draft.FactIDs))
		for _, id := range draft.FactIDs {
			factDigests[id] = byID[id].SHA256
		}
		view, wasCreated, err := s.Store.SaveOpportunityArtifact(ctx, actor, opportunityID, store.ArtifactSaveInput{
			RequestKey: requestKey + ":" + draft.Type, ExpectedVersion: seen[draft.Type],
			Type: draft.Type, Content: draft.Content,
			Basis: store.ArtifactBasis{FactIDs: draft.FactIDs, FactSHA256: factDigests, AnswerRefs: refs,
				CheckSpans: []store.CheckSourceSpan{}, CheckID: resolved.check.ID,
				QuestionSetSHA256:   resolved.check.QuestionSetSHA256,
				OpportunityRevision: resolved.opportunity.Revision}})
		if err != nil {
			_ = s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
				store.PrepareFailed, "error", map[string]any{"rewrite": true,
					"error": truncateRunes(err.Error(), 500)})
			return store.ArtifactReadinessSet{}, false, err
		}
		done[draft.Type] = true
		created = created || wasCreated
		donePayload := map[string]any{"rewrite": true, "type": draft.Type,
			"version": view.Version, "factIds": draft.FactIDs, "answerIds": draft.AnswerIDs}
		if !wasCreated {
			donePayload["replayed"] = true
		}
		if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareArtifactDone, "ok", donePayload); err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
	}
	held := make([]string, 0, len(outcome.Held))
	for _, hold := range outcome.Held {
		held = append(held, hold.Type)
		done[hold.Type] = true
		payload := map[string]any{"rewrite": true, "type": hold.Type, "reason": hold.Reason}
		if hold.MissingFact != "" {
			payload["missingFact"] = hold.MissingFact
		}
		if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareArtifactHeld, "held", payload); err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
	}
	drafted := make([]string, 0, len(outcome.Drafts))
	for _, draft := range outcome.Drafts {
		drafted = append(drafted, draft.Type)
	}
	for _, item := range items {
		if !done[item.Type] {
			held = append(held, item.Type)
			if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
				store.PrepareArtifactHeld, "held", map[string]any{"rewrite": true,
					"type": item.Type, "reason": "omitted by turn"}); err != nil {
				return store.ArtifactReadinessSet{}, false, err
			}
		}
	}
	if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
		store.PrepareCompleted, "ok", map[string]any{"rewrite": true, "drafted": drafted, "held": held}); err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	readiness, err = s.Store.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	return readiness, created, nil
}
