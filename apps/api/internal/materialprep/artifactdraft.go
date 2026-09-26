package materialprep

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ClarifyWorkArtifact names artifact work in clarification refs: the
// affected work id is the artifact type, so one answered clarification
// resumes exactly its dependent items.
const ClarifyWorkArtifact = "artifact"

// Route-dependent artifact drafting (A5). One bounded Standard turn drafts
// the required-but-held artifact types for a checked role: cv, cover
// letter, and the email subject/body pair when the verified route calls
// for them. form_values is never drafted; it derives from saved answers.
//
// Grounding mirrors answer drafting: the prompt carries verified state
// only (route requirements, answered texts, library versions, pinned
// career sources, profile scalars), and validation fences scope (listed
// types only, at most once each), content bounds, and citation ids
// against the pinned sources and answered questions. Omitted types stay
// held downstream; the turn never invents owner facts to fill a gap.

const (
	// StandardArtifactPurpose selects grounded artifact drafting.
	StandardArtifactPurpose = "prepare-draft-artifacts"

	maxArtifactSubjectRunes = 500
	maxArtifactContentRunes = 32768
	maxArtifactFacts        = 12
	maxArtifactAnswers      = 24
)

// ArtifactTarget is one required held type needing a draft with its
// readiness basis citation.
type ArtifactTarget struct {
	Type  string
	Basis string
}

// ArtifactDocument is one employer-requested document label.
type ArtifactDocument struct {
	Label    string
	Required bool
}

// ArtifactRequirement is one verified vacancy requirement statement.
type ArtifactRequirement struct {
	Statement string
	Excerpt   string
}

// ArtifactQuestion is one actual employer field: its identity,
// requiredness and text/upload kind.
type ArtifactQuestion struct {
	ID       string
	Text     string
	Required string
	Kind     string
}

// ClarificationFact is one answered owner clarification: verified
// job-scoped context. Open questions never reach a drafting turn.
type ClarificationFact struct {
	Prompt string
	Text   string
}

// ArtifactDraftRequest is the verified-fact envelope for one artifact
// drafting turn. It never carries employer contact handles, credentials,
// or send authority.
type ArtifactDraftRequest struct {
	OpportunityID    string
	OpportunityTitle string
	CompanyName      string
	CheckID          string
	RouteKind        string
	RouteJudgment    string
	RouteDestination string
	Description      string
	Requirements     []ArtifactRequirement
	Questions        []ArtifactQuestion
	Documents        []ArtifactDocument
	Targets          []ArtifactTarget
	Answered         []AnsweredFact
	Clarifications   []ClarificationFact
	SavedAnswers     []SavedAnswerFact
	CareerSources    []applicationpacks.Source
	Profile          store.Preferences
}

// ArtifactDraft is one validated artifact text with its pinned basis.
type ArtifactDraft struct {
	Type      string   `json:"type"`
	Content   string   `json:"content"`
	FactIDs   []string `json:"facts"`
	AnswerIDs []string `json:"answers"`
}

// ArtifactHold is one target the turn could not support: either the
// model omitted it or validation rejected its draft. MissingFact names
// the first unsupported sentence when claim validation held it, so the
// owner (or a focused clarification) sees exactly what is missing.
type ArtifactHold struct {
	Type        string
	Reason      string
	MissingFact string
}

// DraftOutcome is one turn's validated result: the drafts to commit
// plus the per-type holds to journal. A nil Held entry never appears;
// structural turn failures return an error instead.
type DraftOutcome struct {
	Drafts []ArtifactDraft
	Held   []ArtifactHold
}

// ArtifactDrafter runs the single bounded artifact drafting turn. It is
// invoked at most once per draft operation, and never when no required
// type is held.
type ArtifactDrafter interface {
	DraftArtifacts(ctx context.Context, request ArtifactDraftRequest) (DraftOutcome, error)
}

// artifactPayload is the exact accepted model output shape. Unknown fields
// are rejected so smuggled content fails loudly.
type artifactPayload struct {
	Artifacts []ArtifactDraft `json:"artifacts"`
}

func validArtifactTarget(artifactType string) bool {
	switch artifactType {
	case store.ArtifactCV, store.ArtifactCoverLetter, store.ArtifactEmailSubject, store.ArtifactEmailBody:
		return true
	default:
		return false
	}
}

