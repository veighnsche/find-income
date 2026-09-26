package materialprep_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/materialprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

const careerEvidenceID = "cv-vince-liem.md"

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

type prepFixture struct {
	db          *store.Store
	opportunity store.Opportunity
	check       store.CheckView
	svc         *materialprep.Service
	career      *stubCareer
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
		career: &stubCareer{t: t}}
	f.svc = &materialprep.Service{Store: db, Career: f.career.load}
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
