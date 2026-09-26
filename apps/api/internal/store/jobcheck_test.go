package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

func insertCheckCapture(t *testing.T, s *Store, url string) SourceCapture {
	t.Helper()
	var capture SourceCapture
	err := s.ResearchWrite(context.Background(), func(db ResearchDB) error {
		var err error
		sum := sha256.Sum256([]byte(url))
		capture, err = InsertSourceCapture(context.Background(), db, SourceCaptureInput{
			ContentSHA256: hex.EncodeToString(sum[:]),
			ArtifactRef:   "artifact/" + hex.EncodeToString(sum[:8]),
			ByteLength:    2048,
			MediaType:     "text/html",
			OriginalURL:   url,
			Provenance:    researchcontract.ProvenanceFetchedResponse,
			Completeness:  CaptureComplete,
			Executor:      researchcontract.ExecutorIdentity{Backend: "test-shell"},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return capture
}

func startCheckRound(t *testing.T, s *Store, key string, operations []string, opportunityIDs ...string) (Round, string) {
	t.Helper()
	ctx := context.Background()
	owner := ownerActor()
	agent := Actor{Kind: "agent", ID: "codex-check"}
	preferences, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resources := []string{"campaign:active"}
	for _, id := range opportunityIDs {
		resources = append(resources, "opportunity:"+id)
	}
	round, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: key,
		Intent: "Check one selected role", Outcome: "process_input", ProfileVersion: preferences.Version,
		Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Operations: operations, Resources: resources,
			Delegates: []string{agent.ID}},
		Limits: RoundAllowance{Requests: 8, Items: 8, Tools: 8, Turns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	round, err = s.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	return round, mutationCapability(t, s, round, agent, resources[1])
}

func checkSaveFixture(opportunityID, checkID, captureID string) CheckSaveInput {
	return CheckSaveInput{
		OpportunityID: opportunityID, CheckID: checkID,
		Vacancy: CheckVacancyInput{CaptureIDs: []string{captureID},
			Completeness: CaptureComplete, SourceURL: "https://harbour.example/jobs/1",
			RetrievedAt: "2026-09-24T11:05:00Z"},
		RequestedDocuments: []RequestedDocumentInput{{Label: "CV", Required: true,
			SourceExcerpt: "Send your CV to jobs@example.invalid",
			SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 214, End: 260}}},
		Requirements: []CheckRequirementInput{{Statement: "Weekend availability is required.",
			SourceExcerpt: "Weekend availability is required for this rota.",
			SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 100, End: 149}}},
		Route: CheckRouteInput{Kind: CheckRouteDirect, DestinationText: "jobs@example.invalid",
			Judgment: CheckRouteJudgmentApplication, SourceExcerpt: "Send your CV to jobs@example.invalid",
			ObservedAt: "2026-09-24T11:30:00Z"},
		Gaps: []CheckGapInput{{Description: "Weekly hours are not stated in the vacancy text.",
			Consequential: true, Kind: CheckGapMissingFact}},
		Questions: []CheckQuestionInput{
			{Text: "Why do you want this role?", Required: CheckRequired, Kind: CheckQuestionFreeText,
				SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 150, End: 180},
				SourceExcerpt: "Why do you want this role?"},
			{Text: "Are you willing to work hybrid?", Required: CheckRequiredUnknown,
				SourceSpan:    CheckSourceSpan{CaptureID: captureID, Start: 300, End: 335},
				SourceExcerpt: "Are you willing to work hybrid?"},
		},
		Activity: []CheckActivityInput{
			{Kind: "vacancy.opened", Outcome: string(researchcontract.OutcomeOK), CaptureID: captureID},
			{Kind: "questions.extracted", Payload: json.RawMessage(`{"count":2}`)},
		},
	}
}

func applyCheckSave(t *testing.T, s *Store, round Round, capability, key string, expectedRevision int64, save CheckSaveInput) (RoundMutationResult, bool, error) {
	t.Helper()
	return s.ApplyRoundMutation(context.Background(), Actor{Kind: "agent", ID: "codex-check"}, round.ID,
		RoundMutationInput{RequestKey: key, Operation: RoundCheckSave,
			ResourceID: "opportunity:" + save.OpportunityID, ExpectedRevision: expectedRevision,
			CheckSave: &save, Capability: capability})
}

func TestJobCheckStartRequiresSelectedRole(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	start := CheckStartInput{RequestKey: "check-1", ExpectedOpportunityRevision: 1, ExpectedWorkflowRevision: 0}
	if _, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, start); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("unselected start: %v", err)
	}
	if _, _, err := s.StartJobCheck(ctx, ownerActor(), "missing", start); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing role start: %v", err)
	}
	selected := selectFixtureOpportunity(t, s, company.ID, "select-guard")
	if _, _, err := s.SetOwnerOpportunityDecision(ctx, ownerActor(), selected.ID, OwnerDecisionInput{
		RequestKey: "dismiss-guard", ExpectedOpportunityRevision: selected.Revision,
		ExpectedDecisionRevision: 1, Decision: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartJobCheck(ctx, ownerActor(), selected.ID, start); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("dismissed start: %v", err)
	}
	if _, err := s.CurrentJobCheck(ctx, selected.ID); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("dismissed read: %v", err)
	}
}

func TestJobCheckStartTransitionsAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-idem")
	if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallNotChecked || status.Check != nil {
		t.Fatalf("before start: %+v %v", status, err)
	}
	start := CheckStartInput{RequestKey: "check-idem", ExpectedOpportunityRevision: opportunity.Revision, ExpectedWorkflowRevision: 0}
	first, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, start)
	if err != nil || !created || first.Status != CheckStatusChecking || first.WorkflowRevision != 1 {
		t.Fatalf("start: %+v created=%v err=%v", first, created, err)
	}
	if workflow, err := s.RoleWorkflow(ctx, opportunity.ID); err != nil || workflow.Stage != RoleStageChecking || workflow.Revision != 1 {
		t.Fatalf("workflow after start: %+v %v", workflow, err)
	}
	replay, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, start)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("idempotent replay: %+v created=%v err=%v", replay, created, err)
	}
	altered := start
	altered.ExpectedWorkflowRevision = 7
	if _, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, altered); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("altered replay: %v", err)
	}
	converge := CheckStartInput{RequestKey: "check-other-key", ExpectedOpportunityRevision: 999, ExpectedWorkflowRevision: 999}
	same, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, converge)
	if err != nil || created || same.ID != first.ID {
		t.Fatalf("second key while pending: %+v created=%v err=%v", same, created, err)
	}
	events, _, err := s.ListCheckActivity(ctx, opportunity.ID, "", 10)
	if err != nil || len(events) != 1 || events[0].Kind != CheckActivityStarted {
		t.Fatalf("start activity: %+v %v", events, err)
	}
}

func TestJobCheckStartRevisionAndStageGuards(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-guards")
	stale := CheckStartInput{RequestKey: "check-stale", ExpectedOpportunityRevision: 999, ExpectedWorkflowRevision: 0}
	if _, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale opportunity revision: %v", err)
	}
	stale = CheckStartInput{RequestKey: "check-stale-wf", ExpectedOpportunityRevision: opportunity.Revision, ExpectedWorkflowRevision: 5}
	if _, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale workflow revision: %v", err)
	}
	if _, _, err := s.StartJobCheck(ctx, Actor{Kind: "agent", ID: "codex-check"}, opportunity.ID,
		CheckStartInput{RequestKey: "check-agent", ExpectedOpportunityRevision: opportunity.Revision}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent direct start: %v", err)
	}
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-guards", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "round-guards", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "save-guards", started.WorkflowRevision,
		checkSaveFixture(opportunity.ID, started.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	after, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	late := CheckStartInput{RequestKey: "check-late", ExpectedOpportunityRevision: opportunity.Revision, ExpectedWorkflowRevision: after.Revision}
	converged, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, late)
	if err != nil || created || converged.ID != started.ID {
		t.Fatalf("start from checked must converge: created=%v %+v %v", created, converged, err)
	}
}