// artifactScope validates the required-held target set: known stored
// types only, unique, each with a cited basis.
func artifactScope(request ArtifactDraftRequest) (map[string]struct{}, error) {
	if len(request.Targets) == 0 || len(request.Targets) > 4 {
		return nil, fmt.Errorf("%w: artifact scope", store.ErrInvalid)
	}
	scope := make(map[string]struct{}, len(request.Targets))
	for _, target := range request.Targets {
		if !validArtifactTarget(target.Type) || strings.TrimSpace(target.Basis) == "" {
			return nil, fmt.Errorf("%w: artifact target", store.ErrInvalid)
		}
		if _, dup := scope[target.Type]; dup {
			return nil, fmt.Errorf("%w: duplicate artifact target", store.ErrInvalid)
		}
		scope[target.Type] = struct{}{}
	}
	return scope, nil
}

// artifactPrompt assembles the verified-fact artifact prompt: the full
// relevant vacancy context (description, requirements, route, actual
// fields), the target set, and the verified owner facts (answered
// texts, clarifications, library versions, career sources) grounding
// every claim.
func artifactPrompt(request ArtifactDraftRequest) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Draft application materials for %q at %q (check %s) from the verified facts below.\n\n",
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
	prompt.WriteString("\nActual employer fields (text values versus uploads):\n")
	if len(request.Questions) == 0 {
		prompt.WriteString("(vacancy states no employer questions)\n")
	}
	for _, question := range request.Questions {
		fmt.Fprintf(&prompt, "- %s [%s/%s/%s]\n", question.Text, question.ID, question.Required, question.Kind)
	}
	prompt.WriteString("\nArtifact types needing drafts (draft these types only, at most once each):\n")
	for _, target := range request.Targets {
		fmt.Fprintf(&prompt, "- %s: %s\n", target.Type, target.Basis)
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
	prompt.WriteString("\nApproved saved answers:\n")
	if len(request.SavedAnswers) == 0 {
		prompt.WriteString("(none)\n")
	}
	savedRunes := 0
	for _, fact := range request.SavedAnswers {
		entry := fmt.Sprintf("- %s [tags: %s] [context: %s]: %s\n",
			fact.AnswerID, strings.Join(fact.ScopeTags, ", "), fact.ContextNote, fact.Text)
		if savedRunes+len([]rune(entry)) > maxDraftSavedRunes {
			prompt.WriteString("[saved answers truncated to fit the turn budget]\n")
			break
		}
		savedRunes += len([]rune(entry))
		prompt.WriteString(entry)
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
	for _, criterion := range profile.RoleCriteria {
		fmt.Fprintf(&prompt, "- criterion %s [%s/%s]: %s — %s\n",
			criterion.ID, criterion.Kind, criterion.Mode, criterion.Label, criterion.Description)
	}
	prompt.WriteString(`
Respond with ONLY this JSON object, no other text:
{"artifacts":[{"type":"...","content":"...","facts":["source-id"],"answers":["question-id"]}]}
Rules: draft only the listed types, at most once each; content is complete
standalone text for that artifact (email_subject is one subject line);
facts names the career source ids grounding the claims; answers names the
answered question ids reused; omit any type the facts cannot support and
never invent owner claims, dates, or credentials.`)
	return prompt.String()
}

// buildStandardArtifactInput packages verified facts into a private
// Standard session input. Purpose is fixed, BundleRef is the verified
// CheckID, and Targets are the in-scope artifact types.
func buildStandardArtifactInput(request ArtifactDraftRequest) musecode.StandardInput {
	targets := make([]string, 0, len(request.Targets))
	for _, target := range request.Targets {
		targets = append(targets, target.Type)
	}
	return musecode.StandardInput{
		Purpose:   StandardArtifactPurpose,
		BundleRef: request.CheckID,
		Context:   map[string]string{"prompt": artifactPrompt(request)},
		Targets:   targets,
	}
}

// DraftArtifacts drafts the in-scope held types in one bounded Standard
// turn. It returns the validated drafts plus per-type holds: omitted
// types stay held downstream, and drafts whose claims the cited
// evidence cannot support are held with the unsupported sentence
// named instead of committed. Structural turn failures (bad shape,
// out-of-scope or duplicate types, unknown citation ids) fail the
// whole turn: the output is untrustworthy as a set.
func (d *StandardDrafter) DraftArtifacts(ctx context.Context, request ArtifactDraftRequest) (DraftOutcome, error) {
	if d == nil || d.Runner == nil {
		return DraftOutcome{}, ErrUnavailable
	}
	scope, err := artifactScope(request)
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
			return DraftOutcome{}, fmt.Errorf("%w: artifact answer", store.ErrInvalid)
		}
		answered[fact.QuestionID] = fact.Text
	}
	result, err := d.Runner.RunStandard(ctx, buildStandardArtifactInput(request))
	if err != nil {
		return DraftOutcome{}, err
	}
	clarified := make([]string, 0, 2*len(request.Clarifications))
	for _, fact := range request.Clarifications {
		clarified = append(clarified, fact.Prompt, fact.Text)
	}
	return checkModelArtifactDrafts(scope, sources, answered, clarified, roleCorpus(request), result.Messages)
}

