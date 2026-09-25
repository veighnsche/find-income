package materialprep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Direct exact edits (E09). EditOpportunityMaterials saves one owner-supplied
// exact text as a new immutable material version. No Standard call and no
// Jev call exists anywhere on this path: the text is stored byte-exact, the
// store carries the base version's answer resolution into the new version
// (drafted texts survive edits), and the role returns to prepared so the
// changed version needs fresh review before any send.
//
// This file also hosts the revision helpers shared with rewrite.go: the
// revision pin loader, the deterministic pack-line builders, and the
// material-section injector for revision manifests.

const (
	// maxEditTextBytes mirrors the store edit bound: the complete new
	// material text is 1..100000 bytes of valid UTF-8.
	maxEditTextBytes = 100000
	// editSourceID pins the owner edit as an exact approved pack source so
	// the Typst focus can cite the edited bytes.
	editSourceID = "owner-edit"
	// maxEditFocusBytes bounds the edited-text prefix carried in the Typst
	// focus line, mirroring the pack line bound.
	maxEditFocusBytes = 1200
)

// revisionPins groups the verified reads one edit or rewrite revision is
// bound to. The store re-verifies every pin at commit and at probe; these
// reads only fail fast and supply the render inputs.
type revisionPins struct {
	base             store.MaterialVersionView
	check            store.CheckView
	questions        []store.CheckQuestionView
	opportunity      store.Opportunity
	company          store.Company
	profile          store.Preferences
	workflowRevision int64
	description      string
}

