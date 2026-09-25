package materialprep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Explicit Standard rewrite (E09). RewriteOpportunityMaterials is the SOLE
// caller of the rewrite turn and of store.RewriteOpportunityMaterials: no
// hook, retry, or background path invokes either, so a rewrite only happens
// when the owner explicitly requests it and only the reviewed result is
// committed. The turn runs through the shared Standard primitive behind the
// Service's Draft collaborator (see standardRewriteRunner in standard.go),
// so drafting and rewrite share one Standard entry point. The service
// performs no research, capture, fetch, contact, or send: the Standard input
// carries saved state only.

// RewriteInstructions is the trusted rewrite discipline, composed into the
// rewrite prompt ahead of the owner instruction, current texts, and verified
// facts. It carries no facts, prompts, or authority: only the rewrite rules.
const RewriteInstructions = `Rewrite each listed employer-question answer according to the owner instruction, using only the verified facts in this prompt. Never invent experience, dates, credentials, or availability, and never contact anyone or browse. Every listed question id gets exactly one text; use the empty string for any question the facts cannot support.`

const (
	// maxRewriteInstructionRunes bounds the owner rewrite instruction,
	// measured in runes so multibyte text is never split mid-character.
	maxRewriteInstructionRunes = 2000
	// maxRewriteTextBytes mirrors the store rewrite bound: each rewritten
	// per-question text is at most 20000 bytes of valid UTF-8, empty
	// allowed (required questions left blank stay held).
	maxRewriteTextBytes = 20000
)

// rewriteRunner is the legacy Codex shared-runner accessor kept for building
// until E13.
// TODO E13 (M): remove with the legacy CodexDrafter binding; rewrite uses
// standardRewriteRunner (see standard.go).
func rewriteRunner(draft Drafter) (OneShotTurn, error) {
	drafter, ok := draft.(*CodexDrafter)
	if !ok || drafter == nil || drafter.Turns == nil {
		return nil, ErrUnavailable
	}
	return drafter.Turns, nil
}

// priorMaterialTexts carries the current per-question texts recovered for
// one rewrite prompt: E3 answered values stay authoritative, the prior pack
// manifest supplies drafted or rewritten texts, and a direct-edit base
// additionally contributes its combined text as prompt context.
type priorMaterialTexts struct {
	byQuestion map[string]string
	combined   string
	origin     string
}

// parsePriorMaterialTexts recovers per-question texts from a prior pack
// manifest, verifying the material section still binds the base version's
// check pins. Any mismatch fails closed: a manifest that no longer binds
// its version is never used as rewrite context.
func parsePriorMaterialTexts(manifest []byte, base store.MaterialVersionView) (priorMaterialTexts, error) {
	var out priorMaterialTexts
	var decoded struct {
		Material *struct {
			CheckID           string `json:"checkId"`
			QuestionSetSHA256 string `json:"questionSetSha256"`
			Origin            string `json:"origin"`
			Text              string `json:"text"`
			Answers           []struct {
				QuestionID string `json:"questionId"`
				Text       string `json:"text"`
			} `json:"answers"`
		} `json:"material"`
	}
	if err := json.Unmarshal(manifest, &decoded); err != nil || decoded.Material == nil {
		return out, fmt.Errorf("materialprep: prior pack material unreadable")
	}
	material := decoded.Material
	if material.CheckID != base.CheckID || material.QuestionSetSHA256 != base.QuestionSetSHA256 {
		return out, fmt.Errorf("materialprep: prior pack material pins mismatch")
	}
	switch material.Origin {
	case store.MaterialOriginPrepared, store.MaterialOriginDirectEdit, store.MaterialOriginRewrite:
		out.origin = material.Origin
	default:
		return out, fmt.Errorf("materialprep: prior pack material origin %q unknown", material.Origin)
	}
	out.byQuestion = make(map[string]string, len(material.Answers))
	for _, answer := range material.Answers {
		if answer.QuestionID == "" {
			continue
		}
		out.byQuestion[answer.QuestionID] = answer.Text
	}
	if material.Origin == store.MaterialOriginDirectEdit {
		out.combined = material.Text
	}
	return out, nil
}