// roleCorpus collects the vacancy-side grounding context: role,
// company, description, requirements, document labels and route
// destination. It grounds names the vacancy itself supplies; owner
// claims never ground against it.
func roleCorpus(request ArtifactDraftRequest) groundingSet {
	texts := []string{request.OpportunityTitle, request.CompanyName,
		request.Description, request.RouteDestination}
	for _, requirement := range request.Requirements {
		texts = append(texts, requirement.Statement, requirement.Excerpt)
	}
	for _, document := range request.Documents {
		texts = append(texts, document.Label)
	}
	return corpusOf(texts...)
}

// DraftOpportunityArtifacts runs one explicit Prepare: it drafts every
// required-but-held stored artifact type in one bounded Standard turn
// over complete vacancy context and verified facts only, then commits
// each validated draft as a new artifact version pinned to the
// consumed inputs. Types the turn omits or validation rejects stay
// held with their reason; items awaiting an open owner clarification
// are skipped without spending a turn, and answered clarifications
// resume only their dependent items. Pins are compared before
// generation and again before commit, so a concurrent input change
// cannot commit a fresh-looking stale draft. The returned readiness
// set always reflects stored truth. No held type means no model call.
// Committed versions replay by request key; a version that appears
// between the readiness read and the save conflicts honestly.
func (s *Service) DraftOpportunityArtifacts(ctx context.Context, actor store.Actor, opportunityID, requestKey string, expectedCheckID, expectedQuestionSetSHA256 string, expectedWorkflowRevision int64) (store.ArtifactReadinessSet, bool, error) {
	if s == nil || s.Store == nil || s.Career == nil {
		return store.ArtifactReadinessSet{}, false, ErrUnavailable
	}
	if actor.Kind != "administrator" || actor.ID == "" || !validRequestKey(requestKey) {
		return store.ArtifactReadinessSet{}, false, store.ErrInvalid
	}
	resolved, err := s.verifyPins(ctx, opportunityID, expectedCheckID, expectedQuestionSetSHA256, expectedWorkflowRevision)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	if err := s.draftRequestedAnswers(ctx, actor, opportunityID, &resolved); err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	readiness, err := s.Store.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	targets := make([]ArtifactTarget, 0, 4)
	for _, entry := range readiness.Entries {
		if entry.Required && entry.State == store.ArtifactStateHeld && entry.Type != store.ArtifactFormValues {
			targets = append(targets, ArtifactTarget{Type: entry.Type, Basis: entry.Basis})
		}
	}
	if len(targets) == 0 {
		if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareCompleted, "ok", map[string]any{"drafted": []string{}, "reason": "no held types"}); err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
		return readiness, false, nil
	}
	if s.Artifacts == nil {
		return store.ArtifactReadinessSet{}, false, ErrUnavailable
	}
	clarified, openHolds, resumed, err := s.clarificationState(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	kept := targets[:0]
	waiting := make([]string, 0)
	for _, target := range targets {
		if clarificationID, ok := openHolds[target.Type]; ok {
			waiting = append(waiting, target.Type)
			if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
				store.PrepareArtifactHeld, "held", map[string]any{"type": target.Type,
					"reason": "awaiting owner clarification", "clarificationId": clarificationID}); err != nil {
				return store.ArtifactReadinessSet{}, false, err
			}
			continue
		}
		kept = append(kept, target)
	}
	targets = kept
	if len(targets) == 0 {
		if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareCompleted, "ok", map[string]any{"drafted": []string{},
				"held": waiting, "reason": "awaiting owner clarification"}); err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
		return readiness, false, nil
	}
	sources, _, err := s.Career()
	if err != nil {
		return store.ArtifactReadinessSet{}, false, fmt.Errorf("materialprep: career sources: %w", err)
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
	questions := make([]ArtifactQuestion, 0, len(resolved.check.Questions))
	for _, question := range resolved.check.Questions {
		questions = append(questions, ArtifactQuestion{ID: question.ID, Text: question.Text,
			Required: question.Required, Kind: question.Kind})
	}
	targetNames := make([]string, 0, len(targets))
	for _, target := range targets {
		targetNames = append(targetNames, target.Type)
	}
	factIDs := make([]string, 0, len(sources))
	for _, source := range sources {
		factIDs = append(factIDs, source.ID)
	}
	answerIDs := make([]string, 0, len(answered))
	for _, fact := range answered {
		answerIDs = append(answerIDs, fact.QuestionID)
	}
	if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
		store.PrepareTurnStarted, "started", map[string]any{
			"targets": targetNames, "factIds": factIDs, "answerIds": answerIDs,
			"clarifications": len(clarified), "savedAnswers": len(saved)}); err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	outcome, err := s.Artifacts.DraftArtifacts(ctx, ArtifactDraftRequest{
		OpportunityID: resolved.opportunity.ID, OpportunityTitle: resolved.opportunity.Title,
		CompanyName: resolved.company.Name, CheckID: resolved.check.ID,
		RouteKind: resolved.check.Route.Kind, RouteJudgment: resolved.check.Route.Judgment,
		RouteDestination: resolved.check.Route.DestinationText, Description: resolved.description,
		Requirements: requirements, Questions: questions, Documents: documents,
		Targets: targets, Answered: answered, Clarifications: clarified,
		SavedAnswers: saved, CareerSources: sources, Profile: resolved.profile})
	if err != nil {
		_ = s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareFailed, "error", map[string]any{"error": truncateRunes(err.Error(), 500)})
		return store.ArtifactReadinessSet{}, false, err
	}
	cited := make([]string, 0)
	for _, draft := range outcome.Drafts {
		cited = append(cited, draft.AnswerIDs...)
	}
	if err := s.confirmPins(ctx, opportunityID, resolved, cited); err != nil {
		_ = s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareFailed, "error", map[string]any{"error": truncateRunes(err.Error(), 500)})
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
		digests := make(map[string]string, len(draft.FactIDs))
		for _, id := range draft.FactIDs {
			digests[id] = byID[id].SHA256
		}
		view, wasCreated, err := s.Store.SaveOpportunityArtifact(ctx, actor, opportunityID, store.ArtifactSaveInput{
			RequestKey: requestKey + ":" + draft.Type, Type: draft.Type, Content: draft.Content,
			Basis: store.ArtifactBasis{FactIDs: draft.FactIDs, FactSHA256: digests, AnswerRefs: refs,
				CheckSpans: []store.CheckSourceSpan{}, CheckID: resolved.check.ID,
				QuestionSetSHA256:   resolved.check.QuestionSetSHA256,
				OpportunityRevision: resolved.opportunity.Revision}})
		if err != nil {
			_ = s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
				store.PrepareFailed, "error", map[string]any{"error": truncateRunes(err.Error(), 500)})
			return store.ArtifactReadinessSet{}, false, err
		}
		done[draft.Type] = true
		created = created || wasCreated
		payload := map[string]any{"type": draft.Type,
			"version": view.Version, "factIds": draft.FactIDs, "answerIds": draft.AnswerIDs}
		if !wasCreated {
			payload["replayed"] = true
		}
		if from, ok := resumed[draft.Type]; ok {
			payload["resumedFrom"] = from
		}
		if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareArtifactDone, "ok", payload); err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
	}
	held := make([]string, 0, len(outcome.Held))
	for _, hold := range outcome.Held {
		held = append(held, hold.Type)
		done[hold.Type] = true
		payload := map[string]any{"type": hold.Type, "reason": hold.Reason}
		if hold.MissingFact != "" {
			payload["missingFact"] = hold.MissingFact
		}
		if clarificationID, opened := s.clarifyMissingFact(ctx, actor, opportunityID, resolved, openHolds, hold); opened {
			payload["clarificationId"] = clarificationID
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
	for _, target := range targets {
		if !done[target.Type] {
			held = append(held, target.Type)
			if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
				store.PrepareArtifactHeld, "held", map[string]any{"type": target.Type,
					"reason": "omitted by turn"}); err != nil {
				return store.ArtifactReadinessSet{}, false, err
			}
		}
	}
	held = append(held, waiting...)
	if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
		store.PrepareCompleted, "ok", map[string]any{"drafted": drafted, "held": held}); err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	readiness, err = s.Store.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	return readiness, created, nil
}