func TestCheckSaveAcceptsContentSHA(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-sha")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-sha", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/sha")
	round, capability := startCheckRound(t, s, "round-sha", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	// Production receipts bind the content sha256, not the retrieval row
	// id; the vacancy, documents and question spans must all validate.
	save := checkSaveFixture(opportunity.ID, started.ID, capture.ContentSHA256)
	if _, _, err := applyCheckSave(t, s, round, capability, "save-sha", started.WorkflowRevision, save); err != nil {
		t.Fatalf("sha-form spans: %v", err)
	}
	view, err := s.GetJobCheck(ctx, opportunity.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != CheckStatusChecked || len(view.Questions) != 2 ||
		view.Questions[0].SourceSpan.CaptureID != capture.ID {
		t.Fatalf("sha-form view: %+v", view)
	}
}

func TestCheckSaveCompletesOnlyWhenComplete(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-complete")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-complete", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "round-complete", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	broken := []struct {
		name   string
		mutate func(*CheckSaveInput)
	}{
		{"missing vacancy", func(save *CheckSaveInput) { save.Vacancy = CheckVacancyInput{} }},
		{"missing completeness", func(save *CheckSaveInput) { save.Vacancy.Completeness = "" }},
		{"missing route", func(save *CheckSaveInput) { save.Route = CheckRouteInput{} }},
		{"missing judgment destination", func(save *CheckSaveInput) {
			save.Route.Kind, save.Route.DestinationText = "", ""
		}},
		{"unsupported application route", func(save *CheckSaveInput) {
			save.Route.Kind, save.Route.Judgment = CheckRouteUnsupported, CheckRouteJudgmentApplication
		}},
		{"implicit documents", func(save *CheckSaveInput) { save.RequestedDocuments = nil }},
		{"implicit requirements", func(save *CheckSaveInput) { save.Requirements = nil }},
		{"implicit gaps", func(save *CheckSaveInput) { save.Gaps = nil }},
		{"empty questions", func(save *CheckSaveInput) { save.Questions = nil }},
		{"unsourced question", func(save *CheckSaveInput) {
			save.Questions[0].SourceSpan.CaptureID, save.Questions[0].SourceExcerpt = "", ""
		}},
		{"unknown capture", func(save *CheckSaveInput) { save.Questions[0].SourceSpan.CaptureID = "missing" }},
		{"guessed requiredness", func(save *CheckSaveInput) { save.Questions[1].Required = "maybe" }},
		{"bad span", func(save *CheckSaveInput) {
			save.Questions[0].SourceSpan.Start, save.Questions[0].SourceSpan.End = 90, 90
		}},
		{"unknown route", func(save *CheckSaveInput) { save.Route.RouteID = "missing" }},
		{"forged lifecycle activity", func(save *CheckSaveInput) {
			save.Activity = []CheckActivityInput{{Kind: "check.completed"}}
		}},
	}
	for i, tc := range broken {
		save := checkSaveFixture(opportunity.ID, started.ID, capture.ID)
		tc.mutate(&save)
		if _, _, err := applyCheckSave(t, s, round, capability, fmt.Sprintf("broken-save-%d", i), started.WorkflowRevision, save); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallChecking {
			t.Fatalf("%s left state %+v err=%v", tc.name, status, err)
		}
	}
	save := checkSaveFixture(opportunity.ID, started.ID, capture.ID)
	result, created, err := applyCheckSave(t, s, round, capability, "save-complete", started.WorkflowRevision, save)
	if err != nil || !created || result.EntityKind != "job_check" || result.EntityID != started.ID || result.Revision != 2 {
		t.Fatalf("complete save: %+v created=%v err=%v", result, created, err)
	}
	replay, created, err := applyCheckSave(t, s, round, capability, "save-complete", started.WorkflowRevision, save)
	if err != nil || created || replay != result {
		t.Fatalf("save replay duplicated: %+v created=%v err=%v", replay, created, err)
	}
	view, err := s.GetJobCheck(ctx, opportunity.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != CheckStatusChecked || view.CompletedAt == "" || len(view.Questions) != 2 ||
		view.QuestionSetVersion != 1 || view.QuestionSetSHA256 == "" {
		t.Fatalf("completed view: %+v", view)
	}
	if view.Questions[0].Ordinal != 0 || view.Questions[1].Ordinal != 1 ||
		view.Questions[0].ID == "" || view.Questions[0].ID == view.Questions[1].ID {
		t.Fatalf("question identity: %+v", view.Questions)
	}
	for i, question := range view.Questions {
		sum := sha256.Sum256([]byte(question.Text))
		if question.TextSHA256 != hex.EncodeToString(sum[:]) || question.CheckID != started.ID {
			t.Fatalf("question %d hash/link: %+v", i, question)
		}
		if question.Text != save.Questions[i].Text || question.Required != save.Questions[i].Required {
			t.Fatalf("question %d content: %+v", i, question)
		}
	}
	setSum := sha256.Sum256(mustMarshalCheckSet(t, view.Questions))
	if view.QuestionSetSHA256 != hex.EncodeToString(setSum[:]) {
		t.Fatalf("question set hash not server-derived: %s", view.QuestionSetSHA256)
	}
	if len(view.Requirements) != 1 || view.Requirements[0].Statement != save.Requirements[0].Statement ||
		view.Requirements[0].SourceSpan != save.Requirements[0].SourceSpan {
		t.Fatalf("requirements round trip: %+v", view.Requirements)
	}
	if workflow, err := s.RoleWorkflow(ctx, opportunity.ID); err != nil || workflow.Stage != RoleStageChecked || workflow.Revision != 2 {
		t.Fatalf("workflow after save: %+v %v", workflow, err)
	}
	if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallChecked {
		t.Fatalf("current after save: %+v %v", status, err)
	}
}

func mustMarshalCheckSet(t *testing.T, questions []CheckQuestionView) []byte {
	t.Helper()
	canonical := make([]struct {
		Text     string          `json:"text"`
		Required string          `json:"required"`
		Kind     string          `json:"kind"`
		Span     CheckSourceSpan `json:"span"`
	}, len(questions))
	for i, question := range questions {
		canonical[i].Text, canonical[i].Required = question.Text, question.Required
		canonical[i].Kind, canonical[i].Span = question.Kind, question.SourceSpan
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCheckSaveBlockedCasesAndRecheck(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-blocked")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-blocked", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	round, capability := startCheckRound(t, s, "round-blocked", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	uncoded := CheckSaveInput{OpportunityID: opportunity.ID, CheckID: started.ID,
		Blocked: &CheckBlockedInput{Code: "nope", Detail: "missing code"}}
	if _, _, err := applyCheckSave(t, s, round, capability, "blocked-uncoded", started.WorkflowRevision, uncoded); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uncoded block: %v", err)
	}
	blocked := CheckSaveInput{OpportunityID: opportunity.ID, CheckID: started.ID,
		Blocked: &CheckBlockedInput{Code: CheckBlockedSourceUnavailable, Detail: "Vacancy page returns 404; no content captured."}}
	if _, _, err := applyCheckSave(t, s, round, capability, "blocked-save", started.WorkflowRevision, blocked); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetJobCheck(ctx, opportunity.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != CheckStatusBlocked || view.BlockedReason == nil ||
		view.BlockedReason.Code != CheckBlockedSourceUnavailable || view.CompletedAt == "" {
		t.Fatalf("blocked view: %+v", view)
	}
	before, _ := json.Marshal(view)
	if workflow, err := s.RoleWorkflow(ctx, opportunity.ID); err != nil || workflow.Stage != RoleStageBlocked ||
		!strings.HasPrefix(workflow.BlockedReason, CheckBlockedSourceUnavailable+":") {
		t.Fatalf("workflow after block: %+v %v", workflow, err)
	}
	if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallBlocked {
		t.Fatalf("current after block: %+v %v", status, err)
	}
	recheck, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, CheckStartInput{
		RequestKey: "check-recheck", ExpectedOpportunityRevision: opportunity.Revision, ExpectedWorkflowRevision: 2})
	if err != nil || !created || recheck.ID == started.ID || recheck.Status != CheckStatusChecking {
		t.Fatalf("recheck start: %+v created=%v err=%v", recheck, created, err)
	}
	if workflow, err := s.RoleWorkflow(ctx, opportunity.ID); err != nil || workflow.Stage != RoleStageChecking ||
		workflow.BlockedReason != "" || workflow.Revision != 3 {
		t.Fatalf("workflow after recheck: %+v %v", workflow, err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	if _, _, err := applyCheckSave(t, s, round, capability, "save-recheck", recheck.WorkflowRevision,
		checkSaveFixture(opportunity.ID, recheck.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(view)
	reread, err := s.GetJobCheck(ctx, opportunity.ID, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterReread, _ := json.Marshal(reread)
	if string(before) != string(after) || string(before) != string(afterReread) {
		t.Fatalf("blocked check mutated by recheck")
	}
	if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallChecked ||
		status.Check == nil || status.Check.ID != recheck.ID {
		t.Fatalf("current after recheck save: %+v %v", status, err)
	}
}

func TestCheckSaveRejectsWrongRoleState(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-fence")
	other := selectFixtureOpportunity(t, s, company.ID, "select-fence-other")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-fence", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "round-fence", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID, other.ID)
	save := checkSaveFixture(opportunity.ID, started.ID, capture.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "save-stale-rev", started.WorkflowRevision+5, save); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale workflow revision save: %v", err)
	}
	mismatched := save
	mismatched.OpportunityID = other.ID
	if _, _, err := applyCheckSave(t, s, round, capability, "save-mismatch", started.WorkflowRevision, mismatched); !errors.Is(err, ErrInvalid) {
		t.Fatalf("check/opportunity mismatch: %v", err)
	}
	if _, _, err := applyCheckSave(t, s, round, capability, "save-fence", started.WorkflowRevision, save); err != nil {
		t.Fatal(err)
	}
	if _, _, err := applyCheckSave(t, s, round, capability, "save-twice", started.WorkflowRevision+1, save); !errors.Is(err, ErrConflict) {
		t.Fatalf("second save to completed check: %v", err)
	}
	otherStarted, _, err := s.StartJobCheck(ctx, ownerActor(), other.ID,
		CheckStartInput{RequestKey: "check-fence-other", ExpectedOpportunityRevision: other.Revision})
	if err != nil {
		t.Fatal(err)
	}
	otherSave := checkSaveFixture(other.ID, otherStarted.ID, capture.ID)
	outOfScope := checkSaveFixture("missing-scope", "missing-check", capture.ID)
	_, _, err = s.ApplyRoundMutation(ctx, Actor{Kind: "agent", ID: "codex-check"}, round.ID,
		RoundMutationInput{RequestKey: "save-unscoped", Operation: RoundCheckSave,
			ResourceID: "opportunity:missing-scope", ExpectedRevision: otherStarted.WorkflowRevision,
			CheckSave: &outOfScope, Capability: capability})
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("unscoped save: %v", err)
	}
	_, _, err = s.ApplyRoundMutation(ctx, ownerActor(), round.ID,
		RoundMutationInput{RequestKey: "save-owner", Operation: RoundCheckSave,
			ResourceID: "opportunity:" + other.ID, ExpectedRevision: otherStarted.WorkflowRevision,
			CheckSave: &otherSave, Capability: capability})
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("owner direct save: %v", err)
	}
	if _, _, err := s.SetOwnerOpportunityDecision(ctx, ownerActor(), other.ID, OwnerDecisionInput{
		RequestKey: "dismiss-fence", ExpectedOpportunityRevision: other.Revision,
		ExpectedDecisionRevision: 1, Decision: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := applyCheckSave(t, s, round, capability, "save-dismissed", otherStarted.WorkflowRevision, otherSave); !errors.Is(err, ErrRoleNotSelected) {
		t.Fatalf("save after dismiss: %v", err)
	}
}

func TestJobCheckOutdatedAndIndependence(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	first := selectFixtureOpportunity(t, s, company.ID, "select-out-first")
	second := selectFixtureOpportunity(t, s, company.ID, "select-out-second")
	firstStarted, _, err := s.StartJobCheck(ctx, ownerActor(), first.ID,
		CheckStartInput{RequestKey: "check-out-first", ExpectedOpportunityRevision: first.Revision})
	if err != nil {
		t.Fatal(err)
	}
	secondStarted, _, err := s.StartJobCheck(ctx, ownerActor(), second.ID,
		CheckStartInput{RequestKey: "check-out-second", ExpectedOpportunityRevision: second.Revision})
	if err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "round-out", []string{RoundCodexTurn, RoundCheckSave}, first.ID, second.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "save-out-first", firstStarted.WorkflowRevision,
		checkSaveFixture(first.ID, firstStarted.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	blocked := CheckSaveInput{OpportunityID: second.ID, CheckID: secondStarted.ID,
		Blocked: &CheckBlockedInput{Code: CheckBlockedRouteAmbiguous, Detail: "No submission instruction on the vacancy page."}}
	if _, _, err := applyCheckSave(t, s, round, capability, "save-out-second", secondStarted.WorkflowRevision, blocked); err != nil {
		t.Fatal(err)
	}
	if status, err := s.CurrentJobCheck(ctx, second.ID); err != nil || status.Status != CheckOverallBlocked {
		t.Fatalf("blocked role: %+v %v", status, err)
	}
	notes := "Owner added a note after the check completed"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), first.ID,
		OpportunityPatch{ExpectedRevision: first.Revision, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if status, err := s.CurrentJobCheck(ctx, first.ID); err != nil || status.Status != CheckOverallOutdated ||
		status.Check == nil || status.Check.ID != firstStarted.ID {
		t.Fatalf("outdated role: %+v %v", status, err)
	}
	if _, err := s.GetJobCheck(ctx, first.ID, secondStarted.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-role check read: %v", err)
	}
}

func TestJobCheckActivityPaginationAndScope(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-activity")
	other := selectFixtureOpportunity(t, s, company.ID, "select-activity-other")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-activity", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartJobCheck(ctx, ownerActor(), other.ID,
		CheckStartInput{RequestKey: "check-activity-other", ExpectedOpportunityRevision: other.Revision}); err != nil {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "round-activity", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "save-activity", started.WorkflowRevision,
		checkSaveFixture(opportunity.ID, started.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	cursor := ""
	for {
		events, next, err := s.ListCheckActivity(ctx, opportunity.ID, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			kinds = append(kinds, event.Kind)
			if event.RunID != opportunity.ID || event.ID == "" {
				t.Fatalf("activity scope/shape: %+v", event)
			}
			if len(event.Payload) > 0 && !json.Valid(event.Payload) {
				t.Fatalf("activity payload: %+v", event)
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(kinds) != 4 || kinds[0] != CheckActivityStarted || kinds[3] != CheckActivityCompleted {
		t.Fatalf("activity lifecycle: %v", kinds)
	}
	middle := map[string]bool{kinds[1]: true, kinds[2]: true}
	if !middle["vacancy.opened"] || !middle["questions.extracted"] {
		t.Fatalf("activity progress: %v", kinds)
	}
	otherEvents, _, err := s.ListCheckActivity(ctx, other.ID, "", 10)
	if err != nil || len(otherEvents) != 1 || otherEvents[0].Kind != CheckActivityStarted {
		t.Fatalf("other role activity: %+v %v", otherEvents, err)
	}
	if _, _, err := s.ListCheckActivity(ctx, opportunity.ID, "missing", 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, _, err := s.ListCheckActivity(ctx, "missing", "", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing role activity: %v", err)
	}
}

func TestCheckSaveStalePendingSupersedes(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-supersede")
	started, _, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-supersede", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil {
		t.Fatal(err)
	}
	notes := "Vacancy reposted with a new closing date"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID,
		OpportunityPatch{ExpectedRevision: opportunity.Revision, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallOutdated {
		t.Fatalf("stale pending: %+v %v", status, err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "round-supersede", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	if _, _, err := applyCheckSave(t, s, round, capability, "save-stale", started.WorkflowRevision,
		checkSaveFixture(opportunity.ID, started.ID, capture.ID)); !errors.Is(err, ErrConflict) {
		t.Fatalf("save against moved basis: %v", err)
	}
	next, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID, CheckStartInput{
		RequestKey: "check-supersede-2", ExpectedOpportunityRevision: opportunity.Revision + 1,
		ExpectedWorkflowRevision: started.WorkflowRevision})
	if err != nil || !created || next.ID == started.ID || next.OpportunityRevision != opportunity.Revision+1 {
		t.Fatalf("superseding start: %+v created=%v err=%v", next, created, err)
	}
	if _, _, err := applyCheckSave(t, s, round, capability, "save-fresh", next.WorkflowRevision,
		checkSaveFixture(opportunity.ID, next.ID, capture.ID)); err != nil {
		t.Fatal(err)
	}
	if status, err := s.CurrentJobCheck(ctx, opportunity.ID); err != nil || status.Status != CheckOverallChecked ||
		status.Check.ID != next.ID {
		t.Fatalf("current after supersede: %+v %v", status, err)
	}
}

func TestJobCheckRecheckFromChecked(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity := selectFixtureOpportunity(t, s, company.ID, "select-recheck")
	started, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-recheck-1", ExpectedOpportunityRevision: opportunity.Revision})
	if err != nil || !created {
		t.Fatal(err)
	}
	capture := insertCheckCapture(t, s, "https://harbour.example/jobs/1")
	round, capability := startCheckRound(t, s, "round-recheck", []string{RoundCodexTurn, RoundCheckSave}, opportunity.ID)
	workflow, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = applyCheckSave(t, s, round, capability, "save-recheck-1", workflow.Revision,
		checkSaveFixture(opportunity.ID, started.ID, capture.ID))
	if err != nil {
		t.Fatal(err)
	}
	converged, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-recheck-2", ExpectedOpportunityRevision: opportunity.Revision, ExpectedWorkflowRevision: 2})
	if err != nil || created || converged.ID != started.ID {
		t.Fatalf("current basis must converge: created=%v %+v %v", created, converged, err)
	}
	notes := "Owner added a private note."
	bumped, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: opportunity.Revision, Notes: &notes})
	if err != nil {
		t.Fatal(err)
	}
	recheck, created, err := s.StartJobCheck(ctx, ownerActor(), opportunity.ID,
		CheckStartInput{RequestKey: "check-recheck-3", ExpectedOpportunityRevision: bumped.Revision, ExpectedWorkflowRevision: 2})
	if err != nil || !created || recheck.ID == started.ID || recheck.Status != CheckStatusChecking {
		t.Fatalf("outdated basis must recheck: created=%v %+v %v", created, recheck, err)
	}
	restaged, err := s.RoleWorkflow(ctx, opportunity.ID)
	if err != nil || restaged.Stage != RoleStageChecking {
		t.Fatalf("recheck stage: %+v %v", restaged, err)
	}
}