// rewritePrompt assembles the rewrite prompt: trusted discipline, owner
// instruction, current per-question texts, and verified facts. It carries
// saved state only: no employer contact handles, credentials, or send
// authority exist in the envelope, so none can reach the turn.
func rewritePrompt(instruction string, questions []store.CheckQuestionView, current map[string]string, combined string, answered []AnsweredFact, saved []SavedAnswerFact, career []applicationpacks.Source, profile store.Preferences) string {
	var prompt strings.Builder
	prompt.WriteString(RewriteInstructions)
	prompt.WriteString("\n\nOwner instruction:\n")
	if strings.TrimSpace(instruction) == "" {
		prompt.WriteString("(none — improve clarity and completeness from the verified facts only)\n")
	} else {
		prompt.WriteString(instruction + "\n")
	}
	prompt.WriteString("\nCurrent answers by question (rewrite every listed id exactly once):\n")
	for _, question := range questions {
		text := current[question.ID]
		if text == "" {
			text = "(blank)"
		}
		fmt.Fprintf(&prompt, "- %s [%s]: %s\n  current: %s\n", question.ID, question.Required, question.Text, text)
	}
	if combined != "" {
		fmt.Fprintf(&prompt, "\nCurrent combined material text (context only, superseded per question above):\n%s\n", combined)
	}
	prompt.WriteString("\nVerified owner answers (reuse, do not contradict):\n")
	if len(answered) == 0 {
		prompt.WriteString("(none)\n")
	}
	for _, fact := range answered {
		fmt.Fprintf(&prompt, "- %s: %s\n", fact.Question, fact.Text)
	}
	prompt.WriteString("\nApproved saved answers:\n")
	if len(saved) == 0 {
		prompt.WriteString("(none)\n")
	}
	savedRunes := 0
	for _, fact := range saved {
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
	for _, source := range career {
		body := source.Body
		mark := ""
		if len([]rune(body)) > maxDraftCareerRunes {
			body = truncateRunes(body, maxDraftCareerRunes)
			mark = "\n[truncated to fit the turn budget]"
		}
		fmt.Fprintf(&prompt, "--- %s (%s) ---\n%s%s\n", source.ID, source.Name, body, mark)
	}
	fmt.Fprintf(&prompt, "\nProfile: location %q, remote %v, hybrid %v, target %d/100h, min base %d %s, timezone %q.\n",
		profile.PreferredLocation, profile.AllowRemote, profile.AllowHybrid,
		profile.TargetHoursHundredths, profile.MinMonthlyBaseCents, profile.SalaryCurrency, profile.Timezone)
	for _, criterion := range profile.RoleCriteria {
		fmt.Fprintf(&prompt, "- criterion %s [%s/%s]: %s — %s\n",
			criterion.ID, criterion.Kind, criterion.Mode, criterion.Label, criterion.Description)
	}
	prompt.WriteString(`
Respond with ONLY this JSON object, no other text:
{"texts":[{"questionId":"...","text":"..."}]}
Rules: cover every listed question id exactly once, at most once each; each
text is at most 20000 bytes of valid UTF-8; the empty string blanks that
question (required questions left blank stay held); use only the verified
facts above; never invent, contact, or browse.`)
	return prompt.String()
}

// rewriteTextOut is one rewritten per-question text in model output.
type rewriteTextOut struct {
	QuestionID string `json:"questionId"`
	Text       string `json:"text"`
}

// rewritePayload is the exact accepted rewrite output shape. Unknown fields
// are rejected so smuggled content fails loudly instead of slipping through.
type rewritePayload struct {
	Texts []rewriteTextOut `json:"texts"`
}

// rewritePayloadText joins the collected messages and extracts the JSON
// payload: either the raw object or a single fenced block. Anything else is
// rejected so prose around the payload fails loudly.
func rewritePayloadText(messages []string) (string, error) {
	trimmed := strings.TrimSpace(strings.Join(messages, "\n"))
	if trimmed == "" {
		return "", fmt.Errorf("empty rewrite response")
	}
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	_, rest, _ := strings.Cut(trimmed[3:], "\n")
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", fmt.Errorf("unclosed rewrite code fence")
	}
	return strings.TrimSpace(rest[:end]), nil
}

