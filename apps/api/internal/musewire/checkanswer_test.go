package musewire

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// cannedExecutor answers one fetch shape without touching the network and
// records every input for honesty assertions.
type cannedExecutor struct {
	mu     sync.Mutex
	calls  int
	inputs []researchcontract.ExecuteInput
	output researchcontract.ExecuteOutput
	err    error
}

func (f *cannedExecutor) Execute(_ context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.inputs = append(f.inputs, in)
	return f.output, f.err
}

func (f *cannedExecutor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeMatchClient judges answer batches from a per-question script with no
// live call. Unscripted questions resolve to none_fits.
type fakeMatchClient struct {
	mu      sync.Mutex
	calls   int
	choices map[string]string
}

func (f *fakeMatchClient) RequestedModel() string { return "jev-fixture-1" }

func (f *fakeMatchClient) EncodedRequest(jev.Request) ([]byte, error) {
	return []byte(`{"model":"jev-fixture-1"}`), nil
}

func (f *fakeMatchClient) EvaluateOnceCaptured(_ context.Context, req jev.Request) (jev.Result, jev.CapturedExchange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	answers := make(map[string]jev.Answer, len(req.Questions))
	wire := make(map[string]any, len(req.Questions))
	for key, question := range req.Questions {
		choice := f.choices[key]
		if choice == "" {
			choice = jev.AnswerMatchNoneFits
		}
		options := question.(jev.ChoiceQuestion).Criteria
		probabilities := make(map[string]float64, len(options))
		for option := range options {
			probabilities[option] = 0
		}
		probabilities[choice] = 1
		answers[key] = jev.Answer{Type: "choice", Choice: &jev.ChoiceAnswer{
			Choice: choice, Probabilities: probabilities, Confidence: 0.9}}
		wire[key] = map[string]any{"type": "choice", "choice": choice,
			"probabilities": probabilities, "confidence": 0.9}
	}
	// The captured response bytes replay exactly what this fake returned, so
	// SaveAnswerMatch re-derives the same selections from the stored exchange.
	raw, err := json.Marshal(map[string]any{"model": "jev-fixture-1", "answers": wire,
		"usage": map[string]any{"input_tokens": 9, "output_tokens": 2}})
	if err != nil {
		return jev.Result{}, jev.CapturedExchange{}, err
	}
	requestBody := []byte(`{"model":"jev-fixture-1"}`)
	return jev.Result{RequestedModel: "jev-fixture-1", ReturnedModel: "jev-fixture-1",
			Answers: answers, Usage: jev.Usage{InputTokens: 9, OutputTokens: 2}},
		jev.CapturedExchange{RequestBytes: requestBody, ResponseBytes: raw,
			HTTPStatus: 200, ReturnedModel: "jev-fixture-1"}, nil
}

func (f *fakeMatchClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type vacancySeed struct {
	pageURL  string
	employer string
	title    string
	receipt  string
}

// commissionForCheck runs one discovery commission through the gate,
// driving the given saves through the live MCP tools like E06 does.
func commissionForCheck(t *testing.T, fix *connectedFixture, gate *gateTransport, runRef string, saves []vacancySeed) CommissionResult {
	t.Helper()
	type commissionOutcome struct {
		result CommissionResult
		err    error
	}
	done := make(chan commissionOutcome, 1)
	go func() {
		result, err := fix.service.CommissionDiscovery(context.Background(), runRef,
			musecode.PublicCriteria{RoleKeywords: []string{"support"}}, fix.profile, fix.rubric)
		done <- commissionOutcome{result, err}
	}()
	<-gate.started
	server, ok := fix.service.ServerForRun(runRef)
	if !ok {
		t.Fatal("live tool server unavailable during the session")
	}
	session := mcpSession(t, server)
	refs := make([]string, 0, len(saves))
	for _, save := range saves {
		refs = append(refs, saveFixtureVacancy(t, session, save.pageURL, save.employer, save.title, save.receipt))
	}
	gate.saves = refs
	close(gate.proceed)
	outcome := <-done
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	return outcome.result
}

func selectFixtureRole(t *testing.T, db *store.Store, opportunityID string, revision int64) {
	t.Helper()
	_, _, err := db.SetOwnerOpportunityDecision(context.Background(), fixtureActor, opportunityID,
		store.OwnerDecisionInput{RequestKey: "select-" + opportunityID, ExpectedOpportunityRevision: revision, Decision: "selected"})
	if err != nil {
		t.Fatal(err)
	}
}

func startFixtureCheck(t *testing.T, db *store.Store, opportunityID string, revision int64) store.CheckView {
	t.Helper()
	view, created, err := db.StartJobCheck(context.Background(), fixtureActor, opportunityID, store.CheckStartInput{
		RequestKey: "check-" + opportunityID, ExpectedOpportunityRevision: revision, ExpectedWorkflowRevision: 0,
	})
	if err != nil || !created {
		t.Fatalf("start check: %+v %v", view, err)
	}
	return view
}

// Discovery yields a classified role, the explicit check retrieves its
// actual employer questions into a sourced persisted body, and Answer
// matches saved owner answers with zero generative or retrieval calls:
// exactly one Jev classifier batch, nothing else.
func TestCheckAnswerConnectedFlow(t *testing.T) {
	ctx := context.Background()
	gate := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	fix := newConnectedFixture(t, gate, []map[string]string{
		{"reason-positive": "hybrid-ok", "reason-negative": "abstain", "reason-missing": "abstain"},
	})
	commissioned := commissionForCheck(t, fix, gate, "run-c1", []vacancySeed{
		{"https://jobs.example.invalid/1", "Example BV", "Senior support engineer", "rc-1"},
	})
	if commissioned.Terminal.Outcome != musecode.OutcomeCompleted || len(commissioned.Findings) != 1 {
		t.Fatalf("commission = %+v, want one finding", commissioned.Terminal)
	}
	finding := commissioned.Findings[0]
	opportunityID := finding.OpportunityID
	selectFixtureRole(t, fix.db, opportunityID, finding.OpportunityRevision)
	pending := startFixtureCheck(t, fix.db, opportunityID, finding.OpportunityRevision)

	checker, err := NewChecker(CheckDeps{DB: fix.db, Actor: fixtureActor,
		Executor: fix.executor, Captures: fix.captures, Bounds: musecode.DefaultBounds(), Authorized: true})
	if err != nil {
		t.Fatal(err)
	}
	view, err := checker.PerformCheck(ctx, opportunityID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != store.CheckStatusChecked || len(view.Questions) != 2 {
		t.Fatalf("check = %+v, want checked with 2 sourced questions", view)
	}
	if view.Questions[0].Text != "Why do you want this support role?" || view.Questions[0].Required != store.CheckOptional {
		t.Fatalf("question 0 = %+v, want verbatim optional", view.Questions[0])
	}
	if view.Questions[1].Text != "Are you available for night shifts (required)?" || view.Questions[1].Required != store.CheckRequired {
		t.Fatalf("question 1 = %+v, want verbatim required", view.Questions[1])
	}
	captureID := finding.EvidenceLinks[0].CaptureID
	blob := fix.captures.blobs[captureID]
	for _, question := range view.Questions {
		span := question.SourceSpan
		if span.CaptureID != captureID || span.Start < 0 || span.End <= span.Start || span.End > len(blob) {
			t.Fatalf("question %q span = %+v, want valid span into the capture", question.Text, span)
		}
		if string(blob[span.Start:span.End]) != question.Text {
			t.Fatalf("span text %q != prompt %q", string(blob[span.Start:span.End]), question.Text)
		}
	}
	if view.QuestionSetSHA256 == "" || view.Route.Judgment != "unresolved" || len(view.Gaps) != 1 {
		t.Fatalf("view = %+v, want set hash, unresolved route and one gap", view)
	}
	if view.Vacancy.CaptureIDs[0] != captureID {
		t.Fatalf("vacancy captures = %v, want the reused capture", view.Vacancy.CaptureIDs)
	}
	assertNoDeniedKeys(t, view)

	// Reload honesty: sourced questions survive re-reads with zero new calls.
	reloaded, err := fix.db.GetJobCheck(ctx, opportunityID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Questions) != 2 || reloaded.Questions[0].ID != view.Questions[0].ID ||
		reloaded.Questions[1].Text != view.Questions[1].Text {
		t.Fatalf("reloaded = %+v, want stable sourced questions", reloaded)
	}
	if fix.executor.count() != 0 {
		t.Fatalf("executor calls = %d, want pure reuse", fix.executor.count())
	}

	// Answer: one saved owner answer, one Jev choice batch, deterministic
	// prefill, exact owner edit. Retrieval and provider counts freeze
	// except the single Jev classifier batch.
	saved, _, err := fix.db.CreateSavedAnswer(ctx, fixtureActor, store.SavedAnswerCreateInput{
		RequestKey: "ans-1", Text: "I thrive in senior support engineering roles with hybrid schedules.",
		ScopeTags: []string{"support"},
	})
	if err != nil {
		t.Fatal(err)
	}
	answer := saved.Versions[saved.CurrentVersion-1]
	round, _, err := fix.db.StartRound(ctx, fixtureActor, store.StartRoundInput{
		RequestKey: "match-c1", Intent: "Match saved answers", Outcome: "process_input",
		ProfileVersion: fix.profile,
		Scope: store.RoundScope{Operations: []string{store.RoundJevRequest},
			Resources: []string{"opportunity:" + opportunityID}},
		Limits: store.RoundAllowance{Requests: 2}, Deadline: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fix.db.ActivateRound(ctx, fixtureActor, round.ID); err != nil {
		t.Fatal(err)
	}
	digest, err := fix.db.AnswerCatalogDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	questions := make([]jev.AnswerMatchQuestion, 0, len(view.Questions))
	for _, question := range view.Questions {
		questions = append(questions, jev.AnswerMatchQuestion{QuestionID: question.ID,
			Text: question.Text, Required: question.Required, Kind: question.Kind,
			TextSHA256: question.TextSHA256,
			Candidates: []jev.AnswerMatchCandidate{{AnswerID: saved.ID,
				AnswerVersion: answer.Version, TextSHA256: answer.TextSHA256,
				ScopeTags: saved.ScopeTags, Excerpt: answer.Text}},
		})
	}
	matcher := &fakeMatchClient{choices: map[string]string{"match_0": saved.ID}}
	input := jev.AnswerMatchInput{CheckID: view.ID, QuestionSetSHA256: view.QuestionSetSHA256,
		AnswerCatalogDigest: digest, MaxReportedTokens: 100, Questions: questions}
	matched, err := (jevservice.Service{Store: fix.db, Client: matcher}).RunAnswerMatch(ctx,
		jevservice.Binding{Actor: fixtureActor, RoundID: round.ID,
			ResourceID: "opportunity:" + opportunityID, RequestKeyPrefix: "match-c1", ProfileVersion: fix.profile}, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched.Selections) != 2 || matched.Selections[0].AnswerID != saved.ID || matched.Selections[0].NoneFits {
		t.Fatalf("selections = %+v, want first question matched", matched.Selections)
	}
	if !matched.Selections[1].NoneFits || matched.Selections[1].AnswerID != "" {
		t.Fatalf("selections = %+v, want second question none_fits", matched.Selections)
	}
	if matcher.count() != 1 {
		t.Fatalf("jev match calls = %d, want exactly 1", matcher.count())
	}
	ids, err := fix.db.JevAttemptIDsForRequestPrefix(ctx, round.ID, "match-c1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("attempts = %v %v, want one charged attempt", ids, err)
	}
	if _, _, err := fix.db.SaveAnswerMatch(ctx, fixtureActor, round.ID, ids[0], "match-c1", input, matched); err != nil {
		t.Fatal(err)
	}
	prefilled, err := fix.db.SaveAnswerValue(ctx, fixtureActor, opportunityID, view.Questions[0].ID,
		store.AnswerValueSaveInput{ExpectedAnswerVersion: 0, Text: answer.Text})
	if err != nil {
		t.Fatal(err)
	}
	if prefilled.Version != 1 || prefilled.Text != answer.Text {
		t.Fatalf("prefill = %+v, want matched text at v1", prefilled)
	}
	edited, err := fix.db.SaveAnswerValue(ctx, fixtureActor, opportunityID, view.Questions[0].ID,
		store.AnswerValueSaveInput{ExpectedAnswerVersion: 1, Text: answer.Text + " Edited for tone."})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Version != 2 {
		t.Fatalf("edit = %+v, want v2", edited)
	}
	values, err := fix.db.CurrentQuestionAnswers(ctx, opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(values.Values) != 1 || values.Values[0].Version != 2 {
		t.Fatalf("values = %+v, want only the edited v2", values)
	}
	if fix.executor.count() != 0 {
		t.Fatalf("executor calls after answer = %d, want zero retrieval in Answer", fix.executor.count())
	}
	if fix.provider.count() != 1 {
		t.Fatalf("screening calls = %d, want frozen at 1", fix.provider.count())
	}
}

func TestCheckFetchPathRetrievesBounded(t *testing.T) {
	ctx := context.Background()
	gate := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	fix := newConnectedFixture(t, gate, []map[string]string{
		{"reason-positive": "hybrid-ok", "reason-negative": "abstain", "reason-missing": "abstain"},
	})
	commissioned := commissionForCheck(t, fix, gate, "run-fetch", []vacancySeed{
		{"https://jobs.example.invalid/1", "Example BV", "Senior support engineer", "rc-1"},
	})
	finding := commissioned.Findings[0]
	// The saved capture becomes unreadable after discovery, forcing the
	// bounded fetch path for exactly one retrieval.
	fix.captures.breakOpen(finding.EvidenceLinks[0].CaptureID)
	fetchBody := []byte("<html><body>\nFetched detail page.\nWhat on-call rotation do you cover?\n</body></html>")
	fetchCapture := insertFixtureCapture(t, fix.db, "https://jobs.example.invalid/1", fetchBody)
	fix.captures.blobs[fetchCapture.ID] = fetchBody
	executor := &cannedExecutor{output: researchcontract.ExecuteOutput{
		Outcome: researchcontract.OutcomeOK, CaptureID: fetchCapture.ID,
		Receipt: researchcontract.ExecutionReceipt{ID: "rc-fetch", Status: researchcontract.ReceiptOK, CaptureID: fetchCapture.ID},
	}}
	opportunityID := finding.OpportunityID
	selectFixtureRole(t, fix.db, opportunityID, finding.OpportunityRevision)
	pending := startFixtureCheck(t, fix.db, opportunityID, finding.OpportunityRevision)
	checker, err := NewChecker(CheckDeps{DB: fix.db, Actor: fixtureActor,
		Executor: executor, Captures: fix.captures, Bounds: musecode.DefaultBounds(), Authorized: true})
	if err != nil {
		t.Fatal(err)
	}
	view, err := checker.PerformCheck(ctx, opportunityID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != store.CheckStatusChecked || len(view.Questions) != 1 ||
		view.Questions[0].Text != "What on-call rotation do you cover?" {
		t.Fatalf("view = %+v, want one fetched question", view)
	}
	if len(view.Vacancy.CaptureIDs) != 1 || view.Vacancy.CaptureIDs[0] != fetchCapture.ID {
		t.Fatalf("vacancy captures = %v, want the fetched capture", view.Vacancy.CaptureIDs)
	}
	if executor.count() != 1 {
		t.Fatalf("executor calls = %d, want exactly 1 bounded fetch", executor.count())
	}
	if len(executor.inputs) != 1 || executor.inputs[0].Request.URLOrQuery != "https://jobs.example.invalid/1" {
		t.Fatalf("fetch inputs = %+v, want the vacancy page only", executor.inputs)
	}
}

func TestCheckBlockedWithoutQuestions(t *testing.T) {
	ctx := context.Background()
	gate := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	fix := newConnectedFixture(t, gate, []map[string]string{
		{"reason-positive": "hybrid-ok", "reason-negative": "abstain", "reason-missing": "abstain"},
	})
	commissioned := commissionForCheck(t, fix, gate, "run-blocked", []vacancySeed{
		{"https://jobs.example.invalid/1", "Example BV", "Senior support engineer", "rc-1"},
	})
	finding := commissioned.Findings[0]
	// The capture holds prose but no interrogative lines: a checked role
	// with zero questions completes as blocked/questions-unresolved.
	fix.captures.blobs[finding.EvidenceLinks[0].CaptureID] = []byte("<html><body>\nSenior role, no questions listed.\nApply by email.\n</body></html>")
	opportunityID := finding.OpportunityID
	selectFixtureRole(t, fix.db, opportunityID, finding.OpportunityRevision)
	pending := startFixtureCheck(t, fix.db, opportunityID, finding.OpportunityRevision)
	checker, err := NewChecker(CheckDeps{DB: fix.db, Actor: fixtureActor,
		Executor: fix.executor, Captures: fix.captures, Bounds: musecode.DefaultBounds(), Authorized: true})
	if err != nil {
		t.Fatal(err)
	}
	view, err := checker.PerformCheck(ctx, opportunityID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != store.CheckStatusBlocked || view.BlockedReason == nil ||
		view.BlockedReason.Code != store.CheckBlockedQuestionsUnresolved {
		t.Fatalf("view = %+v, want blocked/questions-unresolved", view)
	}
	if len(view.Questions) != 0 || len(view.Vacancy.CaptureIDs) != 1 {
		t.Fatalf("view = %+v, want no questions with retrieved capture", view)
	}
}

func TestCheckerGuards(t *testing.T) {
	ctx := context.Background()
	db := openFixtureDB(t)
	deps := CheckDeps{DB: db, Actor: fixtureActor, Executor: &fakeExecutor{},
		Captures: &fakeCaptures{}, Bounds: musecode.DefaultBounds(), Authorized: true}
	if _, err := NewChecker(deps); err != nil {
		t.Fatalf("valid deps rejected: %v", err)
	}
	badBounds := deps
	badBounds.Bounds = musecode.Bounds{}
	if _, err := NewChecker(badBounds); err == nil {
		t.Error("zero bounds admitted")
	}
	closed, err := NewChecker(CheckDeps{DB: db, Actor: fixtureActor, Executor: &fakeExecutor{},
		Captures: &fakeCaptures{}, Bounds: musecode.DefaultBounds()})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Authorized() {
		t.Error("default checker authorized")
	}
	if _, err := closed.PerformCheck(ctx, "opp-1", "check-1"); err == nil || !strings.Contains(err.Error(), "E12") {
		t.Fatalf("unauthorized perform err = %v, want E12 gate", err)
	}
	company, _, err := db.CreateCompany(ctx, fixtureActor, store.CompanyInput{Name: "Manual Co"})
	if err != nil {
		t.Fatal(err)
	}
	manual, _, err := db.CreateOpportunity(ctx, fixtureActor, store.OpportunityInput{
		CompanyID: company.ID, Title: "Manual role", Kind: "employment",
		SourceURL: "https://manual.example.invalid/jobs/1", Stage: "found",
	})
	if err != nil {
		t.Fatal(err)
	}
	selectFixtureRole(t, db, manual.ID, manual.Revision)
	pending := startFixtureCheck(t, db, manual.ID, manual.Revision)
	checker, err := NewChecker(deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checker.PerformCheck(ctx, manual.ID, pending.ID); !errors.Is(err, ErrCheckNotMuse) {
		t.Fatalf("manual role err = %v, want ErrCheckNotMuse", err)
	}
	still, err := db.CurrentJobCheck(ctx, manual.ID)
	if err != nil || still.Status != store.CheckStatusChecking {
		t.Fatalf("manual check = %+v %v, want untouched pending", still, err)
	}
}
