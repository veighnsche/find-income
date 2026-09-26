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

// Required-answer drafting (C4/M4). Prepare drafts the required
// questions the owner explicitly flagged for drafting, from verified
// owner facts only. Optional blanks are never drafted here; questions
// the facts cannot support stay blank with their flag and a hold,
// never invented text.

// DraftQuestion is one flagged required question needing a draft.
type DraftQuestion struct {
	ID   string
	Text string
}

// RequestedAnswerDraft is one validated draft keyed by question.
type RequestedAnswerDraft struct {
	QuestionID string
	Text       string
}

// RequestedAnswerHold is one flagged question the turn could not
// support. MissingFact names the first unsupported sentence when
// claim validation held it.
type RequestedAnswerHold struct {
	QuestionID  string
	Reason      string
	MissingFact string
}

// RequestedAnswerOutcome is one turn's validated result.
type RequestedAnswerOutcome struct {
	Drafts []RequestedAnswerDraft
	Held   []RequestedAnswerHold
}

// AnswerDraftRequest is the verified-fact envelope for one drafting
// turn: the vacancy context, the flagged questions, and the verified
// facts grounding every line.
type AnswerDraftRequest struct {
	OpportunityID    string
	OpportunityTitle string
	CompanyName      string
	CheckID          string
	Description      string
	Requirements     []ArtifactRequirement
	Questions        []DraftQuestion
	Answered         []AnsweredFact
	Clarifications   []ClarificationFact
	CareerSources    []applicationpacks.Source
}

// RequestedAnswerDrafter runs the single bounded required-answer
// drafting turn. Production wires StandardDrafter; the service
// asserts it optionally so fake artifact drafters keep working.
type RequestedAnswerDrafter interface {
	DraftRequestedAnswers(ctx context.Context, request AnswerDraftRequest) (RequestedAnswerOutcome, error)
}

// maxAnswerDraftRunes bounds one drafted answer.
const maxAnswerDraftRunes = 4000

// maxAnswerDraftLines bounds the cited lines of one drafted answer.
const maxAnswerDraftLines = 40

func answerDraftScope(request AnswerDraftRequest) (map[string]string, error) {
	if len(request.Questions) == 0 || len(request.Questions) > 50 {
		return nil, fmt.Errorf("%w: answer draft scope", store.ErrInvalid)
	}
	scope := make(map[string]string, len(request.Questions))
	for _, question := range request.Questions {
		if strings.TrimSpace(question.ID) == "" || strings.TrimSpace(question.Text) == "" {
			return nil, fmt.Errorf("%w: answer draft question", store.ErrInvalid)
		}
		if _, dup := scope[question.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate answer draft question", store.ErrInvalid)
		}
		scope[question.ID] = question.Text
	}
	return scope, nil
}

func answerDraftPrompt(request AnswerDraftRequest) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Draft employer-question answers for %q at %q (check %s) from the verified facts below.\n\n",
		request.OpportunityTitle, request.CompanyName, request.CheckID)
	prompt.WriteString("Vacancy description (verified vacancy text):\n")
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
	prompt.WriteString("\nDraft exactly these questions, at most once each:\n")
	for _, question := range request.Questions {
		fmt.Fprintf(&prompt, "- %s: %s\n", question.ID, question.Text)
	}
	return prompt.String()
}

func buildStandardAnswerDraftInput(request AnswerDraftRequest) musecode.StandardInput {
	targets := make([]string, 0, len(request.Questions))
	for _, question := range request.Questions {
		targets = append(targets, question.ID)
	}
	return musecode.StandardInput{
		Purpose:   StandardDraftPurpose,
		BundleRef: request.CheckID,
		Context:   map[string]string{"prompt": answerDraftPrompt(request)},
		Targets:   targets,
	}
}

type answerDraftCitation struct {
	SourceID string `json:"sourceId"`
	Excerpt  string `json:"excerpt"`
}

type answerDraftLine struct {
	Text      string                `json:"text"`
	Citations []answerDraftCitation `json:"citations"`
}

type answerDraftItem struct {
	QuestionID string            `json:"questionId"`
	Lines      []answerDraftLine `json:"lines"`
}

type answerDraftPayload struct {
	Drafts []answerDraftItem `json:"drafts"`
}

