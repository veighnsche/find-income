package materialprep_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Pinned excerpts from cv-vince-liem.md. The loader rejects changed bytes,
// so these substrings are stable.
const (
	careerExcerptMain = "Recent work spans Go services"
	careerExcerpt2    = "Development environments"
	careerExcerpt3    = "automated tests"
	careerExcerpt4    = "SodaOS — Go / Linux"
	careerExcerpt5    = "type checking and TypeScript emission"
	careerExcerpt6    = "interim team leadership"
)

const careerEvidenceID = "cv-vince-liem.md"

func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// workspaceRoot anchors the pinned career files at the test file location
// instead of the working directory.
func workspaceRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "cv-vince-liem.typ")); err != nil {
		t.Fatalf("career root %s lacks pinned sources: %v", root, err)
	}
	return root
}

type stubCareer struct {
	t        *testing.T
	calls    int
	sources  []applicationpacks.Source
	template []byte
	log      *[]string
}

func (f *stubCareer) load() ([]applicationpacks.Source, []byte, error) {
	f.calls++
	if f.log != nil {
		*f.log = append(*f.log, "career")
	}
	if f.sources == nil {
		sources, template, err := applicationpacks.LoadApprovedCareerSources(
			workspaceRoot(f.t), []string{"cv-vince-liem.typ", careerEvidenceID})
		if err != nil {
			return nil, nil, err
		}
		f.sources, f.template = sources, template
	}
	return f.sources, f.template, nil
}

type stubDrafter struct {
	calls    int
	requests []materialprep.DraftRequest
	fn       func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error)
	log      *[]string
}

func (f *stubDrafter) DraftRequiredAnswers(_ context.Context, req materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.log != nil {
		*f.log = append(*f.log, "draft")
	}
	if f.fn == nil {
		return nil, nil
	}
	return f.fn(req)
}

type stubRelevance struct {
	calls        int
	requirements []string
	excerpts     []string
	log          *[]string
}

func (f *stubRelevance) AssessRelevance(_ context.Context, requirement string, source applicationpacks.Source, excerpt string) (applicationpacks.Relevance, error) {
	f.calls++
	f.requirements = append(f.requirements, requirement)
	f.excerpts = append(f.excerpts, excerpt)
	if f.log != nil {
		*f.log = append(*f.log, "relevance:"+excerpt)
	}
	return applicationpacks.Relevance{Requirement: requirement, SourceID: source.ID,
		Scope: "relevant", Confidence: 0.9,
		InputSHA256: sha256hex([]byte(requirement + "\x00" + source.ID + "\x00" + excerpt)),
		Model:       "stub-jev-v1"}, nil
}

type stubRenderer struct {
	calls  int
	inputs []applicationpacks.Input
	log    *[]string
}

func (f *stubRenderer) Prepare(_ context.Context, input applicationpacks.Input) (applicationpacks.Prepared, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	if f.log != nil {
		*f.log = append(*f.log, "render")
	}
	manifest, _ := json.Marshal(input)
	typst := []byte("#stub typst source for material prepare tests")
	pdf := append([]byte("%PDF-1.4 stub-material\n"), bytes.Repeat([]byte("0"), 200)...)
	return applicationpacks.Prepared{ManifestJSON: manifest, TypstSource: typst, PDF: pdf}, nil
}

type prepFixture struct {
	db          *store.Store
	opportunity store.Opportunity
	check       store.CheckView
	svc         *materialprep.Service
	career      *stubCareer
	draft       *stubDrafter
	relevance   *stubRelevance
	render      *stubRenderer
}

func testOwner() store.Actor { return store.Actor{Kind: "administrator", ID: "owner"} }

func testAgent() store.Actor { return store.Actor{Kind: "agent", ID: "codex-check"} }

