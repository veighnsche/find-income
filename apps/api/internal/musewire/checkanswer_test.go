package musewire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
	"github.com/veighnsche/find-income-dashboard/api/internal/publicresearch"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// checkTurn scripts one fixture check turn: the turn emits native
// retrieval events plus its structured findings text citing source URLs,
// and the checker fetches and verifies everything itself. Extra prompts
// save directly as saved-but-uncited questions to exercise that gap.
type checkTurn struct {
	text  string
	extra []string
	err   error
}

// checkScriptTransport plays one scripted check turn like the direct
// CLI: structured findings text only, no saves. The checker performs
// the deterministic fetch-and-verify behind it.
type checkScriptTransport struct {
	t       *testing.T
	checker *Checker
	checkID string
	turn    checkTurn
}

func (f *checkScriptTransport) Run(ctx context.Context, _ musecode.SessionSpec, input musecode.SessionInput, _ musecode.Cursor, sink musecode.EventSink) error {
	check, ok := input.(musecode.CheckInput)
	if !ok {
		return errors.New("fixture: check turn needs CheckInput")
	}
	if check.VacancyRef == "" || check.PageURL == "" || check.ReceiptRef == "" {
		return errors.New("fixture: check input incomplete")
	}
	if f.turn.err != nil {
		return f.turn.err
	}
	server, ok := f.checker.ServerForCheck(checkRefOf(f.checkID))
	if !ok {
		return errors.New("fixture: no live check server")
	}
	vacancies := server.SavedVacancyRefs()
	if len(vacancies) != 1 {
		return fmt.Errorf("fixture: %d seeded vacancies, want 1", len(vacancies))
	}
	for _, prompt := range f.turn.extra {
		if _, err := server.SaveQuestion(ctx, publicresearch.SaveQuestionInput{
			VacancyRef: vacancies[0], PromptText: prompt, SourceURL: check.PageURL,
		}); err != nil {
			return fmt.Errorf("fixture: save extra question: %w", err)
		}
	}
	sink.Emit(musecode.Event{Kind: musecode.EventModelStep})
	sink.Emit(musecode.Event{Kind: musecode.EventToolCall, Tool: "web_fetch"})
	sink.Emit(musecode.Event{Kind: musecode.EventToolResult, Tool: "web_fetch", BytesOut: 64})
	if f.turn.text != "" {
		sink.Emit(musecode.Event{Kind: musecode.EventModelText, Text: f.turn.text, BytesOut: int64(len(f.turn.text))})
	}
	sink.Emit(musecode.Event{Kind: musecode.EventFinished})
	return nil
}

func newCheckFixture(t *testing.T, fix *connectedFixture, transport *checkScriptTransport) *Checker {
	t.Helper()
	checker, err := NewChecker(CheckDeps{DB: fix.db, Actor: fixtureActor,
		Executor: fix.executor, Captures: fix.captures,
		Bounds: musecode.DefaultBounds(), Transport: transport,
		Cursors: StoreCursors{DB: fix.db}, Facts: fixtureFacts(),
		Workspaces: t.TempDir(), Authorized: true})
	if err != nil {
		t.Fatal(err)
	}
	transport.checker = checker
	return checker
}