// checkModelRewrite parses and validates one rewrite turn's output. Coverage
// is strict: texts for EVERY pinned question exactly once, each at most
// 20000 bytes of valid UTF-8. Empty texts are allowed and hold required
// questions downstream. Model-output failures are plain errors, never
// confused with client-invalid; the caller runs no second turn.
func checkModelRewrite(questions []store.CheckQuestionView, messages []string) ([]store.MaterialRewriteText, error) {
	text, err := rewritePayloadText(messages)
	if err != nil {
		return nil, err
	}
	var payload rewritePayload
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid rewrite response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid rewrite response: trailing content")
	}
	byID := make(map[string]string, len(payload.Texts))
	for _, entry := range payload.Texts {
		if entry.QuestionID == "" {
			return nil, fmt.Errorf("rewrite text without question id")
		}
		if _, dup := byID[entry.QuestionID]; dup {
			return nil, fmt.Errorf("duplicate rewrite text: %q", entry.QuestionID)
		}
		if len(entry.Text) > maxRewriteTextBytes || !utf8.ValidString(entry.Text) || strings.ContainsRune(entry.Text, 0) {
			return nil, fmt.Errorf("rewrite text for %q", entry.QuestionID)
		}
		byID[entry.QuestionID] = entry.Text
	}
	if len(byID) != len(questions) {
		return nil, fmt.Errorf("rewrite covers %d of %d pinned questions", len(byID), len(questions))
	}
	out := make([]store.MaterialRewriteText, 0, len(questions))
	for _, question := range questions {
		text, ok := byID[question.ID]
		if !ok {
			return nil, fmt.Errorf("rewrite missing pinned question %q", question.ID)
		}
		out = append(out, store.MaterialRewriteText{QuestionID: question.ID, Text: text, TextSHA256: textSHA256(text)})
	}
	return out, nil
}

// rewriteMaterialAnswer pins one ordinal rewritten text in a rewrite
// manifest, making the immutable pack self-contained.
type rewriteMaterialAnswer struct {
	QuestionID string `json:"questionId"`
	Text       string `json:"text"`
}

// rewriteMaterialSection is the pack-manifest binding for a rewrite. The
// store verifies checkId, questionSetSha256, origin, the ordinal question
// ids, and each entry text against its input sha.
type rewriteMaterialSection struct {
	CheckID           string                  `json:"checkId"`
	QuestionSetSHA256 string                  `json:"questionSetSha256"`
	Origin            string                  `json:"origin"`
	Answers           []rewriteMaterialAnswer `json:"answers"`
}

// buildRewriteInput assembles the validated pack input for one explicit
// rewrite: the pinned role record, career plus role sources, deterministic
// role lines, and one materialUnknowns entry per required question the
// rewrite leaves blank. Rewritten texts travel manifest-only, never as
// cited pack lines: the turn returns plain texts without citation cover,
// and the store pins them as manifest-only bytes exactly like drafts.
func buildRewriteInput(p revisionPins, sources []applicationpacks.Source, template []byte, texts []store.MaterialRewriteText, requestKey string) (applicationpacks.Input, rewriteMaterialSection, error) {
	role := roleSource(p.opportunity, p.company)
	all := make([]applicationpacks.Source, 0, len(sources)+1)
	all = append(all, sources...)
	all = append(all, role)
	if len(all) > 8 {
		return applicationpacks.Input{}, rewriteMaterialSection{}, fmt.Errorf("%w: too many pack sources", store.ErrInvalid)
	}
	byID := make(map[string]string, len(texts))
	for _, entry := range texts {
		byID[entry.QuestionID] = entry.Text
	}
	section := rewriteMaterialSection{CheckID: p.check.ID, QuestionSetSHA256: p.check.QuestionSetSHA256,
		Origin: store.MaterialOriginRewrite, Answers: make([]rewriteMaterialAnswer, 0, len(p.questions))}
	required, held := 0, 0
	var unknowns []string
	for _, question := range p.questions {
		section.Answers = append(section.Answers, rewriteMaterialAnswer{QuestionID: question.ID, Text: byID[question.ID]})
		if question.Required != store.CheckRequired {
			continue
		}
		required++
		if byID[question.ID] == "" {
			held++
			unknowns = append(unknowns, unknownsEntry(question))
		}
	}
	if len(unknowns) > maxMaterialUnknowns {
		return applicationpacks.Input{}, rewriteMaterialSection{}, fmt.Errorf("%w: too many held questions", store.ErrInvalid)
	}
	focus := roleFocusLine(p.opportunity.Title, p.company.Name)
	cover := roleCoverLine(p.opportunity.Title, p.company.Name, required-held, required, held)
	input := applicationpacks.Input{
		Role:    revisionRole(p),
		Sources: all,
		Draft: applicationpacks.Draft{Focus: focus, Cover: []applicationpacks.Line{cover},
			MaterialUnknowns: unknowns},
		CVTemplate: template, TemplateSHA256: textSHA256(string(template)),
		PreparationRequestSHA256: requestSHA(requestKey, p.check.QuestionSetSHA256, p.workflowRevision),
	}
	return input, section, nil
}

