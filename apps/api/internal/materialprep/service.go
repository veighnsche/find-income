// Package materialprep runs grounded preparation for one selected role: it
// verifies the pinned check and saved answers, drafts required-but-unset
// answers in one bounded Muse Standard turn over verified facts only,
// validates citation-exactness, runs Jev relevance per unique citation,
// Typst-renders the pack, and commits through
// store.PrepareOpportunityMaterials.
//
// The service performs no research, fetch, or send of any kind. It holds
// no capability for employer contact: its only outbound dependencies are
// the injected Career, Draft, Relevance, and Render collaborators plus the
// store. Pin verification (check, answers, workflow, opportunity) always
// precedes any Standard spend, and zero required+unset questions means zero
// Standard call. The optional Captures reader opens only already-stored
// immutable vacancy capture bytes for the pack role description; it
// performs no retrieval.
package materialprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// ErrUnavailable reports a missing preparation dependency (store, career
// sources, drafter, relevance, or renderer). The HTTP layer maps it to 503.
var ErrUnavailable = errors.New("material preparation unavailable")

const (
	// maxUniqueCitations mirrors the bounded pack helper: at most six Jev
	// relevance calls per prepare, one per unique cited excerpt.
	maxUniqueCitations = 6
	// maxPackAnswers and maxMaterialUnknowns are the applicationpacks.Input
	// bounds; drafts beyond them fail honestly instead of truncating.
	maxPackAnswers      = 12
	maxMaterialUnknowns = 20
	// maxSavedAnswers caps the reusable-answer context handed to one draft
	// turn. The library is paged in store order until the cap.
	maxSavedAnswers = 500
	// roleSourceID identifies the service-composed snapshot of the saved
	// role record. It is a pinned exact snapshot like any other source:
	// bytes are fixed before use and the sha travels in SourceShas.
	roleSourceID = "role-description"
)

// Material answer states carried in the manifest material section. Answered
// and blank mirror E3 rows; drafted marks a Standard draft (bytes in the
// manifest only, never written back to E3); held marks a required question
// with neither value nor draft.
const (
	answerStateAnswered = "answered"
	answerStateBlank    = "blank"
	answerStateDrafted  = "drafted"
	answerStateHeld     = "held"
)

// CareerLoader returns the pinned approved career sources plus the Typst
// template. Production wires applicationpacks.LoadApprovedCareerSources.
type CareerLoader func() ([]applicationpacks.Source, []byte, error)

// DraftQuestion is one required+unset employer question needing a draft.
type DraftQuestion struct {
	ID         string
	Text       string
	TextSHA256 string
	Kind       string
}

// AnsweredFact is one E3 answered value supplied as verified draft context.
type AnsweredFact struct {
	QuestionID    string
	Question      string
	Text          string
	AnswerVersion int64
}

// SavedAnswerFact is one owner-approved library answer (current version)
// supplied as verified draft context.
type SavedAnswerFact struct {
	AnswerID    string
	Version     int64
	TextSHA256  string
	Text        string
	ScopeTags   []string
	ContextNote string
}

// DraftRequest is the whole verified-fact envelope for one bounded Standard
// turn. It carries saved state only: answered texts, library versions,
// pinned career sources, and profile scalars. It never carries employer
// contact handles, credentials, or send authority.
type DraftRequest struct {
	OpportunityID    string
	OpportunityTitle string
	CompanyName      string
	CheckID          string
	RequiredUnset    []DraftQuestion
	Answered         []AnsweredFact
	SavedAnswers     []SavedAnswerFact
	CareerSources    []applicationpacks.Source
	Profile          store.Preferences
}

// RequiredDraft is one Standard-drafted answer. Lines carry exact citations
// against the pack sources; ValidateInput rejects invented excerpts.
type RequiredDraft struct {
	QuestionID string
	Lines      []applicationpacks.Line
}

// Drafter runs the single bounded Standard turn. It is invoked at most once
// per prepare, and never when no required question is unset.
type Drafter interface {
	DraftRequiredAnswers(ctx context.Context, request DraftRequest) ([]RequiredDraft, error)
}