func waitCheckDone(t *testing.T, db *store.Store, opportunityID, checkID string) store.CheckView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		view, err := db.GetJobCheck(context.Background(), opportunityID, checkID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Status != store.CheckStatusChecking {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("check still pending: %+v", view)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

// Discovery yields a classified role, the explicit check conducts one
// bounded turn that saves the actual employer questions and returns
// span-verified findings, and Answer matches saved owner answers with
// zero generative or retrieval calls: exactly one Jev classifier batch,
// nothing else.
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

	transport := &checkScriptTransport{t: t, checkID: pending.ID}
	checker := newCheckFixture(t, fix, transport)
	captureID := finding.EvidenceLinks[0].CaptureID
	pageURL := "https://jobs.example.invalid/1"
	fix.executor.scriptFetch(pageURL, "rc-check-fetch", captureID, fix.captures.blobs[captureID])
	transport.turn = checkTurn{
		text: fmt.Sprintf(`{"requirements":[{"text":"Base pay unstated.","source":%q}],`+
			`"route":{"kind":"direct","destination":"","source":""},"documents":[],`+
			`"questions":[{"prompt_text":"Why do you want this support role?","required":false,"source":%q},`+
			`{"prompt_text":"Are you available for night shifts (required)?","required":true,"source":%q}],"gaps":[]}`,
			pageURL, pageURL, pageURL),
	}
	started, err := checker.PerformCheck(ctx, opportunityID, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != store.CheckStatusChecking {
		t.Fatalf("start = %+v, want the checking view while the turn conducts", started)
	}
	view := waitCheckDone(t, fix.db, opportunityID, pending.ID)
	if view.Status != store.CheckStatusChecked || len(view.Questions) != 2 {
		t.Fatalf("check = %+v, want checked with 2 sourced questions", view)
	}
	if len(view.Requirements) != 1 || view.Requirements[0].Statement != "Base pay unstated." {
		t.Fatalf("requirements = %+v, want the verified statement", view.Requirements)
	}
	if view.Questions[0].Text != "Why do you want this support role?" || view.Questions[0].Required != store.CheckOptional {
		t.Fatalf("question 0 = %+v, want verbatim optional", view.Questions[0])
	}
	if view.Questions[1].Text != "Are you available for night shifts (required)?" || view.Questions[1].Required != store.CheckRequired {
		t.Fatalf("question 1 = %+v, want verbatim required", view.Questions[1])
	}
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
	if view.QuestionSetSHA256 == "" || view.Route.Judgment != "unresolved" || len(view.Gaps) != 0 {
		t.Fatalf("view = %+v, want set hash, unresolved route and no gaps", view)
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
	if fix.executor.count() != 1 {
		t.Fatalf("executor calls = %d, want the one deterministic listing fetch", fix.executor.count())
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
	if fix.executor.count() != 1 {
		t.Fatalf("executor calls after answer = %d, want frozen at the check fetch (zero retrieval in Answer)", fix.executor.count())
	}
	if fix.provider.count() != 1 {
		t.Fatalf("screening calls = %d, want frozen at 1", fix.provider.count())
	}
}

// An invented requirement, an unknown question ref and a saved-but-uncited
// question all drop as gaps; with zero verified questions the check
// completes as blocked/questions-unresolved with the gaps disclosed.
func TestCheckBlockedOnUnverifiable(t *testing.T) {
	ctx := context.Background()
	gate := &gateTransport{started: make(chan struct{}), proceed: make(chan struct{})}
	fix := newConnectedFixture(t, gate, []map[string]string{
		{"reason-positive": "hybrid-ok", "reason-negative": "abstain", "reason-missing": "abstain"},
	})
	commissioned := commissionForCheck(t, fix, gate, "run-unverifiable", []vacancySeed{
		{"https://jobs.example.invalid/1", "Example BV", "Senior support engineer", "rc-1"},
	})
	finding := commissioned.Findings[0]
	opportunityID := finding.OpportunityID
	selectFixtureRole(t, fix.db, opportunityID, finding.OpportunityRevision)
	pending := startFixtureCheck(t, fix.db, opportunityID, finding.OpportunityRevision)
	transport := &checkScriptTransport{t: t, checkID: pending.ID}
	checker := newCheckFixture(t, fix, transport)
	captureID := finding.EvidenceLinks[0].CaptureID
	pageURL := "https://jobs.example.invalid/1"
	fix.executor.scriptFetch(pageURL, "rc-check-fetch", captureID, fix.captures.blobs[captureID])
	transport.turn = checkTurn{
		extra: []string{"Why do you want this support role?"},
		text: fmt.Sprintf(`{"requirements":[{"text":"Five years of Go are mandatory.","source":%q}],`+
			`"route":{"kind":"direct","destination":"","source":""},"documents":[],`+
			`"questions":[{"prompt_text":"What is your quest?","required":true,"source":"https://jobs.example.invalid/gone"}],"gaps":[]}`,
			pageURL),
	}
	if _, err := checker.PerformCheck(ctx, opportunityID, pending.ID); err != nil {
		t.Fatal(err)
	}
	view := waitCheckDone(t, fix.db, opportunityID, pending.ID)
	if view.Status != store.CheckStatusBlocked || view.BlockedReason == nil ||
		view.BlockedReason.Code != store.CheckBlockedQuestionsUnresolved {
		t.Fatalf("view = %+v, want blocked/questions-unresolved", view)
	}
	for _, want := range []string{"requirement 1 dropped", "question 1 dropped", "was not cited"} {
		if !strings.Contains(view.BlockedReason.Detail, want) {
			t.Errorf("blocked detail misses %q: %q", want, view.BlockedReason.Detail)
		}
	}
}

// A turn that saves nothing and returns empty findings completes as
// blocked/questions-unresolved with no questions and no captures.
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
	opportunityID := finding.OpportunityID
	selectFixtureRole(t, fix.db, opportunityID, finding.OpportunityRevision)
	pending := startFixtureCheck(t, fix.db, opportunityID, finding.OpportunityRevision)
	transport := &checkScriptTransport{t: t, checkID: pending.ID}
	checker := newCheckFixture(t, fix, transport)
	transport.turn = checkTurn{
		text: `{"requirements":[],"route":{"kind":"direct","destination":"","source":""},` +
			`"documents":[],"questions":[],"gaps":[]}`,
	}
	if _, err := checker.PerformCheck(ctx, opportunityID, pending.ID); err != nil {
		t.Fatal(err)
	}
	view := waitCheckDone(t, fix.db, opportunityID, pending.ID)
	if view.Status != store.CheckStatusBlocked || view.BlockedReason == nil ||
		view.BlockedReason.Code != store.CheckBlockedQuestionsUnresolved {
		t.Fatalf("view = %+v, want blocked/questions-unresolved", view)
	}
	if len(view.Questions) != 0 || len(view.Vacancy.CaptureIDs) != 0 {
		t.Fatalf("view = %+v, want no questions and no captures", view)
	}
}

func TestCheckerGuards(t *testing.T) {
	ctx := context.Background()
	db := openFixtureDB(t)
	deps := CheckDeps{DB: db, Actor: fixtureActor, Executor: &fakeExecutor{},
		Captures: &fakeCaptures{}, Bounds: musecode.DefaultBounds(),
		Transport: &checkScriptTransport{}, Cursors: StoreCursors{DB: db},
		Facts: fixtureFacts(), Workspaces: t.TempDir(), Authorized: true}
	if _, err := NewChecker(deps); err != nil {
		t.Fatalf("valid deps rejected: %v", err)
	}
	badBounds := deps
	badBounds.Bounds = musecode.Bounds{}
	if _, err := NewChecker(badBounds); err == nil {
		t.Error("zero bounds admitted")
	}
	badWorkspaces := deps
	badWorkspaces.Workspaces = "relative/root"
	if _, err := NewChecker(badWorkspaces); err == nil {
		t.Error("relative workspaces admitted")
	}
	closed, err := NewChecker(CheckDeps{DB: db, Actor: fixtureActor, Executor: &fakeExecutor{},
		Captures: &fakeCaptures{}, Bounds: musecode.DefaultBounds(),
		Transport: &checkScriptTransport{}, Cursors: StoreCursors{DB: db},
		Facts: fixtureFacts(), Workspaces: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Authorized() {
		t.Error("default checker authorized")
	}
	if _, err := closed.PerformCheck(ctx, "opp-1", "check-1"); err == nil || !strings.Contains(err.Error(), "live authorization") {
		t.Fatalf("unauthorized perform err = %v, want live-authorization gate", err)
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