// loadRevisionPins loads the expected base version plus the latest checked
// check, opportunity, company, profile, and workflow revision. It compares
// nothing against the base: the store probe fences staleness before any
// render, and idempotency is checked before fences so a retry after success
// still replays.
func (s *Service) loadRevisionPins(ctx context.Context, opportunityID string, expectedVersion int64) (revisionPins, error) {
	var out revisionPins
	if opportunityID == "" || expectedVersion < 1 {
		return out, store.ErrInvalid
	}
	base, err := s.Store.OpportunityMaterialVersion(ctx, opportunityID, expectedVersion)
	if err != nil {
		return out, err
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
	opportunity, err := s.Store.Opportunity(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	if opportunity.ArchivedAt != "" {
		return out, store.ErrNotFound
	}
	company, err := s.Store.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return out, err
	}
	profile, err := s.Store.CurrentPreferences(ctx)
	if err != nil {
		return out, err
	}
	workflow, err := s.Store.RoleWorkflow(ctx, opportunityID)
	if err != nil {
		return out, err
	}
	questions := append([]store.CheckQuestionView(nil), status.Check.Questions...)
	sort.Slice(questions, func(i, j int) bool { return questions[i].Ordinal < questions[j].Ordinal })
	description, err := s.roleDescription(ctx, opportunity, status.Check.Vacancy.CaptureIDs)
	if err != nil {
		return out, err
	}
	out = revisionPins{base: base, check: *status.Check, questions: questions,
		opportunity: opportunity, company: company, profile: profile,
		workflowRevision: workflow.Revision, description: description}
	return out, nil
}

// revisionRole mirrors buildInput's role block so revision packs carry the
// same pinned role record as prepared packs.
func revisionRole(p revisionPins) applicationpacks.Role {
	destination := ""
	if p.check.Route.Judgment == store.CheckRouteJudgmentApplication && strings.TrimSpace(p.check.Route.DestinationText) != "" {
		destination = p.check.Route.DestinationText
	}
	return applicationpacks.Role{OpportunityID: p.opportunity.ID,
		OpportunityRevision: p.opportunity.Revision, ProfileRevision: p.profile.Version,
		Title: p.opportunity.Title, Company: p.company.Name, SourceURL: p.opportunity.SourceURL,
		Description: p.description, Destination: destination}
}

// roleFocusLine is the deterministic role focus citing the saved role
// record, mirroring the prepared pack focus.
func roleFocusLine(title, company string) applicationpacks.Line {
	return applicationpacks.Line{
		Text:      fmt.Sprintf("Application for %s at %s.", title, company),
		Citations: []applicationpacks.Citation{{SourceID: roleSourceID, Excerpt: title}},
	}
}

// roleCoverLine is the deterministic summary line citing the saved role
// record, mirroring the prepared pack cover.
func roleCoverLine(title, company string, answered, required, held int) applicationpacks.Line {
	return applicationpacks.Line{
		Text: fmt.Sprintf("Prepared application material for %s at %s: %d of %d required employer questions answered, %d held.",
			title, company, answered, required, held),
		Citations: []applicationpacks.Citation{{SourceID: roleSourceID, Excerpt: company}},
	}
}

// truncateBytes shortens text to at most max bytes without splitting UTF-8.
// The input must already be valid UTF-8; only the final rune can split, so
// the back-off loop runs at most three iterations.
func truncateBytes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// heldUnknowns renders one materialUnknowns entry per held question in
// ordinal order. Unknown ids fail loudly: a revision pack must never
// silently drop a held question from its immutable record.
func heldUnknowns(questions []store.CheckQuestionView, held []string) ([]string, error) {
	wanted := make(map[string]struct{}, len(held))
	for _, id := range held {
		wanted[id] = struct{}{}
	}
	var unknowns []string
	for _, question := range questions {
		if _, ok := wanted[question.ID]; !ok {
			continue
		}
		unknowns = append(unknowns, unknownsEntry(question))
		delete(wanted, question.ID)
	}
	if len(wanted) > 0 {
		return nil, fmt.Errorf("materialprep: held question not pinned")
	}
	return unknowns, nil
}

// editMaterialAnswer pins one ordinal question in a direct-edit manifest. The
// per-question texts stay in the carried resolution; only the combined text
// is new.
type editMaterialAnswer struct {
	QuestionID string `json:"questionId"`
}

// editMaterialSection is the pack-manifest binding for a direct edit. The
// store verifies checkId, questionSetSha256, origin, the byte-exact text,
// and the ordinal answer count.
type editMaterialSection struct {
	CheckID           string               `json:"checkId"`
	QuestionSetSHA256 string               `json:"questionSetSha256"`
	Origin            string               `json:"origin"`
	Text              string               `json:"text"`
	Answers           []editMaterialAnswer `json:"answers"`
}

// injectRevisionMaterial adds a revision material section to a rendered
// manifest and returns the final bytes. The base layout mirrors
// applicationpacks.Input field order with material appended, keeping
// committed bytes deterministic. It generalizes injectMaterial for the
// revision section shapes, which differ from the prepared shape.
func injectRevisionMaterial[Section any](manifest []byte, section Section) ([]byte, error) {
	var base struct {
		Role                     applicationpacks.Role        `json:"role"`
		Sources                  []applicationpacks.Source    `json:"sources"`
		Draft                    applicationpacks.Draft       `json:"draft"`
		TemplateSHA256           string                       `json:"templateSha256"`
		PreparationRequestSHA256 string                       `json:"preparationRequestSha256,omitempty"`
		Correction               *applicationpacks.Correction `json:"correction,omitempty"`
		Material                 Section                      `json:"material"`
	}
	if err := json.Unmarshal(manifest, &base); err != nil {
		return nil, err
	}
	base.Material = section
	return json.Marshal(base)
}

// buildEditInput assembles the validated pack input for one exact edit: the
// pinned role record, career plus role sources, one owner-edit source
// pinning the exact text, deterministic role lines, and one
// materialUnknowns entry per carried held question. The Typst focus carries
// the leading bytes of the edited text so short edits render into the PDF;
// the text reaches Typst only through the renderer's JSON focus string,
// never as markup, so owner markup characters stay inert by construction.
// Blank (whitespace-only) texts omit the edit source and fall back to the
// role focus, keeping the pack valid without changing the stored bytes.
func buildEditInput(p revisionPins, sources []applicationpacks.Source, template []byte, text, requestKey string) (applicationpacks.Input, editMaterialSection, error) {
	role := roleSource(p.opportunity, p.company)
	all := make([]applicationpacks.Source, 0, len(sources)+2)
	all = append(all, sources...)
	all = append(all, role)
	focusText := ""
	if strings.TrimSpace(text) != "" {
		all = append(all, applicationpacks.Source{ID: editSourceID, Name: "Owner exact edit",
			SHA256: textSHA256(text), Approved: true, Body: text})
		focusText = truncateBytes(text, maxEditFocusBytes)
	}
	if len(all) > 8 {
		return applicationpacks.Input{}, editMaterialSection{}, fmt.Errorf("%w: too many pack sources", store.ErrInvalid)
	}
	var focus applicationpacks.Line
	if strings.TrimSpace(focusText) != "" {
		focus = applicationpacks.Line{Text: focusText,
			Citations: []applicationpacks.Citation{{SourceID: editSourceID, Excerpt: focusText}}}
	} else {
		focus = roleFocusLine(p.opportunity.Title, p.company.Name)
	}
	required := 0
	for _, question := range p.questions {
		if question.Required == store.CheckRequired {
			required++
		}
	}
	answered := required - len(p.base.Readiness.MissingRequired)
	if answered < 0 {
		answered = 0
	}
	cover := roleCoverLine(p.opportunity.Title, p.company.Name, answered, required, len(p.base.Readiness.Held))
	unknowns, err := heldUnknowns(p.questions, p.base.Readiness.Held)
	if err != nil {
		return applicationpacks.Input{}, editMaterialSection{}, err
	}
	if len(unknowns) > maxMaterialUnknowns {
		return applicationpacks.Input{}, editMaterialSection{}, fmt.Errorf("%w: too many held questions", store.ErrInvalid)
	}
	section := editMaterialSection{CheckID: p.check.ID, QuestionSetSHA256: p.check.QuestionSetSHA256,
		Origin: store.MaterialOriginDirectEdit, Text: text,
		Answers: make([]editMaterialAnswer, 0, len(p.questions))}
	for _, question := range p.questions {
		section.Answers = append(section.Answers, editMaterialAnswer{QuestionID: question.ID})
	}
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

// EditOpportunityMaterials saves one exact owner edit as a new immutable
// version. It validates actor, request key, and text, probes the store for a
// replay before rendering, then renders and commits. It performs no Standard
// turn, no Jev assessment, no research, capture, fetch, or send: the Draft
// and Relevance collaborators are never touched on this path.
func (s *Service) EditOpportunityMaterials(ctx context.Context, actor store.Actor, opportunityID, requestKey string, expectedVersion int64, text string) (store.MaterialVersionView, bool, error) {
	if s == nil || s.Store == nil || s.Career == nil || s.Render == nil {
		return store.MaterialVersionView{}, false, ErrUnavailable
	}
	if actor.Kind != "administrator" || actor.ID == "" || !validRequestKey(requestKey) ||
		expectedVersion < 1 || len(text) < 1 || len(text) > maxEditTextBytes ||
		!utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return store.MaterialVersionView{}, false, store.ErrInvalid
	}
	pins, err := s.loadRevisionPins(ctx, opportunityID, expectedVersion)
	if err != nil {
		return store.MaterialVersionView{}, false, err
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
	if view, replayed, err := s.Store.EditOpportunityMaterials(ctx, actor, opportunityID, store.MaterialEditInput{
		RequestKey: requestKey, ExpectedVersion: expectedVersion, Text: text, SourceShas: shas,
	}); err == nil {
		return view, replayed, nil
	} else if !errors.Is(err, store.ErrInvalid) {
		return store.MaterialVersionView{}, false, err
	}
	input, section, err := buildEditInput(pins, sources, template, text, requestKey)
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
	return s.Store.EditOpportunityMaterials(ctx, actor, opportunityID, store.MaterialEditInput{
		RequestKey: requestKey, ExpectedVersion: expectedVersion, Text: text,
		Pack: store.ApplicationPackMutationInput{OpportunityID: opportunityID,
			ExpectedOpportunityRevision: pins.opportunity.Revision, ExpectedProfileRevision: pins.profile.Version,
			ContentSHA256: packContentHash(manifest, prepared.TypstSource, prepared.PDF),
			ManifestJSON:  manifest, TypstSource: prepared.TypstSource, PDF: prepared.PDF},
		SourceShas: shas,
	})
}