// DraftRequestedAnswers drafts the flagged required questions in one
// bounded Standard turn. Every line needs at least one citation to a
// known approved source with a non-empty excerpt, and every drafted
// answer needs per-sentence claim support from the verified owner and
// vacancy facts. Unknown question ids and structural failures fail the
// turn; omitted or unsupported questions hold with their reason.
func (d *StandardDrafter) DraftRequestedAnswers(ctx context.Context, request AnswerDraftRequest) (RequestedAnswerOutcome, error) {
	if d == nil || d.Runner == nil {
		return RequestedAnswerOutcome{}, ErrUnavailable
	}
	scope, err := answerDraftScope(request)
	if err != nil {
		return RequestedAnswerOutcome{}, err
	}
	sources, err := draftSources(request.CareerSources)
	if err != nil {
		return RequestedAnswerOutcome{}, err
	}
	answered := make(map[string]string, len(request.Answered))
	for _, fact := range request.Answered {
		if strings.TrimSpace(fact.QuestionID) == "" || strings.TrimSpace(fact.Text) == "" {
			return RequestedAnswerOutcome{}, fmt.Errorf("%w: answer draft answer", store.ErrInvalid)
		}
		answered[fact.QuestionID] = fact.Text
	}
	result, err := d.Runner.RunStandard(ctx, buildStandardAnswerDraftInput(request))
	if err != nil {
		return RequestedAnswerOutcome{}, err
	}
	clarified := make([]string, 0, 2*len(request.Clarifications))
	for _, fact := range request.Clarifications {
		clarified = append(clarified, fact.Prompt, fact.Text)
	}
	roleTexts := []string{request.OpportunityTitle, request.CompanyName, request.Description}
	for _, requirement := range request.Requirements {
		roleTexts = append(roleTexts, requirement.Statement, requirement.Excerpt)
	}
	return checkModelAnswerDrafts(scope, sources, answered, clarified, corpusOf(roleTexts...), result.Messages)
}

func checkModelAnswerDrafts(scope map[string]string, sources map[string]applicationpacks.Source, answered map[string]string, clarified []string, role groundingSet, messages []string) (RequestedAnswerOutcome, error) {
	text, err := draftPayloadText(messages)
	if err != nil {
		return RequestedAnswerOutcome{}, err
	}
	var payload answerDraftPayload
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return RequestedAnswerOutcome{}, fmt.Errorf("invalid answer draft response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RequestedAnswerOutcome{}, fmt.Errorf("invalid answer draft response: trailing content")
	}
	seen := make(map[string]struct{}, len(payload.Drafts))
	out := RequestedAnswerOutcome{Drafts: []RequestedAnswerDraft{}, Held: []RequestedAnswerHold{}}
	for _, draft := range payload.Drafts {
		if _, ok := scope[draft.QuestionID]; !ok {
			return RequestedAnswerOutcome{}, fmt.Errorf("answer draft outside requested scope: %q", draft.QuestionID)
		}
		if _, dup := seen[draft.QuestionID]; dup {
			return RequestedAnswerOutcome{}, fmt.Errorf("duplicate answer draft: %q", draft.QuestionID)
		}
		seen[draft.QuestionID] = struct{}{}
		if len(draft.Lines) == 0 || len(draft.Lines) > maxAnswerDraftLines {
			out.Held = append(out.Held, RequestedAnswerHold{QuestionID: draft.QuestionID,
				Reason: "draft has no cited lines"})
			continue
		}
		lines := make([]string, 0, len(draft.Lines))
		owner := make([]string, 0, len(draft.Lines)+len(answered)+len(clarified))
		cited := true
		for _, line := range draft.Lines {
			trimmed := strings.TrimSpace(line.Text)
			if trimmed == "" || len(line.Citations) == 0 {
				cited = false
				break
			}
			for _, citation := range line.Citations {
				source, ok := sources[citation.SourceID]
				if !ok || strings.TrimSpace(citation.Excerpt) == "" {
					cited = false
					break
				}
				owner = append(owner, source.Body)
			}
			if !cited {
				break
			}
			lines = append(lines, trimmed)
		}
		if !cited {
			out.Held = append(out.Held, RequestedAnswerHold{QuestionID: draft.QuestionID,
				Reason: "draft line lacks a usable approved-source citation"})
			continue
		}
		for _, text := range answered {
			owner = append(owner, text)
		}
		owner = append(owner, clarified...)
		content := strings.Join(lines, "\n")
		if content == "" || len([]rune(content)) > maxAnswerDraftRunes ||
			!utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			out.Held = append(out.Held, RequestedAnswerHold{QuestionID: draft.QuestionID,
				Reason: "draft text is empty or over bounds"})
			continue
		}
		if missing := unsupportedSentence(content, corpusOf(owner...), role); missing != "" {
			out.Held = append(out.Held, RequestedAnswerHold{QuestionID: draft.QuestionID,
				Reason:      "unsupported claim: the cited evidence does not support the drafted text",
				MissingFact: truncateRunes(missing, 300)})
			continue
		}
		out.Drafts = append(out.Drafts, RequestedAnswerDraft{QuestionID: draft.QuestionID, Text: content})
	}
	for id := range scope {
		if _, ok := seen[id]; !ok {
			out.Held = append(out.Held, RequestedAnswerHold{QuestionID: id,
				Reason: "the facts did not support a draft"})
		}
	}
	return out, nil
}

