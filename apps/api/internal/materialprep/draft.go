package materialprep

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Shared draft validation for the Standard production path (standard.go):
// scope-fenced question ids, no duplicates, 1-8 cited lines per draft,
// exact excerpts against the cited career source bodies, and the pack
// line bounds. The service layer re-validates scope and the pack builder
// re-verifies citation-exactness before anything is rendered or committed.
// Omitted questions stay held; the drafter never invents facts to fill
// a gap. The verified-fact prompt (draftPrompt) carries saved state only:
// no employer contact handles, credentials, or send authority exist in
// the request envelope, so none can reach the turn.

const (
	// maxDraftQuestions mirrors the pack answer bound: more unset required
	// questions fail honestly instead of truncating scope silently.
	maxDraftQuestions = 12
	// maxDraftCareerRunes and maxDraftSavedRunes bound the verified-fact
	// context per turn. Truncation is marked in-prompt; excerpts still
	// validate against the full pinned bodies, so a truncated fact can
	// only be omitted (held), never mis-cited.
	maxDraftCareerRunes = 12000
	maxDraftSavedRunes  = 20000
	// maxDraftLineBytes, maxDraftCitations, and maxDraftAnswerBytes mirror
	// the pack ValidateInput bounds the drafts must later satisfy.
	maxDraftLineBytes   = 1200
	maxDraftCitations   = 5
	maxDraftAnswerBytes = 2000
)

// draftPayload is the exact accepted model output shape. Unknown fields are
// rejected so smuggled content fails loudly instead of slipping through.
type draftPayload struct {
	Drafts []RequiredDraft `json:"drafts"`
}

// draftScope validates the required+unset set: non-blank unique ids and
// questions, within the pack answer bound.
func draftScope(request DraftRequest) (map[string]struct{}, error) {
	if len(request.RequiredUnset) == 0 || len(request.RequiredUnset) > maxDraftQuestions {
		return nil, fmt.Errorf("%w: draft scope", store.ErrInvalid)
	}
	scope := make(map[string]struct{}, len(request.RequiredUnset))
	for _, question := range request.RequiredUnset {
		if strings.TrimSpace(question.ID) == "" || strings.TrimSpace(question.Text) == "" {
			return nil, fmt.Errorf("%w: draft question", store.ErrInvalid)
		}
		if _, dup := scope[question.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate draft question", store.ErrInvalid)
		}
		scope[question.ID] = struct{}{}
	}
	return scope, nil
}

// draftSources indexes the pinned approved career sources by id.
func draftSources(sources []applicationpacks.Source) (map[string]applicationpacks.Source, error) {
	byID := make(map[string]applicationpacks.Source, len(sources))
	for _, source := range sources {
		if strings.TrimSpace(source.ID) == "" || !source.Approved {
			return nil, fmt.Errorf("%w: draft source", store.ErrInvalid)
		}
		if _, dup := byID[source.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate draft source", store.ErrInvalid)
		}
		byID[source.ID] = source
	}
	return byID, nil
}

// draftPrompt assembles the verified-fact prompt. It carries saved state
// only: no employer contact handles, credentials, or send authority exist
// in the request envelope, so none can reach the turn.
func draftPrompt(request DraftRequest) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Draft answers for %q at %q (check %s) from the verified facts below.\n\n",
		request.OpportunityTitle, request.CompanyName, request.CheckID)
	prompt.WriteString("Questions needing drafts (draft these ids only):\n")
	for _, question := range request.RequiredUnset {
		fmt.Fprintf(&prompt, "- %s: %s\n", question.ID, question.Text)
	}
	prompt.WriteString("\nVerified owner answers (reuse, do not contradict):\n")
	if len(request.Answered) == 0 {
		prompt.WriteString("(none)\n")
	}
	for _, fact := range request.Answered {
		fmt.Fprintf(&prompt, "- %s: %s\n", fact.Question, fact.Text)
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
	prompt.WriteString("\nApproved career sources (cite these bodies exactly):\n")
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
{"drafts":[{"questionId":"...","lines":[{"text":"...","citations":[{"sourceId":"...","excerpt":"..."}]}]}]}
Rules: draft only the listed question ids, at most once each; 1-8 lines per
draft; every line carries 1-5 citations; every excerpt is copied byte-exact
from the cited career source body above; omit any question the facts cannot
support.`)
	return prompt.String()
}

// draftPayloadText joins the collected messages and extracts the JSON
// payload: either the raw object or a single fenced block. Anything else is
// rejected so prose around the payload fails loudly.
func draftPayloadText(messages []string) (string, error) {
	trimmed := strings.TrimSpace(strings.Join(messages, "\n"))
	if trimmed == "" {
		return "", fmt.Errorf("empty draft response")
	}
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	_, rest, _ := strings.Cut(trimmed[3:], "\n")
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", fmt.Errorf("unclosed draft code fence")
	}
	return strings.TrimSpace(rest[:end]), nil
}

func validDraftText(text string, max int) bool {
	return strings.TrimSpace(text) != "" && len(text) <= max &&
		utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

// checkModelDrafts parses and validates one turn's output against the
// scope and the pinned source bodies. Subsets (and the empty set) are
// accepted: omitted questions stay held downstream.
func checkModelDrafts(scope map[string]struct{}, sources map[string]applicationpacks.Source, messages []string) ([]RequiredDraft, error) {
	text, err := draftPayloadText(messages)
	if err != nil {
		return nil, err
	}
	var payload draftPayload
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid draft response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid draft response: trailing content")
	}
	seen := make(map[string]struct{}, len(payload.Drafts))
	out := make([]RequiredDraft, 0, len(payload.Drafts))
	for _, draft := range payload.Drafts {
		if _, ok := scope[draft.QuestionID]; !ok {
			return nil, fmt.Errorf("draft outside required+unset scope: %q", draft.QuestionID)
		}
		if _, dup := seen[draft.QuestionID]; dup {
			return nil, fmt.Errorf("duplicate draft: %q", draft.QuestionID)
		}
		seen[draft.QuestionID] = struct{}{}
		if len(draft.Lines) == 0 || len(draft.Lines) > 8 {
			return nil, fmt.Errorf("draft line count for %q", draft.QuestionID)
		}
		answerLength := 0
		for _, line := range draft.Lines {
			if !validDraftText(line.Text, maxDraftLineBytes) {
				return nil, fmt.Errorf("draft text for %q", draft.QuestionID)
			}
			answerLength += len(line.Text)
			if len(line.Citations) == 0 || len(line.Citations) > maxDraftCitations {
				return nil, fmt.Errorf("draft citations for %q", draft.QuestionID)
			}
			for _, citation := range line.Citations {
				source, ok := sources[citation.SourceID]
				if !ok || !validDraftText(citation.Excerpt, maxDraftLineBytes) ||
					!strings.Contains(source.Body, citation.Excerpt) {
					return nil, fmt.Errorf("draft citation for %q", draft.QuestionID)
				}
			}
		}
		if answerLength > maxDraftAnswerBytes {
			return nil, fmt.Errorf("draft answer too long for %q", draft.QuestionID)
		}
		out = append(out, draft)
	}
	return out, nil
}
