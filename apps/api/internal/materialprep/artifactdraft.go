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
	Documents        []ArtifactDocument
	Targets          []ArtifactTarget
	Answered         []AnsweredFact
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

// ArtifactDrafter runs the single bounded artifact drafting turn. It is
// invoked at most once per draft operation, and never when no required
// type is held.
type ArtifactDrafter interface {
	DraftArtifacts(ctx context.Context, request ArtifactDraftRequest) ([]ArtifactDraft, error)
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

// artifactPrompt assembles the verified-fact artifact prompt. Route
// requirements name what the vacancy calls for; answered texts and career
// sources ground every claim.
func artifactPrompt(request ArtifactDraftRequest) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Draft application materials for %q at %q (check %s) from the verified facts below.\n\n",
		request.OpportunityTitle, request.CompanyName, request.CheckID)
	fmt.Fprintf(&prompt, "Verified route: kind %s, judgment %s, destination %q.\n",
		request.RouteKind, request.RouteJudgment, request.RouteDestination)
	prompt.WriteString("Employer-requested documents:\n")
	if len(request.Documents) == 0 {
		prompt.WriteString("(none stated)\n")
	}
	for _, document := range request.Documents {
		fmt.Fprintf(&prompt, "- %s (required: %v)\n", document.Label, document.Required)
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
// turn. It returns a subset when the model omits unsupported types (they
// stay held) and an empty slice when nothing is drafted.
func (d *StandardDrafter) DraftArtifacts(ctx context.Context, request ArtifactDraftRequest) ([]ArtifactDraft, error) {
	if d == nil || d.Runner == nil {
		return nil, ErrUnavailable
	}
	scope, err := artifactScope(request)
	if err != nil {
		return nil, err
	}
	sources, err := draftSources(request.CareerSources)
	if err != nil {
		return nil, err
	}
	answered := make(map[string]struct{}, len(request.Answered))
	for _, fact := range request.Answered {
		if strings.TrimSpace(fact.QuestionID) == "" || strings.TrimSpace(fact.Text) == "" {
			return nil, fmt.Errorf("%w: artifact answer", store.ErrInvalid)
		}
		answered[fact.QuestionID] = struct{}{}
	}
	result, err := d.Runner.RunStandard(ctx, buildStandardArtifactInput(request))
	if err != nil {
		return nil, err
	}
	return checkModelArtifactDrafts(scope, sources, answered, result.Messages)
}

// DraftOpportunityArtifacts drafts every required-but-held stored artifact
// type in one bounded Standard turn and commits each validated draft as a
// new artifact version. Types the turn omits stay held; the returned
// readiness set always reflects stored truth. No held type means no model
// call. Committed versions replay by request key; a version that appears
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
		return readiness, false, nil
	}
	if s.Artifacts == nil {
		return store.ArtifactReadinessSet{}, false, ErrUnavailable
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
	drafts, err := s.Artifacts.DraftArtifacts(ctx, ArtifactDraftRequest{
		OpportunityID: resolved.opportunity.ID, OpportunityTitle: resolved.opportunity.Title,
		CompanyName: resolved.company.Name, CheckID: resolved.check.ID,
		RouteKind: resolved.check.Route.Kind, RouteJudgment: resolved.check.Route.Judgment,
		RouteDestination: resolved.check.Route.DestinationText, Documents: documents,
		Targets: targets, Answered: answered, SavedAnswers: saved,
		CareerSources: sources, Profile: resolved.profile})
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	created := false
	for _, draft := range drafts {
		refs := make([]store.ArtifactAnswerRef, 0, len(draft.AnswerIDs))
		for _, id := range draft.AnswerIDs {
			refs = append(refs, store.ArtifactAnswerRef{QuestionID: id, AnswerVersion: resolved.values[id].Version})
		}
		_, wasCreated, err := s.Store.SaveOpportunityArtifact(ctx, actor, opportunityID, store.ArtifactSaveInput{
			RequestKey: requestKey + ":" + draft.Type, Type: draft.Type, Content: draft.Content,
			Basis: store.ArtifactBasis{FactIDs: draft.FactIDs, AnswerRefs: refs, CheckSpans: []store.CheckSourceSpan{}}})
		if err != nil {
			return store.ArtifactReadinessSet{}, false, err
		}
		created = created || wasCreated
	}
	readiness, err = s.Store.ArtifactReadiness(ctx, opportunityID)
	if err != nil {
		return store.ArtifactReadinessSet{}, false, err
	}
	return readiness, created, nil
}

// checkModelArtifactDrafts parses and validates one turn's output against
// the scope, pinned sources, and answered questions. Subsets (and the
// empty set) are accepted: omitted types stay held downstream.
func checkModelArtifactDrafts(scope map[string]struct{}, sources map[string]applicationpacks.Source, answered map[string]struct{}, messages []string) ([]ArtifactDraft, error) {
	text, err := draftPayloadText(messages)
	if err != nil {
		return nil, err
	}
	var payload artifactPayload
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid artifact response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid artifact response: trailing content")
	}
	seen := make(map[string]struct{}, len(payload.Artifacts))
	out := make([]ArtifactDraft, 0, len(payload.Artifacts))
	for _, draft := range payload.Artifacts {
		if _, ok := scope[draft.Type]; !ok {
			return nil, fmt.Errorf("artifact outside held scope: %q", draft.Type)
		}
		if _, dup := seen[draft.Type]; dup {
			return nil, fmt.Errorf("duplicate artifact: %q", draft.Type)
		}
		seen[draft.Type] = struct{}{}
		bound := maxArtifactContentRunes
		if draft.Type == store.ArtifactEmailSubject {
			bound = maxArtifactSubjectRunes
		}
		content := strings.TrimSpace(draft.Content)
		if content == "" || len([]rune(content)) > bound ||
			!utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return nil, fmt.Errorf("artifact content for %q", draft.Type)
		}
		if len(draft.FactIDs) > maxArtifactFacts || len(draft.AnswerIDs) > maxArtifactAnswers {
			return nil, fmt.Errorf("artifact basis for %q", draft.Type)
		}
		for _, id := range draft.FactIDs {
			if _, ok := sources[id]; !ok {
				return nil, fmt.Errorf("artifact fact for %q", draft.Type)
			}
		}
		for _, id := range draft.AnswerIDs {
			if _, ok := answered[id]; !ok {
				return nil, fmt.Errorf("artifact answer for %q", draft.Type)
			}
		}
		draft.Content = content
		if draft.FactIDs == nil {
			draft.FactIDs = []string{}
		}
		if draft.AnswerIDs == nil {
			draft.AnswerIDs = []string{}
		}
		out = append(out, draft)
	}
	return out, nil
}