func setupPrep(t *testing.T, key string) prepFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner := testOwner()
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{
		CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/1", OriginalText: "Build Go services.",
		Notes: "First private tracking note", Stage: "new", WorkPattern: "hybrid",
		LocationText: "Amsterdam", PostedOn: "2026-09-20", DeadlineOn: "2026-10-20",
		Compensation: store.AdvertisedCompensation{Currency: "EUR", Period: "month", Basis: "base"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{
		RequestKey: key + "-select", ExpectedOpportunityRevision: opportunity.Revision,
		ExpectedDecisionRevision: 0, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	started, _, err := db.StartJobCheck(ctx, owner, opportunity.ID,
		store.CheckStartInput{RequestKey: key + "-check", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	var capture store.SourceCapture
	sum := sha256.Sum256([]byte("https://harbour.example/jobs/1"))
	digest := hex.EncodeToString(sum[:])
	if err := db.ResearchWrite(ctx, func(tx store.ResearchDB) error {
		capture, err = store.InsertSourceCapture(ctx, tx, store.SourceCaptureInput{
			ContentSHA256: digest, ArtifactRef: "artifact/" + digest[:16], ByteLength: 2048,
			MediaType: "text/html", OriginalURL: "https://harbour.example/jobs/1",
			Provenance: researchcontract.ProvenanceFetchedResponse, Completeness: store.CaptureComplete,
			Executor: researchcontract.ExecutorIdentity{Backend: "test-shell"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	preferences, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: key + "-round",
		Intent: "Check one selected role", Outcome: "process_input", ProfileVersion: preferences.Version,
		Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Operations: []string{store.RoundCodexTurn, store.RoundCheckSave, store.RoundJevRequest},
			Resources: []string{"campaign:active", "opportunity:" + opportunity.ID}, Delegates: []string{testAgent().ID}},
		Limits: store.RoundAllowance{Requests: 8, Items: 8, Tools: 8, Turns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if round, err = db.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, testAgent(), round.ID, store.RoundAttemptInput{
		RequestKey: "bound-turn", Operation: store.RoundCodexTurn,
		ResourceID: "opportunity:" + opportunity.ID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, testAgent().ID)
	if err != nil {
		t.Fatal(err)
	}
	questions := []store.CheckQuestionInput{
		{Text: "Why do you want this role?", Required: store.CheckRequired, Kind: store.CheckQuestionFreeText,
			SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 150, End: 180},
			SourceExcerpt: "Why do you want this role?"},
		{Text: "Describe a Go service you shipped.", Required: store.CheckRequired, Kind: store.CheckQuestionFreeText,
			SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 400, End: 436},
			SourceExcerpt: "Describe a Go service you shipped."},
		{Text: "Anything else to share?", Required: store.CheckOptional, Kind: store.CheckQuestionFreeText,
			SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 500, End: 525},
			SourceExcerpt: "Anything else to share?"},
		{Text: "Are you willing to work hybrid?", Required: store.CheckRequiredUnknown, Kind: store.CheckQuestionFreeText,
			SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 600, End: 635},
			SourceExcerpt: "Are you willing to work hybrid?"},
	}
	save := store.CheckSaveInput{OpportunityID: opportunity.ID, CheckID: started.ID,
		Vacancy: store.CheckVacancyInput{CaptureIDs: []string{capture.ID},
			Completeness: store.CaptureComplete, SourceURL: "https://harbour.example/jobs/1",
			RetrievedAt: "2026-09-24T11:05:00Z"},
		RequestedDocuments: []store.RequestedDocumentInput{{Label: "CV", Required: true,
			SourceExcerpt: "Send your CV to jobs@example.invalid",
			SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 214, End: 260}}},
		Requirements: []store.CheckRequirementInput{{Statement: "Weekend availability is required.",
			SourceExcerpt: "Weekend availability is required for this rota.",
			SourceSpan:    store.CheckSourceSpan{CaptureID: capture.ID, Start: 100, End: 149}}},
		Route: store.CheckRouteInput{Kind: store.CheckRouteDirect, DestinationText: "jobs@example.invalid",
			Judgment: store.CheckRouteJudgmentApplication, SourceExcerpt: "Send your CV to jobs@example.invalid",
			ObservedAt: "2026-09-24T11:30:00Z"},
		Gaps: []store.CheckGapInput{{Description: "Weekly hours are not stated in the vacancy text.",
			Consequential: true, Kind: store.CheckGapMissingFact}},
		Questions: questions,
		Activity: []store.CheckActivityInput{
			{Kind: "vacancy.opened", Outcome: string(researchcontract.OutcomeOK), CaptureID: capture.ID},
			{Kind: "questions.extracted", Payload: json.RawMessage(`{"count":4}`)},
		}}
	if _, _, err := db.ApplyRoundMutation(ctx, testAgent(), round.ID, store.RoundMutationInput{
		RequestKey: key + "-save", Operation: store.RoundCheckSave,
		ResourceID: "opportunity:" + opportunity.ID, ExpectedRevision: started.WorkflowRevision,
		CheckSave: &save, Capability: capability}); err != nil {
		t.Fatal(err)
	}
	status, err := db.CurrentJobCheck(ctx, opportunity.ID)
	if err != nil || status.Status != store.CheckStatusChecked || len(status.Check.Questions) != 4 {
		t.Fatalf("fixture check: %+v %v", status, err)
	}
	workflow, err := db.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow, err = db.AdvanceRoleWorkflow(ctx, opportunity.ID, workflow.Revision, store.RoleStageAnswering, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AdvanceRoleWorkflow(ctx, opportunity.ID, workflow.Revision, store.RoleStageAnswered, ""); err != nil {
		t.Fatal(err)
	}
	f := prepFixture{db: db, opportunity: opportunity, check: *status.Check,
		career: &stubCareer{t: t},
		draft:  &stubDrafter{}, relevance: &stubRelevance{}, render: &stubRenderer{}}
	f.svc = &materialprep.Service{Store: db, Career: f.career.load,
		Draft: f.draft, Relevance: f.relevance, Render: f.render}
	return f
}

func prepPins(t *testing.T, f prepFixture) (checkID, questionSet string, workflowRev int64) {
	t.Helper()
	status, err := f.db.CurrentJobCheck(context.Background(), f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := f.db.RoleWorkflow(context.Background(), f.opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	return status.Check.ID, status.Check.QuestionSetSHA256, workflow.Revision
}

func savePrepAnswer(t *testing.T, f prepFixture, ordinal int, text string) store.QuestionAnswerValue {
	t.Helper()
	value, err := f.db.SaveAnswerValue(context.Background(), testOwner(), f.opportunity.ID,
		f.check.Questions[ordinal].ID, store.AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type manifestMaterial struct {
	CheckID           string `json:"checkId"`
	QuestionSetSHA256 string `json:"questionSetSha256"`
	Origin            string `json:"origin"`
	Answers           []struct {
		QuestionID    string `json:"questionId"`
		State         string `json:"state"`
		AnswerVersion int64  `json:"answerVersion"`
		TextSHA256    string `json:"textSha256"`
		Text          string `json:"text"`
	} `json:"answers"`
}

type committedManifest struct {
	Role struct {
		OpportunityID string `json:"opportunityId"`
		Destination   string `json:"destination"`
	} `json:"role"`
	PreparationRequestSHA256 string `json:"preparationRequestSha256"`
	Draft                    struct {
		MaterialUnknowns []string `json:"materialUnknowns"`
		Relevance        []struct {
			Requirement string `json:"requirement"`
			SourceID    string `json:"sourceId"`
		} `json:"relevance"`
	} `json:"draft"`
	Material manifestMaterial `json:"material"`
}

func readCommittedManifest(t *testing.T, f prepFixture, packID string) ([]byte, committedManifest) {
	t.Helper()
	pack, err := f.db.ApplicationPack(context.Background(), packID)
	if err != nil {
		t.Fatal(err)
	}
	var manifest committedManifest
	if err := json.Unmarshal(pack.ManifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	return pack.ManifestJSON, manifest
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func TestPrepareAnsweredRoleCommitsWithoutCodex(t *testing.T) {
	f := setupPrep(t, "prep-nocodex")
	ctx := context.Background()
	first := savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	second := savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-nocodex-key", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	if view.Status != store.MaterialStatusPrepared || view.Current == nil || view.Current.Version != 1 {
		t.Fatalf("prepared status: %+v", view)
	}
	if f.draft.calls != 0 {
		t.Fatalf("Codex turn ran with zero required+unset: %d", f.draft.calls)
	}
	if f.career.calls != 1 || f.render.calls != 1 {
		t.Fatalf("career=%d render=%d, want 1/1", f.career.calls, f.render.calls)
	}
	if f.relevance.calls != 2 {
		t.Fatalf("relevance calls=%d, want 2 (role title + company)", f.relevance.calls)
	}
	if err := applicationpacks.ValidateInput(f.render.inputs[0]); err != nil {
		t.Fatalf("rendered input invalid: %v", err)
	}
	if len(f.render.inputs[0].Draft.Relevance) != 2 {
		t.Fatalf("relevance entries=%d, want 2", len(f.render.inputs[0].Draft.Relevance))
	}
	current := view.Current
	if current.CheckID != checkID || current.QuestionSetSHA256 != questionSet ||
		current.Provenance.Origin != store.MaterialOriginPrepared || len(current.Answers) != 4 {
		t.Fatalf("version pins: %+v", current)
	}
	if current.Answers[0].AnswerVersion != first.Version || current.Answers[0].TextSHA256 != first.TextSHA256 ||
		current.Answers[1].AnswerVersion != second.Version || current.Answers[1].TextSHA256 != second.TextSHA256 {
		t.Fatalf("carried refs: %+v", current.Answers)
	}
	wantShas := map[string]bool{
		f.career.sources[0].SHA256: true, f.career.sources[1].SHA256: true,
		questionSet: true, first.TextSHA256: true, second.TextSHA256: true,
	}
	if len(current.Provenance.SourceShas) != len(wantShas)+1 {
		t.Fatalf("source shas: %v", current.Provenance.SourceShas)
	}
	if !sort.StringsAreSorted(current.Provenance.SourceShas) {
		t.Fatalf("source shas unsorted: %v", current.Provenance.SourceShas)
	}
	for _, sha := range current.Provenance.SourceShas {
		if len(sha) != 64 {
			t.Fatalf("bad source sha: %q", sha)
		}
		delete(wantShas, sha)
	}
	if len(wantShas) != 0 {
		t.Fatalf("missing source shas: %v", wantShas)
	}
	_, manifest := readCommittedManifest(t, f, current.PackID)
	if len(manifest.Draft.MaterialUnknowns) != 0 {
		t.Fatalf("unknowns on prepared pack: %v", manifest.Draft.MaterialUnknowns)
	}
	if manifest.Material.Origin != store.MaterialOriginPrepared || len(manifest.Material.Answers) != 4 {
		t.Fatalf("material section: %+v", manifest.Material)
	}
	for i, entry := range manifest.Material.Answers {
		if entry.QuestionID != f.check.Questions[i].ID {
			t.Fatalf("material order at %d: %q", i, entry.QuestionID)
		}
	}
	if manifest.Material.Answers[0].State != "answered" || manifest.Material.Answers[0].Text != first.Text ||
		manifest.Material.Answers[1].State != "answered" || manifest.Material.Answers[2].State != "blank" ||
		manifest.Material.Answers[3].State != "blank" {
		t.Fatalf("material states: %+v", manifest.Material.Answers)
	}
	answers, err := f.db.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || len(answers.Values) != 2 {
		t.Fatalf("prepare touched owner answers: %+v %v", answers, err)
	}
}

func TestPrepareDraftsRequiredUnsetInOneTurn(t *testing.T) {
	f := setupPrep(t, "prep-draft")
	ctx := context.Background()
	saved := savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	if _, _, err := f.db.CreateSavedAnswer(ctx, testOwner(), store.SavedAnswerCreateInput{
		RequestKey: "prep-draft-library", Text: "Approved library phrasing.",
		ScopeTags: []string{"motivation"}}); err != nil {
		t.Fatal(err)
	}
	target := f.check.Questions[1]
	draftLine := applicationpacks.Line{Text: "I shipped a billing service in Go last year.",
		Citations: []applicationpacks.Citation{{SourceID: careerEvidenceID, Excerpt: careerExcerptMain}}}
	f.draft.fn = func(req materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
		return []materialprep.RequiredDraft{{QuestionID: target.ID, Lines: []applicationpacks.Line{draftLine}}}, nil
	}
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-draft-key", checkID, questionSet, workflowRev)
	if err != nil || !created || view.Status != store.MaterialStatusPrepared {
		t.Fatalf("drafted prepare: %+v %v", view, err)
	}
	if f.draft.calls != 1 {
		t.Fatalf("Codex turns=%d, want exactly 1", f.draft.calls)
	}
	req := f.draft.requests[0]
	if len(req.RequiredUnset) != 1 || req.RequiredUnset[0].ID != target.ID {
		t.Fatalf("draft scope: %+v", req.RequiredUnset)
	}
	if len(req.Answered) != 1 || req.Answered[0].Text != saved.Text {
		t.Fatalf("draft answered facts: %+v", req.Answered)
	}
	if len(req.SavedAnswers) != 1 || req.SavedAnswers[0].Text != "Approved library phrasing." {
		t.Fatalf("draft library facts: %+v", req.SavedAnswers)
	}
	if len(req.CareerSources) != 2 || req.Profile.Version < 1 {
		t.Fatalf("draft career/profile facts: %d sources v%d", len(req.CareerSources), req.Profile.Version)
	}
	wantDraft := "I shipped a billing service in Go last year."
	ref := view.Current.Answers[1]
	if ref.AnswerVersion != 0 || ref.TextSHA256 != sha256hex([]byte(wantDraft)) {
		t.Fatalf("drafted ref: %+v", ref)
	}
	if f.relevance.calls != 3 {
		t.Fatalf("relevance calls=%d, want 3", f.relevance.calls)
	}
	if err := applicationpacks.ValidateInput(f.render.inputs[0]); err != nil {
		t.Fatalf("rendered input invalid: %v", err)
	}
	_, manifest := readCommittedManifest(t, f, view.Current.PackID)
	entry := manifest.Material.Answers[1]
	if entry.State != "drafted" || entry.Text != wantDraft || entry.AnswerVersion != 0 {
		t.Fatalf("drafted material entry: %+v", entry)
	}
	answers, err := f.db.CurrentQuestionAnswers(ctx, f.opportunity.ID)
	if err != nil || len(answers.Values) != 1 {
		t.Fatalf("draft entered owner answers: %+v %v", answers, err)
	}
}

func TestPrepareHeldWhenDrafterOmits(t *testing.T) {
	f := setupPrep(t, "prep-held")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	heldID := f.check.Questions[1].ID
	f.draft.fn = func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) { return nil, nil }
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-held-key", checkID, questionSet, workflowRev)
	if err != nil || !created || view.Status != store.MaterialStatusHeld {
		t.Fatalf("held prepare: %+v %v", view, err)
	}
	readiness := view.Current.Readiness
	if readiness.Ready || len(readiness.MissingRequired) != 1 || readiness.MissingRequired[0] != heldID ||
		len(readiness.Held) != 1 || readiness.Held[0] != heldID {
		t.Fatalf("held readiness: %+v", readiness)
	}
	_, manifest := readCommittedManifest(t, f, view.Current.PackID)
	if len(manifest.Draft.MaterialUnknowns) != 1 ||
		!strings.Contains(manifest.Draft.MaterialUnknowns[0], "Describe a Go service you shipped.") {
		t.Fatalf("held unknowns: %v", manifest.Draft.MaterialUnknowns)
	}
	if manifest.Material.Answers[1].State != "held" || manifest.Material.Answers[1].Text != "" {
		t.Fatalf("held material entry: %+v", manifest.Material.Answers[1])
	}
}

func TestPrepareBlankRequiredMissingNotHeld(t *testing.T) {
	f := setupPrep(t, "prep-blank")
	ctx := context.Background()
	blanked := savePrepAnswer(t, f, 0, "")
	if blanked.State != store.AnswerValueStateBlank {
		t.Fatalf("blank fixture: %+v", blanked)
	}
	savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-blank-key", checkID, questionSet, workflowRev)
	if err != nil || !created || view.Status != store.MaterialStatusHeld {
		t.Fatalf("blank prepare: %+v %v", view, err)
	}
	if f.draft.calls != 0 {
		t.Fatalf("Codex turn ran for blank+answered: %d", f.draft.calls)
	}
	readiness := view.Current.Readiness
	if len(readiness.MissingRequired) != 1 || len(readiness.Held) != 0 {
		t.Fatalf("blank readiness: %+v", readiness)
	}
	_, manifest := readCommittedManifest(t, f, view.Current.PackID)
	if len(manifest.Draft.MaterialUnknowns) != 1 {
		t.Fatalf("blank unknowns: %v", manifest.Draft.MaterialUnknowns)
	}
	if manifest.Material.Answers[0].State != "blank" || manifest.Material.Answers[0].AnswerVersion != blanked.Version {
		t.Fatalf("blank material entry: %+v", manifest.Material.Answers[0])
	}
}

func TestPreparePinFencesPrecedeSpend(t *testing.T) {
	f := setupPrep(t, "prep-fence")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
	checkID, questionSet, workflowRev := prepPins(t, f)
	zeros := strings.Repeat("0", 64)
	cases := []struct {
		name  string
		actor store.Actor
		opp   string
		key   string
		check string
		set   string
		rev   int64
		want  error
	}{
		{"check id", testOwner(), f.opportunity.ID, "k-check", "other-check", questionSet, workflowRev, store.ErrConflict},
		{"question set", testOwner(), f.opportunity.ID, "k-set", checkID, zeros, workflowRev, store.ErrConflict},
		{"workflow rev", testOwner(), f.opportunity.ID, "k-rev", checkID, questionSet, workflowRev + 1, store.ErrConflict},
		{"empty key", testOwner(), f.opportunity.ID, "", checkID, questionSet, workflowRev, store.ErrInvalid},
		{"bad actor", testAgent(), f.opportunity.ID, "k-actor", checkID, questionSet, workflowRev, store.ErrInvalid},
		{"missing role", testOwner(), "no-such-role", "k-missing", checkID, questionSet, workflowRev, store.ErrNotFound},
	}
	for _, tc := range cases {
		draftCalls, relevanceCalls, renderCalls := f.draft.calls, f.relevance.calls, f.render.calls
		_, _, err := f.svc.PrepareOpportunityMaterials(ctx, tc.actor, tc.opp, tc.key, tc.check, tc.set, tc.rev)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: want %v got %v", tc.name, tc.want, err)
		}
		if f.draft.calls != draftCalls || f.relevance.calls != relevanceCalls || f.render.calls != renderCalls {
			t.Fatalf("%s: spent after fence (draft=%d relevance=%d render=%d)",
				tc.name, f.draft.calls, f.relevance.calls, f.render.calls)
		}
	}
	fresh, _, err := f.db.CreateOpportunity(ctx, testOwner(), store.OpportunityInput{
		CompanyID: f.opportunity.CompanyID, Title: "Unselected", Kind: "employment", Stage: "new",
		SourceURL: "https://harbour.example/jobs/9", OriginalText: "Unselected role."})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.svc.PrepareOpportunityMaterials(ctx, testOwner(), fresh.ID, "k-unselected", checkID, questionSet, 0)
	if !errors.Is(err, store.ErrRoleNotSelected) {
		t.Fatalf("unselected role: %v", err)
	}
}

func TestPrepareReplaySkipsRender(t *testing.T) {
	f := setupPrep(t, "prep-replay")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
	checkID, questionSet, workflowRev := prepPins(t, f)
	first, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-replay-key", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("first prepare: %+v %v", first, err)
	}
	// Retry with the now-stale pins, as after a lost response: the store
	// digest pins resolved state, so the same key replays without render.
	second, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-replay-key", checkID, questionSet, workflowRev)
	if err != nil || created {
		t.Fatalf("replay: %+v %v", second, err)
	}
	if second.Current.Version != 1 || second.Current.PackID != first.Current.PackID ||
		second.Status != first.Status {
		t.Fatalf("replay diverged: %+v vs %+v", second, first)
	}
	if f.render.calls != 1 || f.draft.calls != 0 {
		t.Fatalf("replay spent: render=%d draft=%d", f.render.calls, f.draft.calls)
	}
	if _, err := f.db.SaveAnswerValue(ctx, testOwner(), f.opportunity.ID, f.check.Questions[0].ID,
		store.AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: "Changed text."}); err != nil {
		t.Fatal(err)
	}
	_, _, freshRev := prepPins(t, f)
	_, _, err = f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-replay-key", checkID, questionSet, freshRev)
	if !errors.Is(err, store.ErrRoundIdempotencyConflict) {
		t.Fatalf("reused key with changed answers: %v", err)
	}
	if f.render.calls != 1 {
		t.Fatalf("conflict rendered: %d", f.render.calls)
	}
	third, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-replay-key-2", checkID, questionSet, freshRev)
	if err != nil || !created || third.Current.Version != 2 {
		t.Fatalf("second version: %+v %v", third, err)
	}
}

func TestPrepareStalePinsWithDraftsNeedsRefresh(t *testing.T) {
	f := setupPrep(t, "prep-stale")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	target := f.check.Questions[1]
	f.draft.fn = func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
		return []materialprep.RequiredDraft{{QuestionID: target.ID, Lines: []applicationpacks.Line{
			{Text: "I shipped a billing service in Go last year.",
				Citations: []applicationpacks.Citation{{SourceID: careerEvidenceID, Excerpt: careerExcerptMain}}}}}}, nil
	}
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-stale-key", checkID, questionSet, workflowRev); err != nil || !created {
		t.Fatalf("first prepare: %v", err)
	}
	// Stale pins with drafts outstanding: no cheap replay is possible, so
	// the fast 409 stands and no second Codex turn runs.
	_, _, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-stale-key", checkID, questionSet, workflowRev)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale draft retry: %v", err)
	}
	if f.draft.calls != 1 || f.render.calls != 1 {
		t.Fatalf("stale retry spent: draft=%d render=%d", f.draft.calls, f.render.calls)
	}
	_, _, freshRev := prepPins(t, f)
	second, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-stale-key", checkID, questionSet, freshRev)
	if err != nil || created || second.Current.Version != 1 {
		t.Fatalf("refreshed replay: %+v %v", second, err)
	}
	if f.render.calls != 1 {
		t.Fatalf("refreshed replay rendered: %d", f.render.calls)
	}
}

func TestPrepareToolFencingCallLog(t *testing.T) {
	f := setupPrep(t, "prep-fencing")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	target := f.check.Questions[1]
	var log []string
	f.career.log, f.draft.log, f.relevance.log, f.render.log = &log, &log, &log, &log
	f.draft.fn = func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
		return []materialprep.RequiredDraft{{QuestionID: target.ID, Lines: []applicationpacks.Line{
			{Text: "I shipped a billing service in Go last year.",
				Citations: []applicationpacks.Citation{{SourceID: careerEvidenceID, Excerpt: careerExcerptMain}}}}}}, nil
	}
	checkID, questionSet, workflowRev := prepPins(t, f)
	if _, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-fencing-key", checkID, questionSet, workflowRev); err != nil || !created {
		t.Fatalf("prepare: %v", err)
	}
	want := []string{"career", "draft", "relevance:Backend Engineer",
		"relevance:Harbour Systems", "relevance:" + careerExcerptMain, "render"}
	if len(log) != len(want) {
		t.Fatalf("call log: %v", log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("call log: %v, want %v", log, want)
		}
	}
	allowed := map[string]bool{"career": true, "draft": true, "render": true}
	for _, entry := range log {
		if strings.HasPrefix(entry, "relevance:") || allowed[entry] {
			continue
		}
		t.Fatalf("call outside fenced tools: %q", entry)
	}
}