// recordPrepare journals one prepare activity entry. Payload must stay
// small and JSON-encodable; failures propagate on success paths and are
// ignored by failure paths (which keep their original error).
func (s *Service) recordPrepare(ctx context.Context, actor store.Actor, opportunityID, checkID, kind, outcome string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.Store.RecordPrepareActivity(ctx, actor, opportunityID, store.PrepareActivityInput{
		CheckID: checkID, Kind: kind, Outcome: outcome, Payload: raw})
	return err
}

// checkModelArtifactDrafts parses and validates one turn's output against
// the scope, pinned sources, answered questions and vacancy context.
// Subsets (and the empty set) are accepted: omitted types stay held
// downstream. Citation ids must name real pinned inputs, and every
// cited claim must survive grounding against the evidence text behind
// the citations: ids alone never suffice. Unsupported drafts hold
// with the offending sentence named instead of failing the set, so
// one invented claim cannot sink the supported drafts beside it.
func checkModelArtifactDrafts(scope map[string]struct{}, sources map[string]applicationpacks.Source, answered map[string]string, clarified []string, role groundingSet, messages []string) (DraftOutcome, error) {
	text, err := draftPayloadText(messages)
	if err != nil {
		return DraftOutcome{}, err
	}
	var payload artifactPayload
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return DraftOutcome{}, fmt.Errorf("invalid artifact response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return DraftOutcome{}, fmt.Errorf("invalid artifact response: trailing content")
	}
	seen := make(map[string]struct{}, len(payload.Artifacts))
	out := DraftOutcome{Drafts: []ArtifactDraft{}, Held: []ArtifactHold{}}
	for _, draft := range payload.Artifacts {
		if _, ok := scope[draft.Type]; !ok {
			return DraftOutcome{}, fmt.Errorf("artifact outside held scope: %q", draft.Type)
		}
		if _, dup := seen[draft.Type]; dup {
			return DraftOutcome{}, fmt.Errorf("duplicate artifact: %q", draft.Type)
		}
		seen[draft.Type] = struct{}{}
		bound := maxArtifactContentRunes
		if draft.Type == store.ArtifactEmailSubject {
			bound = maxArtifactSubjectRunes
		}
		content := strings.TrimSpace(draft.Content)
		if content == "" || len([]rune(content)) > bound ||
			!utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return DraftOutcome{}, fmt.Errorf("artifact content for %q", draft.Type)
		}
		if len(draft.FactIDs) > maxArtifactFacts || len(draft.AnswerIDs) > maxArtifactAnswers {
			return DraftOutcome{}, fmt.Errorf("artifact basis for %q", draft.Type)
		}
		for _, id := range draft.FactIDs {
			if _, ok := sources[id]; !ok {
				return DraftOutcome{}, fmt.Errorf("artifact fact for %q", draft.Type)
			}
		}
		for _, id := range draft.AnswerIDs {
			if _, ok := answered[id]; !ok {
				return DraftOutcome{}, fmt.Errorf("artifact answer for %q", draft.Type)
			}
		}
		owner := make([]string, 0, len(draft.FactIDs)+len(draft.AnswerIDs)+len(clarified))
		for _, id := range draft.FactIDs {
			owner = append(owner, sources[id].Body)
		}
		for _, id := range draft.AnswerIDs {
			owner = append(owner, answered[id])
		}
		// Answered clarifications are verified job-scoped facts
		// supplied to the turn; they ground claims even though the
		// fixed output shape has no citation slot for them.
		owner = append(owner, clarified...)
		if missing := unsupportedSentence(content, corpusOf(owner...), role); missing != "" {
			out.Held = append(out.Held, ArtifactHold{Type: draft.Type,
				Reason:      "unsupported claim: the cited evidence does not support the drafted text",
				MissingFact: truncateRunes(missing, 300)})
			continue
		}
		draft.Content = content
		if draft.FactIDs == nil {
			draft.FactIDs = []string{}
		}
		if draft.AnswerIDs == nil {
			draft.AnswerIDs = []string{}
		}
		out.Drafts = append(out.Drafts, draft)
	}
	return out, nil
}