// RelevanceAssessor runs one Jev relevance judgment over a supplied
// requirement and an exact approved-source excerpt. It performs no
// research; the narrow shape keeps D3 independent of round charging (the
// jevservice binding needs a round, which prepare does not open).
type RelevanceAssessor interface {
	AssessRelevance(ctx context.Context, requirement string, source applicationpacks.Source, excerpt string) (applicationpacks.Relevance, error)
}

// PackRenderer Typst-renders one validated pack input. Production wires
// applicationpacks.Renderer.
type PackRenderer interface {
	Prepare(ctx context.Context, input applicationpacks.Input) (applicationpacks.Prepared, error)
}

// Service prepares grounded application materials. The zero value is
// unusable; the coordinator wires Store plus the four collaborators.
type Service struct {
	Store     *store.Store
	Career    CareerLoader
	Draft     Drafter
	Relevance RelevanceAssessor
	Render    PackRenderer
	// Captures opens already-stored vacancy capture bytes when the saved
	// opportunity carries no text (discovery-saved roles). Nil keeps the
	// previous behavior: undescribed roles fail at pack validation.
	Captures researchcontract.CaptureReader
}

// materialAnswerEntry pins one ordinal question in the manifest material
// section. Text carries the answered or drafted bytes; blank and held
// entries carry the empty string.
type materialAnswerEntry struct {
	QuestionID    string `json:"questionId"`
	State         string `json:"state"`
	AnswerVersion int64  `json:"answerVersion"`
	TextSHA256    string `json:"textSha256"`
	Text          string `json:"text"`
}

// materialSection is injected into the rendered manifest after Typst
// rendering (applicationpacks.Input has no material field by design). The
// store verifies checkId, questionSetSha256, origin, and the ordinal answer
// count; the extra entry fields make the immutable pack self-contained.
type materialSection struct {
	CheckID           string                `json:"checkId"`
	QuestionSetSHA256 string                `json:"questionSetSha256"`
	Origin            string                `json:"origin"`
	Answers           []materialAnswerEntry `json:"answers"`
}

// emptyTextSHA256 is sha256(""): the text sha of blank and held entries,
// matching the store partition.
func emptyTextSHA256() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

func textSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func validSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func validRequestKey(key string) bool {
	return key != "" && len(key) <= 200 && strings.TrimSpace(key) == key
}

// truncateRunes shortens text to at most max runes without splitting UTF-8.
func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	count := 0
	for i := range text {
		if count == max {
			return text[:i]
		}
		count++
	}
	return text
}