func TestPrepareUnavailableDeps(t *testing.T) {
	setup := func(t *testing.T, key string, answeredBoth bool) prepFixture {
		t.Helper()
		f := setupPrep(t, key)
		savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
		if answeredBoth {
			savePrepAnswer(t, f, 1, "I shipped a billing service in Go.")
		}
		return f
	}
	t.Run("nil store", func(t *testing.T) {
		f := setup(t, "prep-unavail-1", true)
		svc := &materialprep.Service{Career: f.career.load, Draft: f.draft, Relevance: f.relevance, Render: f.render}
		_, _, err := svc.PrepareOpportunityMaterials(context.Background(), testOwner(), f.opportunity.ID, "k", "c", strings.Repeat("0", 64), 0)
		if !errors.Is(err, materialprep.ErrUnavailable) {
			t.Fatalf("nil store: %v", err)
		}
	})
	t.Run("nil renderer", func(t *testing.T) {
		f := setup(t, "prep-unavail-2", true)
		svc := &materialprep.Service{Store: f.db, Career: f.career.load, Draft: f.draft, Relevance: f.relevance}
		checkID, questionSet, workflowRev := prepPins(t, f)
		_, _, err := svc.PrepareOpportunityMaterials(context.Background(), testOwner(), f.opportunity.ID, "k", checkID, questionSet, workflowRev)
		if !errors.Is(err, materialprep.ErrUnavailable) {
			t.Fatalf("nil renderer: %v", err)
		}
	})
	t.Run("drafts needed without drafter", func(t *testing.T) {
		f := setup(t, "prep-unavail-3", false)
		svc := &materialprep.Service{Store: f.db, Career: f.career.load, Relevance: f.relevance, Render: f.render}
		checkID, questionSet, workflowRev := prepPins(t, f)
		_, _, err := svc.PrepareOpportunityMaterials(context.Background(), testOwner(), f.opportunity.ID, "k", checkID, questionSet, workflowRev)
		if !errors.Is(err, materialprep.ErrUnavailable) {
			t.Fatalf("nil drafter: %v", err)
		}
		if f.render.calls != 0 {
			t.Fatalf("rendered without drafter: %d", f.render.calls)
		}
	})
	t.Run("no codex path without drafter", func(t *testing.T) {
		f := setup(t, "prep-unavail-4", true)
		svc := &materialprep.Service{Store: f.db, Career: f.career.load, Relevance: f.relevance, Render: f.render}
		checkID, questionSet, workflowRev := prepPins(t, f)
		view, created, err := svc.PrepareOpportunityMaterials(context.Background(), testOwner(), f.opportunity.ID, "k", checkID, questionSet, workflowRev)
		if err != nil || !created || view.Status != store.MaterialStatusPrepared {
			t.Fatalf("nil drafter on answered role: %+v %v", view, err)
		}
	})
	t.Run("nil relevance", func(t *testing.T) {
		f := setup(t, "prep-unavail-5", true)
		svc := &materialprep.Service{Store: f.db, Career: f.career.load, Draft: f.draft, Render: f.render}
		checkID, questionSet, workflowRev := prepPins(t, f)
		_, _, err := svc.PrepareOpportunityMaterials(context.Background(), testOwner(), f.opportunity.ID, "k", checkID, questionSet, workflowRev)
		if !errors.Is(err, materialprep.ErrUnavailable) {
			t.Fatalf("nil relevance: %v", err)
		}
		if f.render.calls != 0 {
			t.Fatalf("rendered without relevance: %d", f.render.calls)
		}
	})
}