// RewriteOpportunityMaterials runs one explicit Standard rewrite turn and
// commits it as a new immutable version. It validates the owner instruction,
// recovers current per-question texts from saved answers plus the prior
// pack manifest, runs exactly one turn through the shared Standard runner,
// strictly validates full pinned coverage, probes the store for a replay
// before rendering, then renders and commits. The turn runs before the
// probe (like drafting in prepare) because the replay digest needs the
// resolved texts; a lost response therefore costs one turn on retry but
// never a second render or version. It performs no research, capture,
// fetch, contact, or send, and no Jev assessment: the Relevance
// collaborator is never touched on this path.
func (s *Service) RewriteOpportunityMaterials(ctx context.Context, actor store.Actor, opportunityID, requestKey string, expectedVersion int64, instruction string) (store.MaterialVersionView, bool, error) {
	if s == nil || s.Store == nil || s.Career == nil || s.Render == nil {
		return store.MaterialVersionView{}, false, ErrUnavailable
	}
	runner, err := standardRewriteRunner(s.Draft)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	if actor.Kind != "administrator" || actor.ID == "" || !validRequestKey(requestKey) ||
		expectedVersion < 1 || !utf8.ValidString(instruction) || len([]rune(instruction)) > maxRewriteInstructionRunes {
		return store.MaterialVersionView{}, false, store.ErrInvalid
	}
	pins, err := s.loadRevisionPins(ctx, opportunityID, expectedVersion)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	if pins.base.CheckID != pins.check.ID || pins.base.QuestionSetSHA256 != pins.check.QuestionSetSHA256 {
		return store.MaterialVersionView{}, false, store.ErrConflict
	}
	pack, err := s.Store.ApplicationPack(ctx, pins.base.PackID)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	prior, err := parsePriorMaterialTexts(pack.ManifestJSON, pins.base)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	answers, err := s.Store.CurrentQuestionAnswers(ctx, opportunityID)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	if answers.CheckID != pins.check.ID || answers.QuestionSetSHA256 != pins.check.QuestionSetSHA256 {
		return store.MaterialVersionView{}, false, store.ErrConflict
	}
	sources, template, err := s.Career()
	if err != nil {
		return store.MaterialVersionView{}, false, fmt.Errorf("materialprep: career sources: %w", err)
	}
	role := roleSource(pins.opportunity, pins.company)
	shas, err := sourceShas(sources, role)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	values := make(map[string]store.QuestionAnswerValue, len(answers.Values))
	for _, value := range answers.Values {
		values[value.QuestionID] = value
	}
	current := make(map[string]string, len(pins.questions))
	answered := make([]AnsweredFact, 0)
	for _, question := range pins.questions {
		if value, ok := values[question.ID]; ok && value.State == store.AnswerValueStateAnswered {
			current[question.ID] = value.Text
			answered = append(answered, AnsweredFact{QuestionID: question.ID,
				Question: question.Text, Text: value.Text, AnswerVersion: value.Version})
			continue
		}
		current[question.ID] = prior.byQuestion[question.ID]
	}
	saved, err := s.savedAnswerContext(ctx)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	stdInput := buildStandardRewriteInput(pins.check.ID, instruction, pins.questions, current, prior.combined, answered, saved, sources, pins.profile)
	result, err := runner.RunStandard(ctx, stdInput)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	texts, err := checkModelRewrite(pins.questions, result.Messages)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	if view, replayed, err := s.Store.RewriteOpportunityMaterials(ctx, actor, opportunityID, store.MaterialRewriteInput{
		RequestKey: requestKey, ExpectedVersion: expectedVersion, Texts: texts, SourceShas: shas,
	}); err == nil {
		return view, replayed, nil
	} else if !errors.Is(err, store.ErrInvalid) {
		return store.MaterialVersionView{}, false, err
	}
	input, section, err := buildRewriteInput(pins, sources, template, texts, requestKey)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	if err := applicationpacks.ValidateInput(input); err != nil {
		return store.MaterialVersionView{}, false, fmt.Errorf("%w: pack input: %v", store.ErrInvalid, err)
	}
	prepared, err := s.Render.Prepare(ctx, input)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	manifest, err := injectRevisionMaterial(prepared.ManifestJSON, section)
	if err != nil {
		return store.MaterialVersionView{}, false, err
	}
	return s.Store.RewriteOpportunityMaterials(ctx, actor, opportunityID, store.MaterialRewriteInput{
		RequestKey: requestKey, ExpectedVersion: expectedVersion, Texts: texts,
		Pack: store.ApplicationPackMutationInput{OpportunityID: opportunityID,
			ExpectedOpportunityRevision: pins.opportunity.Revision, ExpectedProfileRevision: pins.profile.Version,
			ContentSHA256: packContentHash(manifest, prepared.TypstSource, prepared.PDF),
			ManifestJSON:  manifest, TypstSource: prepared.TypstSource, PDF: prepared.PDF},
		SourceShas: shas,
	})
}