// draftRequestedAnswers fills the required blanks the owner flagged
// for drafting, before artifacts target on save-time truth. Each
// validated draft saves as a standard_draft answer value and clears
// its flag; held questions keep flag and blank with a journaled
// reason. Without flagged blanks this is a no-op with no model call.
func (s *Service) draftRequestedAnswers(ctx context.Context, actor store.Actor, opportunityID string, resolved *pinned) error {
	flagged := make([]DraftQuestion, 0)
	versions := make(map[string]int64, 0)
	for _, question := range resolved.check.Questions {
		if question.Required != store.CheckRequired {
			continue
		}
		value, ok := resolved.values[question.ID]
		if !ok || strings.TrimSpace(value.Text) != "" || !value.DraftRequested {
			continue
		}
		flagged = append(flagged, DraftQuestion{ID: question.ID, Text: question.Text})
		versions[question.ID] = value.Version
	}
	if len(flagged) == 0 {
		return nil
	}
	drafter, ok := s.Artifacts.(RequestedAnswerDrafter)
	if !ok || drafter == nil {
		for _, question := range flagged {
			if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
				store.PrepareArtifactHeld, "held", map[string]any{"requestedDraft": question.ID,
					"reason": "answer drafting unavailable"}); err != nil {
				return err
			}
		}
		return nil
	}
	sources, _, err := s.Career()
	if err != nil {
		return fmt.Errorf("materialprep: career sources: %w", err)
	}
	answered := make([]AnsweredFact, 0)
	for _, question := range resolved.check.Questions {
		if value, ok := resolved.values[question.ID]; ok && value.State == store.AnswerValueStateAnswered &&
			strings.TrimSpace(value.Text) != "" {
			answered = append(answered, AnsweredFact{QuestionID: question.ID,
				Question: question.Text, Text: value.Text, AnswerVersion: value.Version})
		}
	}
	clarified, _, _, err := s.clarificationState(ctx, opportunityID)
	if err != nil {
		return err
	}
	requirements := make([]ArtifactRequirement, 0, len(resolved.check.Requirements))
	for _, requirement := range resolved.check.Requirements {
		requirements = append(requirements, ArtifactRequirement{Statement: requirement.Statement,
			Excerpt: requirement.SourceExcerpt})
	}
	outcome, err := drafter.DraftRequestedAnswers(ctx, AnswerDraftRequest{
		OpportunityID: resolved.opportunity.ID, OpportunityTitle: resolved.opportunity.Title,
		CompanyName: resolved.company.Name, CheckID: resolved.check.ID,
		Description: resolved.description, Requirements: requirements, Questions: flagged,
		Answered: answered, Clarifications: clarified, CareerSources: sources})
	if err != nil {
		return err
	}
	for _, draft := range outcome.Drafts {
		saved, err := s.Store.SaveAnswerValue(ctx, actor, opportunityID, draft.QuestionID,
			store.AnswerValueSaveInput{ExpectedAnswerVersion: versions[draft.QuestionID],
				Text: draft.Text, Origin: store.AnswerValueOriginStandardDraft})
		if err != nil {
			return err
		}
		resolved.values[draft.QuestionID] = saved
		if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareArtifactDone, "ok", map[string]any{"requestedDraft": draft.QuestionID,
				"version": saved.Version}); err != nil {
			return err
		}
	}
	for _, held := range outcome.Held {
		if err := s.recordPrepare(ctx, actor, opportunityID, resolved.check.ID,
			store.PrepareArtifactHeld, "held", map[string]any{"requestedDraft": held.QuestionID,
				"reason": held.Reason, "missingFact": held.MissingFact}); err != nil {
			return err
		}
	}
	return nil
}