func TestPrepareCitationBudget(t *testing.T) {
	f := setupPrep(t, "prep-cite")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	target := f.check.Questions[1]
	excerpts := []string{careerExcerptMain, careerExcerpt2, careerExcerpt3, careerExcerpt4, careerExcerpt5}
	lines := make([]applicationpacks.Line, 0, len(excerpts))
	for _, excerpt := range excerpts {
		lines = append(lines, applicationpacks.Line{Text: "Draft line citing " + excerpt + ".",
			Citations: []applicationpacks.Citation{{SourceID: careerEvidenceID, Excerpt: excerpt}}})
	}
	f.draft.fn = func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
		return []materialprep.RequiredDraft{{QuestionID: target.ID, Lines: lines}}, nil
	}
	checkID, questionSet, workflowRev := prepPins(t, f)
	_, _, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-cite-key", checkID, questionSet, workflowRev)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("seven citations: %v", err)
	}
	if f.relevance.calls != 0 || f.render.calls != 0 {
		t.Fatalf("spent past budget: relevance=%d render=%d", f.relevance.calls, f.render.calls)
	}
}

func TestPrepareDraftScopeFencing(t *testing.T) {
	f := setupPrep(t, "prep-scope")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	optional := f.check.Questions[2]
	f.draft.fn = func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
		return []materialprep.RequiredDraft{{QuestionID: optional.ID, Lines: []applicationpacks.Line{
			{Text: "Out-of-scope draft.",
				Citations: []applicationpacks.Citation{{SourceID: careerEvidenceID, Excerpt: careerExcerpt3}}}}}}, nil
	}
	checkID, questionSet, workflowRev := prepPins(t, f)
	_, _, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-scope-key", checkID, questionSet, workflowRev)
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("out-of-scope draft: %v", err)
	}
	if f.relevance.calls != 0 || f.render.calls != 0 {
		t.Fatalf("spent past fencing: relevance=%d render=%d", f.relevance.calls, f.render.calls)
	}
}