// packContentHash mirrors applicationpacks.Prepare and the store pack writer:
// sha256 over the JSON envelope of manifest, Typst source, and PDF bytes.
// The manifest changes after rendering (material injection), so the service
// recomputes the digest over the final bytes it commits.
func packContentHash(manifest, source, pdf []byte) string {
	encoded, _ := json.Marshal(struct {
		Manifest []byte
		Source   []byte
		PDF      []byte
	}{manifest, source, pdf})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// pinned groups the verified reads one prepare is bound to.
type pinned struct {
	check       store.CheckView
	values      map[string]store.QuestionAnswerValue
	workflow    store.RoleWorkflow
	opportunity store.Opportunity
	company     store.Company
	profile     store.Preferences
	description string
}

// verifyPins loads the pinned check, saved answers, workflow, opportunity,
// company, and profile, failing with ErrNotFound/ErrConflict before any
// Standard, Jev, or render spend. The store re-verifies every pin at commit;
// these reads only fail fast.
func (s *Service) verifyPins(ctx context.Context, opportunityID, expectedCheckID, expectedQuestionSet string, expectedWorkflowRevision int64) (pinned, error) {
	var out pinned
	if opportunityID == "" || expectedCheckID == "" || !validSHA(expectedQuestionSet) || expectedWorkflowRevision < 0 {
		return out, store.ErrInvalid
	}
	status, err := s.Store.CurrentJobCheck(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if status.Status == store.CheckOverallNotChecked || status.Check == nil {
		return out, store.ErrNotFound
	}
	if status.Status != store.CheckStatusChecked {
		return out, store.ErrConflict
	}
	check := *status.Check
	if check.ID != expectedCheckID || check.QuestionSetSHA256 != expectedQuestionSet {
		return out, store.ErrConflict
	}
	answers, err := s.Store.CurrentQuestionAnswers(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if answers.CheckID != check.ID || answers.QuestionSetSHA256 != check.QuestionSetSHA256 {
		return out, store.ErrConflict
	}
	workflow, err := s.Store.RoleWorkflow(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if workflow.Revision != expectedWorkflowRevision ||
		(workflow.Stage != store.RoleStageAnswered && workflow.Stage != store.RoleStagePreparing &&
			workflow.Stage != store.RoleStagePrepared) {
		return out, store.ErrConflict
	}
	opportunity, err := s.Store.Opportunity(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if opportunity.ArchivedAt != "" {
		return out, store.ErrNotFound
	}
	if check.OpportunityRevision != opportunity.Revision {
		return out, store.ErrConflict
	}
	company, err := s.Store.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return out, err
	}
	profile, err := s.Store.CurrentPreferences(ctx)
	if err != nil {
		return out, err
	}
	description, err := s.roleDescription(ctx, opportunity, check.Vacancy.CaptureIDs)
	if err != nil {
		return out, err
	}
	out = pinned{check: check, values: make(map[string]store.QuestionAnswerValue, len(answers.Values)),
		workflow: workflow, opportunity: opportunity, company: company, profile: profile,
		description: description}
	for _, value := range answers.Values {
		out.values[value.QuestionID] = value
	}
	return out, nil
}

// partition mirrors the store hold rules over the verified reads: answered
// values are carried, blank required values are missing (never held), and
// required+unset questions need drafts. Optional and unknown-required
// questions are never drafted and never listed.
func (p pinned) partition() (requiredUnset []store.CheckQuestionView) {
	for _, question := range p.check.Questions {
		if question.Required != store.CheckRequired {
			continue
		}
		if _, ok := p.values[question.ID]; ok {
			continue
		}
		requiredUnset = append(requiredUnset, question)
	}
	return requiredUnset
}

// savedAnswerContext pages the owner-approved library (current versions
// only) as draft context. It runs only when a draft turn is needed.
func (s *Service) savedAnswerContext(ctx context.Context) ([]SavedAnswerFact, error) {
	out := make([]SavedAnswerFact, 0)
	cursor := ""
	for {
		page, err := s.Store.ListSavedAnswers(ctx, store.SavedAnswerListOptions{Cursor: cursor, Limit: 100})
		if err != nil {
			return nil, err
		}
		for _, answer := range page.Items {
			for _, version := range answer.Versions {
				if version.Version != answer.CurrentVersion {
					continue
				}
				out = append(out, SavedAnswerFact{AnswerID: answer.ID, Version: version.Version,
					TextSHA256: version.TextSHA256, Text: version.Text,
					ScopeTags: append([]string(nil), answer.ScopeTags...), ContextNote: answer.ContextNote})
				break
			}
			if len(out) >= maxSavedAnswers {
				return out, nil
			}
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// roleSource snapshots the saved role record as an exact approved source so
// role-specific pack lines cite pinned bytes. The body is composed of saved
// fields only, fixed before use, and its sha travels in SourceShas.
// maxRoleDescriptionBytes mirrors the pack Role.Description bound. Longer
// vacancy bytes fail closed instead of truncating the role record.
const maxRoleDescriptionBytes = 30000

// roleDescription resolves the pack role description: the saved
// opportunity text when present, else the check's first vacancy capture
// bytes verbatim (discovery-saved roles carry no opportunity text; the
// capture is the same verified bytes the check read). A nil Captures
// reader or no capture ids leaves the role undescribed and pack
// validation fails honestly, as before.
func (s *Service) roleDescription(ctx context.Context, opportunity store.Opportunity, captureIDs []string) (string, error) {
	if strings.TrimSpace(opportunity.OriginalText) != "" {
		return opportunity.OriginalText, nil
	}
	if s == nil || s.Captures == nil || len(captureIDs) == 0 {
		return "", nil
	}
	_, reader, err := s.Captures.OpenCapture(ctx, captureIDs[0])
	if err != nil {
		return "", fmt.Errorf("materialprep: open vacancy capture: %w", err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, maxRoleDescriptionBytes+1))
	if err != nil {
		return "", fmt.Errorf("materialprep: read vacancy capture: %w", err)
	}
	if len(raw) > maxRoleDescriptionBytes {
		return "", fmt.Errorf("%w: vacancy capture exceeds the role description bound", store.ErrInvalid)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", nil
	}
	return string(raw), nil
}

func roleSource(opportunity store.Opportunity, company store.Company) applicationpacks.Source {
	var body strings.Builder
	body.WriteString("Saved role record.\nTitle: ")
	body.WriteString(opportunity.Title)
	body.WriteString("\nCompany: ")
	body.WriteString(company.Name)
	body.WriteString("\nSource: ")
	body.WriteString(opportunity.SourceURL)
	body.WriteString("\nDescription:\n")
	body.WriteString(opportunity.OriginalText)
	text := body.String()
	return applicationpacks.Source{ID: roleSourceID, Name: "Saved role record",
		SHA256: textSHA256(text), Approved: true, Body: text}
}

// draftText joins cited lines into the single text pinned in the material
// ref and committed to the store as a MaterialDraft.
func draftText(lines []applicationpacks.Line) string {
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	return strings.Join(texts, "\n\n")
}

// checkDrafts fences Standard output to the required+unset scope: every draft
// names a question from this prepare's required+unset set exactly once,
// carries 1-8 cited lines, and joins to valid non-blank text.
func checkDrafts(requiredUnset []store.CheckQuestionView, drafts []RequiredDraft) (map[string]RequiredDraft, error) {
	scope := make(map[string]struct{}, len(requiredUnset))
	for _, question := range requiredUnset {
		scope[question.ID] = struct{}{}
	}
	byID := make(map[string]RequiredDraft, len(drafts))
	for _, draft := range drafts {
		if _, ok := scope[draft.QuestionID]; !ok {
			return nil, fmt.Errorf("%w: draft outside required+unset scope", store.ErrInvalid)
		}
		if _, dup := byID[draft.QuestionID]; dup {
			return nil, fmt.Errorf("%w: duplicate draft", store.ErrInvalid)
		}
		if len(draft.Lines) == 0 || len(draft.Lines) > 8 {
			return nil, fmt.Errorf("%w: draft line count", store.ErrInvalid)
		}
		text := draftText(draft.Lines)
		if strings.TrimSpace(text) == "" || len(text) > 20000 || !utf8.ValidString(text) {
			return nil, fmt.Errorf("%w: draft text", store.ErrInvalid)
		}
		byID[draft.QuestionID] = draft
	}
	return byID, nil
}

// requestSHA pins the logical prepare request for the manifest audit field.
// Inputs are stable across retries (no random ids), so replays converge.
func requestSHA(requestKey, questionSet string, workflowRevision int64) string {
	data, _ := json.Marshal(struct {
		RequestKey        string `json:"requestKey"`
		QuestionSetSHA256 string `json:"questionSetSha256"`
		WorkflowRevision  int64  `json:"workflowRevision"`
	}{requestKey, questionSet, workflowRevision})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// unknownsEntry names one held/missing required question for the delivery
// gate. Entries stay within the 500-rune pack bound.
func unknownsEntry(question store.CheckQuestionView) string {
	const prefix = "No verified answer for required employer question: \""
	const suffix = "\""
	const maxText = 440
	return prefix + truncateRunes(question.Text, maxText) + suffix
}

// buildInput assembles the validated pack input: role facts, career plus
// role sources, deterministic role lines, drafted answers, and one
// materialUnknowns entry per held/missing required question in ordinal
// order. It also returns the material section and store drafts, resolved
// from the same partition so the manifest, the Jev set, and the commit
// always agree.
func buildInput(p pinned, sources []applicationpacks.Source, template []byte, drafts map[string]RequiredDraft, requestKey string) (applicationpacks.Input, materialSection, []store.MaterialDraft, error) {
	role := roleSource(p.opportunity, p.company)
	all := make([]applicationpacks.Source, 0, len(sources)+1)
	all = append(all, sources...)
	all = append(all, role)

	required, answeredCount, heldCount := 0, 0, 0
	for _, question := range p.check.Questions {
		if question.Required != store.CheckRequired {
			continue
		}
		required++
		value, ok := p.values[question.ID]
		switch {
		case ok && value.State == store.AnswerValueStateAnswered:
			answeredCount++
		case ok && value.State == store.AnswerValueStateBlank:
			heldCount++
		default:
			if _, ok := drafts[question.ID]; ok {
				answeredCount++
			} else {
				heldCount++
			}
		}
	}
	focus := applicationpacks.Line{
		Text:      fmt.Sprintf("Application for %s at %s.", p.opportunity.Title, p.company.Name),
		Citations: []applicationpacks.Citation{{SourceID: role.ID, Excerpt: p.opportunity.Title}},
	}
	cover := applicationpacks.Line{
		Text: fmt.Sprintf("Prepared application material for %s at %s: %d of %d required employer questions answered, %d held.",
			p.opportunity.Title, p.company.Name, answeredCount, required, heldCount),
		Citations: []applicationpacks.Citation{{SourceID: role.ID, Excerpt: p.company.Name}},
	}
	section := materialSection{CheckID: p.check.ID, QuestionSetSHA256: p.check.QuestionSetSHA256,
		Origin: store.MaterialOriginPrepared, Answers: make([]materialAnswerEntry, 0, len(p.check.Questions))}
	var packAnswers []applicationpacks.Answer
	var unknowns []string
	var storeDrafts []store.MaterialDraft
	for _, question := range p.check.Questions {
		entry := materialAnswerEntry{QuestionID: question.ID}
		value, ok := p.values[question.ID]
		draft, hasDraft := drafts[question.ID]
		switch {
		case ok && value.State == store.AnswerValueStateAnswered:
			entry.State, entry.AnswerVersion, entry.TextSHA256, entry.Text =
				answerStateAnswered, value.Version, value.TextSHA256, value.Text
		case ok && value.State == store.AnswerValueStateBlank:
			entry.State, entry.AnswerVersion, entry.TextSHA256 =
				answerStateBlank, value.Version, emptyTextSHA256()
			if question.Required == store.CheckRequired {
				unknowns = append(unknowns, unknownsEntry(question))
			}
		case hasDraft:
			text := draftText(draft.Lines)
			entry.State, entry.TextSHA256, entry.Text = answerStateDrafted, textSHA256(text), text
			packAnswers = append(packAnswers, applicationpacks.Answer{
				Question: truncateRunes(question.Text, 1000), Lines: draft.Lines})
			storeDrafts = append(storeDrafts, store.MaterialDraft{
				QuestionID: question.ID, Text: text, TextSHA256: textSHA256(text)})
		case question.Required == store.CheckRequired:
			entry.State, entry.TextSHA256 = answerStateHeld, emptyTextSHA256()
			unknowns = append(unknowns, unknownsEntry(question))
		default:
			entry.State, entry.TextSHA256 = answerStateBlank, emptyTextSHA256()
		}
		section.Answers = append(section.Answers, entry)
	}
	if len(packAnswers) > maxPackAnswers {
		return applicationpacks.Input{}, materialSection{}, nil,
			fmt.Errorf("%w: too many drafted answers", store.ErrInvalid)
	}
	if len(unknowns) > maxMaterialUnknowns {
		return applicationpacks.Input{}, materialSection{}, nil,
			fmt.Errorf("%w: too many held questions", store.ErrInvalid)
	}
	destination := ""
	if p.check.Route.Judgment == store.CheckRouteJudgmentApplication && strings.TrimSpace(p.check.Route.DestinationText) != "" {
		destination = p.check.Route.DestinationText
	}
	input := applicationpacks.Input{
		Role: applicationpacks.Role{OpportunityID: p.opportunity.ID,
			OpportunityRevision: p.opportunity.Revision, ProfileRevision: p.profile.Version,
			Title: p.opportunity.Title, Company: p.company.Name, SourceURL: p.opportunity.SourceURL,
			Description: p.description, Destination: destination},
		Sources: all,
		Draft: applicationpacks.Draft{Focus: focus, Cover: []applicationpacks.Line{cover},
			Answers: packAnswers, MaterialUnknowns: unknowns},
		CVTemplate: template, TemplateSHA256: textSHA256(string(template)),
		PreparationRequestSHA256: requestSHA(requestKey, p.check.QuestionSetSHA256, p.workflow.Revision),
	}
	return input, section, storeDrafts, nil
}

// citationTargets collects unique citations in pack order (focus, cover,
// answers) with the requirement each is judged against. Role lines are
// judged against the role requirement; answer lines against their employer
// question text.
type citationTarget struct {
	requirement string
	source      applicationpacks.Source
	excerpt     string
}

func citationTargets(input applicationpacks.Input) ([]citationTarget, error) {
	byID := make(map[string]applicationpacks.Source, len(input.Sources))
	for _, source := range input.Sources {
		byID[source.ID] = source
	}
	roleRequirement := fmt.Sprintf("Application for %s at %s.", input.Role.Title, input.Role.Company)
	var out []citationTarget
	seen := make(map[string]struct{})
	add := func(requirement string, line applicationpacks.Line) error {
		for _, citation := range line.Citations {
			key := citation.SourceID + "\x00" + citation.Excerpt
			if _, dup := seen[key]; dup {
				continue
			}
			if len(seen) >= maxUniqueCitations {
				return fmt.Errorf("%w: too many unique citations", store.ErrInvalid)
			}
			seen[key] = struct{}{}
			source, ok := byID[citation.SourceID]
			if !ok {
				return fmt.Errorf("%w: citation source", store.ErrInvalid)
			}
			out = append(out, citationTarget{requirement: requirement, source: source, excerpt: citation.Excerpt})
		}
		return nil
	}
	if err := add(roleRequirement, input.Draft.Focus); err != nil {
		return nil, err
	}
	for _, line := range input.Draft.Cover {
		if err := add(roleRequirement, line); err != nil {
			return nil, err
		}
	}
	for _, answer := range input.Draft.Answers {
		for _, line := range answer.Lines {
			if err := add(truncateRunes(answer.Question, 1000), line); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// injectMaterial adds the material section to a rendered manifest and
// returns the final bytes. The base layout mirrors applicationpacks.Input
// field order with material appended, keeping committed bytes deterministic.
func injectMaterial(manifest []byte, section materialSection) ([]byte, error) {
	var base struct {
		Role                     applicationpacks.Role        `json:"role"`
		Sources                  []applicationpacks.Source    `json:"sources"`
		Draft                    applicationpacks.Draft       `json:"draft"`
		TemplateSHA256           string                       `json:"templateSha256"`
		PreparationRequestSHA256 string                       `json:"preparationRequestSha256,omitempty"`
		Correction               *applicationpacks.Correction `json:"correction,omitempty"`
		Material                 materialSection              `json:"material"`
	}
	if err := json.Unmarshal(manifest, &base); err != nil {
		return nil, err
	}
	base.Material = section
	return json.Marshal(base)
}

// sourceShas returns the digests this prepare consumed: career sources plus
// the role snapshot. The store appends the question-set and text shas.
func sourceShas(sources []applicationpacks.Source, role applicationpacks.Source) ([]string, error) {
	out := make([]string, 0, len(sources)+1)
	for _, source := range sources {
		if !validSHA(source.SHA256) {
			return nil, fmt.Errorf("%w: career source digest", store.ErrInvalid)
		}
		out = append(out, source.SHA256)
	}
	out = append(out, role.SHA256)
	return out, nil
}

// probeReplay asks the store to replay an already-committed request key
// without rendering. The store checks idempotency before pack validation,
// so an empty pack probes cheaply: replay success returns created=false,
// ErrRoundIdempotencyConflict reports genuinely changed input, and
// ErrInvalid (after service-side validation ruled out every other invalid
// class) means no committed row, so the caller proceeds to render. All
// other errors return as-is.
func (s *Service) probeReplay(ctx context.Context, actor store.Actor, opportunityID, requestKey, expectedCheckID, expectedQuestionSet string, expectedWorkflowRevision int64, drafts []store.MaterialDraft, shas []string) (store.MaterialStatusView, bool, error) {
	return s.Store.PrepareOpportunityMaterials(ctx, actor, opportunityID, store.MaterialPrepareInput{
		RequestKey: requestKey, ExpectedCheckID: expectedCheckID,
		ExpectedQuestionSetSHA256: expectedQuestionSet, ExpectedWorkflowRevision: expectedWorkflowRevision,
		Drafts: drafts, SourceShas: shas,
	})
}

// replayAfterPinDrift replays an already-committed request key after a pin
// fast-fail, spending no Codex and no render. It succeeds only when the
// current reads show nothing needs drafting (so the probe needs no drafts)
// and the store confirms the resolved state matches the committed digest.
func (s *Service) replayAfterPinDrift(ctx context.Context, actor store.Actor, opportunityID, requestKey, expectedCheckID, expectedQuestionSet string, expectedWorkflowRevision int64) (store.MaterialStatusView, bool, bool) {
	status, err := s.Store.CurrentJobCheck(ctx, opportunityID)
	if err != nil || status.Status != store.CheckStatusChecked || status.Check == nil {
		return store.MaterialStatusView{}, false, false
	}
	answers, err := s.Store.CurrentQuestionAnswers(ctx, opportunityID)
	if err != nil || answers.CheckID != status.Check.ID {
		return store.MaterialStatusView{}, false, false
	}
	valued := make(map[string]struct{}, len(answers.Values))
	for _, value := range answers.Values {
		valued[value.QuestionID] = struct{}{}
	}
	for _, question := range status.Check.Questions {
		if question.Required != store.CheckRequired {
			continue
		}
		if _, ok := valued[question.ID]; !ok {
			return store.MaterialStatusView{}, false, false
		}
	}
	sources, _, err := s.Career()
	if err != nil {
		return store.MaterialStatusView{}, false, false
	}
	opportunity, err := s.Store.Opportunity(ctx, opportunityID)
	if err != nil || opportunity.ArchivedAt != "" {
		return store.MaterialStatusView{}, false, false
	}
	company, err := s.Store.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return store.MaterialStatusView{}, false, false
	}
	shas, err := sourceShas(sources, roleSource(opportunity, company))
	if err != nil {
		return store.MaterialStatusView{}, false, false
	}
	view, replayed, err := s.probeReplay(ctx, actor, opportunityID, requestKey,
		expectedCheckID, expectedQuestionSet, expectedWorkflowRevision, nil, shas)
	if err != nil || replayed {
		return store.MaterialStatusView{}, false, false
	}
	return view, false, true
}

// PrepareOpportunityMaterials implements the httpapi MaterialPreparer
// contract: verify pins, draft required+unset questions in one bounded
// Standard turn (skipped when empty), validate citations, run Jev relevance
// per unique citation, Typst-render, and commit. It performs no research,
// capture, fetch, or send.
func (s *Service) PrepareOpportunityMaterials(ctx context.Context, actor store.Actor, opportunityID, requestKey string, expectedCheckID, expectedQuestionSetSHA256 string, expectedWorkflowRevision int64) (store.MaterialStatusView, bool, error) {
	if s == nil || s.Store == nil || s.Career == nil || s.Render == nil {
		return store.MaterialStatusView{}, false, ErrUnavailable
	}
	if actor.Kind != "administrator" || actor.ID == "" || !validRequestKey(requestKey) {
		return store.MaterialStatusView{}, false, store.ErrInvalid
	}
	resolved, err := s.verifyPins(ctx, opportunityID, expectedCheckID, expectedQuestionSetSHA256, expectedWorkflowRevision)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Stale pins may still address an already-committed key: the
			// store digest pins resolved state only, so a retry after a
			// lost response replays. Attempt that probe when it costs no
			// Standard call (nothing needs drafting); anything else keeps the
			// fast 409 and the client refreshes before retrying.
			if view, replayed, ok := s.replayAfterPinDrift(ctx, actor, opportunityID, requestKey,
				expectedCheckID, expectedQuestionSetSHA256, expectedWorkflowRevision); ok {
				return view, replayed, nil
			}
		}
		return store.MaterialStatusView{}, false, err
	}
	requiredUnset := resolved.partition()
	sources, template, err := s.Career()
	if err != nil {
		return store.MaterialStatusView{}, false, fmt.Errorf("materialprep: career sources: %w", err)
	}
	role := roleSource(resolved.opportunity, resolved.company)
	shas, err := sourceShas(sources, role)
	if err != nil {
		return store.MaterialStatusView{}, false, err
	}
	drafts := make(map[string]RequiredDraft)
	if len(requiredUnset) > 0 {
		if s.Draft == nil {
			return store.MaterialStatusView{}, false, ErrUnavailable
		}
		saved, err := s.savedAnswerContext(ctx)
		if err != nil {
			return store.MaterialStatusView{}, false, err
		}
		questions := make([]DraftQuestion, 0, len(requiredUnset))
		for _, question := range requiredUnset {
			questions = append(questions, DraftQuestion{ID: question.ID, Text: question.Text,
				TextSHA256: question.TextSHA256, Kind: question.Kind})
		}
		answered := make([]AnsweredFact, 0)
		for _, question := range resolved.check.Questions {
			if value, ok := resolved.values[question.ID]; ok && value.State == store.AnswerValueStateAnswered {
				answered = append(answered, AnsweredFact{QuestionID: question.ID,
					Question: question.Text, Text: value.Text, AnswerVersion: value.Version})
			}
		}
		returned, err := s.Draft.DraftRequiredAnswers(ctx, DraftRequest{
			OpportunityID: resolved.opportunity.ID, OpportunityTitle: resolved.opportunity.Title,
			CompanyName: resolved.company.Name, CheckID: resolved.check.ID,
			RequiredUnset: questions, Answered: answered, SavedAnswers: saved,
			CareerSources: sources, Profile: resolved.profile})
		if err != nil {
			return store.MaterialStatusView{}, false, err
		}
		drafts, err = checkDrafts(requiredUnset, returned)
		if err != nil {
			return store.MaterialStatusView{}, false, err
		}
	}
	input, section, storeDrafts, err := buildInput(resolved, sources, template, drafts, requestKey)
	if err != nil {
		return store.MaterialStatusView{}, false, err
	}
	if len(storeDrafts) > 24 {
		return store.MaterialStatusView{}, false, fmt.Errorf("%w: too many drafts", store.ErrInvalid)
	}
	if view, replayed, err := s.probeReplay(ctx, actor, opportunityID, requestKey,
		expectedCheckID, expectedQuestionSetSHA256, expectedWorkflowRevision, storeDrafts, shas); err == nil {
		return view, replayed, nil
	} else if !errors.Is(err, store.ErrInvalid) {
		return store.MaterialStatusView{}, false, err
	}
	if err := applicationpacks.ValidateInput(input); err != nil {
		return store.MaterialStatusView{}, false, fmt.Errorf("%w: pack input: %v", store.ErrInvalid, err)
	}
	targets, err := citationTargets(input)
	if err != nil {
		return store.MaterialStatusView{}, false, err
	}
	if len(targets) > 0 && s.Relevance == nil {
		return store.MaterialStatusView{}, false, ErrUnavailable
	}
	for _, target := range targets {
		relevance, err := s.Relevance.AssessRelevance(ctx, target.requirement, target.source, target.excerpt)
		if err != nil {
			return store.MaterialStatusView{}, false, err
		}
		input.Draft.Relevance = append(input.Draft.Relevance, relevance)
	}
	prepared, err := s.Render.Prepare(ctx, input)
	if err != nil {
		return store.MaterialStatusView{}, false, err
	}
	manifest, err := injectMaterial(prepared.ManifestJSON, section)
	if err != nil {
		return store.MaterialStatusView{}, false, err
	}
	return s.Store.PrepareOpportunityMaterials(ctx, actor, opportunityID, store.MaterialPrepareInput{
		RequestKey: requestKey, ExpectedCheckID: expectedCheckID,
		ExpectedQuestionSetSHA256: expectedQuestionSetSHA256, ExpectedWorkflowRevision: expectedWorkflowRevision,
		Pack: store.ApplicationPackMutationInput{OpportunityID: opportunityID,
			ExpectedOpportunityRevision: resolved.opportunity.Revision, ExpectedProfileRevision: resolved.profile.Version,
			ContentSHA256: packContentHash(manifest, prepared.TypstSource, prepared.PDF),
			ManifestJSON:  manifest, TypstSource: prepared.TypstSource, PDF: prepared.PDF},
		Drafts: storeDrafts, SourceShas: shas,
	})
}