func normalizeManifestIDs(t *testing.T, f prepFixture, raw []byte) string {
	t.Helper()
	out := string(raw)
	out = strings.ReplaceAll(out, f.opportunity.ID, "opp")
	out = strings.ReplaceAll(out, f.check.ID, "chk")
	for i, question := range f.check.Questions {
		out = strings.ReplaceAll(out, question.ID, "q"+string(rune('0'+i)))
	}
	return out
}

func TestPrepareManifestGolden(t *testing.T) {
	f := setupPrep(t, "prep-golden")
	ctx := context.Background()
	savePrepAnswer(t, f, 0, "I want this role for its Go platform work.")
	target := f.check.Questions[1]
	f.draft.fn = func(materialprep.DraftRequest) ([]materialprep.RequiredDraft, error) {
		return []materialprep.RequiredDraft{{QuestionID: target.ID, Lines: []applicationpacks.Line{
			{Text: "I shipped a billing service in Go last year.",
				Citations: []applicationpacks.Citation{{SourceID: careerEvidenceID, Excerpt: careerExcerptMain}}}}}}, nil
	}
	checkID, questionSet, workflowRev := prepPins(t, f)
	view, created, err := f.svc.PrepareOpportunityMaterials(ctx, testOwner(), f.opportunity.ID,
		"prep-golden-key", checkID, questionSet, workflowRev)
	if err != nil || !created {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	raw, parsed := readCommittedManifest(t, f, view.Current.PackID)
	if parsed.Material.QuestionSetSHA256 != questionSet {
		t.Fatalf("material question set: %q", parsed.Material.QuestionSetSHA256)
	}
	got := normalizeManifestIDs(t, f, raw)
	// The question-set sha covers random capture ids, so it and the request
	// sha derived from it are normalized; the pin itself is asserted above.
	got = strings.ReplaceAll(got, questionSet, strings.Repeat("s", 64))
	got = strings.ReplaceAll(got, parsed.PreparationRequestSHA256, strings.Repeat("r", 64))
	if got != materialManifestGolden {
		t.Logf("manifest:\n%s", got)
		t.Fatalf("manifest golden mismatch (%d bytes vs %d)", len(got), len(materialManifestGolden))
	}
}

const materialManifestGolden = `{"role":{"opportunityId":"opp","opportunityRevision":1,"profileRevision":1,"title":"Backend Engineer","company":"Harbour Systems","sourceUrl":"https://harbour.example/jobs/1","description":"Build Go services.","destination":"jobs@example.invalid"},"sources":[{"id":"cv-vince-liem.typ","name":"cv-vince-liem.typ","sha256":"e9643864392f2aff7f900a82714a8feb573f636c24c62e7a169c29de41bc9a57","approved":true,"body":"// Application CV. Research and rationale: cv-design-research.md\n// Build: typst compile cv-vince-liem.typ cv-vince-liem.pdf\n#let ink = rgb(\"#202020\")\n#let muted = rgb(\"#484848\")\n\n#set document(title: \"Vince Liem — Senior Software Engineer\", author: \"Vince Liem\")\n#set page(\n  paper: \"a4\",\n  margin: (left: 18mm, right: 18mm, top: 15mm, bottom: 18mm),\n  footer: context align(right)[#text(size: 9pt, fill: muted)[#counter(page).display()]],\n)\n#set text(font: \"Arial\", size: 10.8pt, fill: ink, lang: \"en\")\n#set par(leading: 4pt, spacing: 5pt)\n#set list(indent: 10pt, body-indent: 3pt, tight: false, spacing: 3pt)\n#show link: set text(fill: ink)\n\n#let section(title) = block(above: 11pt, below: 5pt, sticky: true)[\n  #text(size: 11.2pt, weight: 700)[#title]\n]\n\n#let role(title, organisation, dates, body) = block(breakable: false, above: 11pt, below: 6pt)[\n  #text(size: 10.8pt, weight: 700)[#title — #organisation]\n  #v(2pt)\n  #text(size: 10pt, fill: muted)[#dates]\n  #v(3pt)\n  #body\n]\n\n#text(size: 23pt, weight: 700)[Vince Liem]\n#v(2pt)\n#text(size: 11.5pt, weight: 700)[Senior Software Engineer | Systems, Platforms \u0026 AI Tooling]\n#v(4pt)\nAmsterdam, Netherlands | 32 hours/week | Remote or hybrid \\\n#link(\"mailto:vinch@mailbox.org\")[vinch\\@mailbox.org] | +31 6 303 91 355 | #link(\"https://github.com/veighnsche\")[github.com/veighnsche]\n\n#section[Professional Summary]\nSoftware engineer building developer platforms, language tools and AI integrations. Recent work spans Go services, a compiler targeting TypeScript/Bun, Rust systems utilities and Python MCP tooling. Brings enterprise delivery experience for NN Group and Stedin through iO Digital, plus interim team leadership. Seeking backend or platform work.\n\n#section[Technical Skills]\n*Recent project stack:* Go, Rust, TypeScript/Bun, Python, Linux, Fedora CoreOS, Podman, systemd, SQLite, HTTP APIs and MCP. \\\n*Engineering focus:* Development environments, language tooling, service integration, boot/update lifecycle and AI infrastructure. \\\n*Delivery:* AI-assisted implementation, explicit contracts, automated tests, Git, CI, code review, technical mentoring and documentation.\n\n#section[Personal Engineering Projects]\n#role[Systems, Platforms \u0026 Developer Tooling][VINCH][September 2024–present | Personal projects; selected work below from 2026][\n  - #link(\"https://github.com/LevitateOS/sodaos\")[*SodaOS — Go / Linux:*] Build persistent development environments on Fedora CoreOS with Podman, a Go environment/access API, OAuth and SQLite-backed grants.\n  - #link(\"https://github.com/veighnsche/can-lang\")[*Can — Go / TypeScript / Bun:*] Develop a language compiler with parsing, type checking and TypeScript emission; executable assertions gate build publication.\n  - #link(\"https://github.com/LevitateOS/recab\")[*recab — Rust:*] Implement A/B boot-slot management with trial boot, commit and rollback, systemd-boot integration and persistent state.\n  - #link(\"https://github.com/veighnsche/calendar\")[*Calendar adapter — Go:*] Expose CalDAV operations through HTTP JSON and MCP, sharing validation, dry-run behaviour and ETag conflict handling.\n  - #link(\"https://github.com/veighnsche/qemu-screenshot-mcp\")[*QEMU screenshot MCP — Python:*] Build VM launch, QMP screenshot capture and shutdown tooling for AI agents.\n]\n\n#section[Professional Experience]\n#role[Software Engineering Consultancy \u0026 Interim Team Leadership][iO Digital][2021–2024 | Hybrid][\n  - *NN Group:* Integrated CMS and REST services into React/Gatsby blog applications; built shared components and documentation with Storybook.\n  - *NN Group and Stedin:* Integrated CRM and backend services into helpdesk and customer self-service applications using React and Next.js; implemented accessibility requirements at NN Group.\n  - *iO internal agency:* Led the application UI team during a transition, providing code reviews, onboarding, workshops, sprint planning and architectural guidance.\n]\n\n#pagebreak()\n\n#text(size: 11pt, weight: 700)[Vince Liem — Professional Experience, continued]\n#v(5pt)\n\n#role[Software Development][CryptoHopper BV][January–July 2022 | Amsterdam][\n  - Collaborated with backend engineers on API contracts and data flows for a trading dashboard; integrated backend APIs into React components.\n  - Documented components with Storybook and introduced Agile working practices.\n]\n\n#role[Software Development][Sidekick IT][2018–2021 | Breda][\n  - Developed Angular/TypeScript applications integrated with backend APIs.\n  - Built reusable components, added Jest and Cypress testing, and documented implementation conventions.\n  - *Horizon secondment:* Served as lead developer on a healthcare platform, contributing Angular, React and Flutter interfaces and blockchain integrations.\n]\n\n#role[Software Development][Athena Studies BV][2018–2019 | Amsterdam][\n  - Integrated legacy APIs into a React/Redux employee portal, implementing financial-overview functionality and automating parts of a sales workflow.\n]\n\n#role[Business Application Development][Iuppiter BV][2017–2018 | Rotterdam][\n  - Independently developed a sales and warehouse portal with PHP/Symfony, Doctrine and MySQL.\n  - Translated business processes into application requirements, inventory tracking, sales workflows and a relational data model.\n  - Documented the application for maintenance and handover after moving into development from account management.\n]\n\n#section[Education]\n*Codaisseur* — Full-stack Developer programme, 2018. \\\n*Turing Society* — Python, JavaScript and TensorFlow, 2017–2018. \\\n*Albeda College* — Marketing \u0026 Communication, 2013.\n\n#section[Languages]\nDutch: native. English: fluent (C1).\n"},{"id":"cv-vince-liem.md","name":"cv-vince-liem.md","sha256":"eaf82b8442ac51007b83397279d7f16e0f4a8547bd63f340253ad8263a9ddf61","approved":true,"body":"# Vince Liem\n\n**Senior Software Engineer — Systems · Platforms · AI Tooling**\n\nAmsterdam, Netherlands · 32 hours per week · Remote or hybrid  \nvinch@mailbox.org · +31 6 303 91 355 · https://github.com/veighnsche\n\n## Profile\n\nSoftware engineer building developer platforms, language tools and AI integrations. Recent work spans Go services, a compiler targeting TypeScript/Bun, Rust systems utilities and Python MCP tooling. Brings enterprise delivery experience for NN Group and Stedin through iO Digital, plus interim team leadership. Seeking backend or platform work.\n\n## Core expertise\n\n- **Recent project stack:** Go, Rust, TypeScript/Bun, Python, Linux, Fedora CoreOS, Podman, systemd, SQLite, HTTP APIs and MCP.\n- **Engineering focus:** Development environments, language tooling, service integration, boot/update lifecycle and AI infrastructure.\n- **Delivery:** AI-assisted implementation, explicit contracts, automated tests, Git, CI, code review, technical mentoring and documentation.\n\n## Personal engineering projects\n\n### Systems, Platforms \u0026 Developer Tooling — VINCH\n**September 2024–present · Personal projects; selected work below from 2026**\n\n- [SodaOS — Go / Linux](https://github.com/LevitateOS/sodaos): Build persistent development environments on Fedora CoreOS with Podman, a Go environment/access API, OAuth and SQLite-backed grants.\n- [Can — Go / TypeScript / Bun](https://github.com/veighnsche/can-lang): Develop a language compiler with parsing, type checking and TypeScript emission; executable assertions gate build publication.\n- [recab — Rust](https://github.com/LevitateOS/recab): Implement A/B boot-slot management with trial boot, commit and rollback, systemd-boot integration and persistent state.\n- [Calendar adapter — Go](https://github.com/veighnsche/calendar): Expose CalDAV operations through HTTP JSON and MCP, sharing validation, dry-run behaviour and ETag conflict handling.\n- [QEMU screenshot MCP — Python](https://github.com/veighnsche/qemu-screenshot-mcp): Build VM launch, QMP screenshot capture and shutdown tooling for AI agents.\n\n## Professional experience\n\n### Software Engineering Consultancy \u0026 Interim Team Leadership — iO Digital\n**2021–2024 · Hybrid**\n\n- **NN Group:** Integrated CMS and REST services into React/Gatsby blog applications; built shared components and documentation with Storybook.\n- **NN Group and Stedin:** Integrated CRM and backend services into helpdesk and customer self-service applications using React and Next.js; implemented accessibility requirements at NN Group.\n- **iO internal agency:** Led the application UI team during a transition, providing code reviews, onboarding, workshops, sprint planning and architectural guidance.\n\n### Software Development — CryptoHopper BV\n**January–July 2022 · Amsterdam**\n\n- Collaborated with backend engineers on API contracts and data flows for a trading dashboard; integrated backend APIs into React components.\n- Documented components with Storybook and introduced Agile working practices.\n\n### Software Development — Sidekick IT\n**2018–2021 · Breda**\n\n- Developed Angular/TypeScript applications integrated with backend APIs.\n- Built reusable components, added Jest and Cypress testing, and documented implementation conventions.\n- **Horizon secondment:** Served as lead developer on a healthcare platform, contributing Angular, React and Flutter interfaces and blockchain integrations.\n\n### Software Development — Athena Studies BV\n**2018–2019 · Amsterdam**\n\n- Integrated legacy APIs into a React/Redux employee portal, implementing financial-overview functionality and automating parts of a sales workflow.\n\n### Business Application Development — Iuppiter BV\n**2017–2018 · Rotterdam**\n\n- Independently developed a sales and warehouse portal using PHP/Symfony, Doctrine and MySQL.\n- Translated operational processes into application requirements, inventory tracking, sales workflows and a relational data model.\n- Documented the application for maintenance and handover; moved into development from an account-management role.\n\n## Education and languages\n\n- **Codaisseur**, Full-stack Developer programme — 2018.\n- **Turing Society**, Python, JavaScript and TensorFlow — 2017–2018.\n- **Albeda College**, Marketing \u0026 Communication — 2013.\n- **Dutch:** Native. **English:** Fluent (C1).\n"},{"id":"role-description","name":"Saved role record","sha256":"010cd957991aeffd31d17ec32400cc2f39c9df9153358e37d0d5e9aa7616ae46","approved":true,"body":"Saved role record.\nTitle: Backend Engineer\nCompany: Harbour Systems\nSource: https://harbour.example/jobs/1\nDescription:\nBuild Go services."}],"draft":{"focus":{"text":"Application for Backend Engineer at Harbour Systems.","citations":[{"sourceId":"role-description","excerpt":"Backend Engineer"}]},"cover":[{"text":"Prepared application material for Backend Engineer at Harbour Systems: 2 of 2 required employer questions answered, 0 held.","citations":[{"sourceId":"role-description","excerpt":"Harbour Systems"}]}],"answers":[{"question":"Describe a Go service you shipped.","lines":[{"text":"I shipped a billing service in Go last year.","citations":[{"sourceId":"cv-vince-liem.md","excerpt":"Recent work spans Go services"}]}]}],"materialUnknowns":null,"relevance":[{"requirement":"Application for Backend Engineer at Harbour Systems.","sourceId":"role-description","scope":"relevant","confidence":0.9,"inputSha256":"c1471028bd70d4d0e3a16213cbce91b02e9122d064856ede04484570783beaf5","model":"stub-jev-v1"},{"requirement":"Application for Backend Engineer at Harbour Systems.","sourceId":"role-description","scope":"relevant","confidence":0.9,"inputSha256":"3f74c32fa0cfafff3164b733d6cfbf5fb21776a48eee3db19871620cbbadac14","model":"stub-jev-v1"},{"requirement":"Describe a Go service you shipped.","sourceId":"cv-vince-liem.md","scope":"relevant","confidence":0.9,"inputSha256":"32bdbaf1ecddc37b598593b6b0116144500d3c3d789d00f1745b540503adbaf2","model":"stub-jev-v1"}]},"templateSha256":"e9643864392f2aff7f900a82714a8feb573f636c24c62e7a169c29de41bc9a57","preparationRequestSha256":"rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr","material":{"checkId":"chk","questionSetSha256":"ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss","origin":"prepared","answers":[{"questionId":"q0","state":"answered","answerVersion":1,"textSha256":"f019470542ba1d5c7b3917cb1b304ad7577b6d4e086f2f98b5cee442824816b0","text":"I want this role for its Go platform work."},{"questionId":"q1","state":"drafted","answerVersion":0,"textSha256":"122afaf87361de7af4530cb975c6b0d8a5cbfb0bc18ad6a68eb278cbe1f84b22","text":"I shipped a billing service in Go last year."},{"questionId":"q2","state":"blank","answerVersion":0,"textSha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","text":""},{"questionId":"q3","state":"blank","answerVersion":0,"textSha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","text":""}]}}`
