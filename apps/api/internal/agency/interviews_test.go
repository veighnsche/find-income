package agency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/applicationpacks"
	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/interviewprep"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type interviewFixtureSources struct{}

type interviewUnavailableFocus struct{}

func (interviewUnavailableFocus) RunInterviewFocus(context.Context, jevservice.Binding, jev.InterviewFocusInput) (jev.InterviewFocusResult, error) {
	return jev.InterviewFocusResult{}, errors.New("focus unavailable")
}

type interviewObservedSuccess struct{}

func (interviewObservedSuccess) ObserveDispatch(context.Context, string) (rounds.Observation, error) {
	return rounds.Observation{State: store.AttemptObservedSuccess, Evidence: json.RawMessage(`{"status":"completed"}`)}, nil
}

func (interviewFixtureSources) LoadPackSources(context.Context) ([]applicationpacks.Source, error) {
	return []applicationpacks.Source{{ID: "cv-vince-liem.typ", Approved: true, Body: "Approved template"}, {ID: "cv-vince-liem.md", Approved: true, Body: "Approved career"}, {ID: "github-evidence-review.md", Approved: true, Body: "Approved project"}}, nil
}

type interviewFixtureRuntime struct {
	db         *store.Store
	commission store.Interview
	calls      int
	entered    chan struct{}
	hold       bool
	seen       string
}

func (f *interviewFixtureRuntime) CheckRound(context.Context, string) error { return nil }
func (f *interviewFixtureRuntime) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	f.calls++
	f.seen = input.Evidence
	turn, _, err := f.db.ReserveRoundAttempt(ctx, agent, roundID, store.RoundAttemptInput{RequestKey: input.RequestKey, Operation: store.RoundCodexTurn, ResourceID: input.ResourceID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		return turn, err
	}
	turn, err = f.db.MarkRoundDispatched(ctx, roundID, turn.ID)
	if err != nil {
		return turn, err
	}
	if f.entered != nil {
		close(f.entered)
	}
	if f.hold {
		<-ctx.Done()
		return turn, ctx.Err()
	}
	capability, err := f.db.IssueRoundToolCapability(ctx, roundID, turn.ID, agent.ID)
	if err != nil {
		return turn, err
	}
	opportunity, err := f.db.Opportunity(ctx, f.commission.OpportunityID)
	if err != nil {
		return turn, err
	}
	company, err := f.db.Company(ctx, opportunity.CompanyID)
	if err != nil {
		return turn, err
	}
	h := sha256.Sum256([]byte(opportunity.OriginalText))
	citation := applicationpacks.Citation{SourceID: "role", Excerpt: "Maintain Go services"}
	brief, err := interviewprep.Prepare(interviewprep.Input{InterviewID: f.commission.ID, OpportunityID: opportunity.ID, RoleTitle: opportunity.Title, EmployerName: company.Name, Context: []interviewprep.ContextSource{{ID: "owner-input", Kind: "owner_input", Revision: f.commission.ContextSHA256, SHA256: f.commission.ContextSHA256, Body: f.commission.Context}, {ID: "role", Kind: "role", Revision: "1", SHA256: hex.EncodeToString(h[:]), Body: opportunity.OriginalText}}, Draft: interviewprep.Draft{Focus: []interviewprep.FocusAlternative{{ID: "go", Why: interviewprep.CitedText{Text: "Discuss the saved Go duties.", Citations: []applicationpacks.Citation{citation}}}}, Questions: []interviewprep.Question{{Text: "How are Go changes reviewed?", Why: interviewprep.CitedText{Text: "Go is listed in the role.", Citations: []applicationpacks.Citation{citation}}}}, Unknowns: []string{"No career example or interview schedule was supplied."}}})
	if err != nil {
		return turn, err
	}
	encoded, _ := json.Marshal(brief)
	_, _, err = f.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "brief", Operation: store.RoundInterviewBriefSave, ResourceID: input.ResourceID, ExpectedRevision: opportunity.Revision, InterviewBrief: &store.InterviewBriefMutation{InterviewID: f.commission.ID, OpportunityID: opportunity.ID, InputSHA256: brief.InputSHA256, BriefJSON: encoded}, Capability: capability})
	if err != nil {
		return turn, err
	}
	if err = f.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, "fixture-interview-thread"); err != nil {
		return turn, err
	}
	if err = f.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-interview-thread", "fixture-interview-turn"); err != nil {
		return turn, err
	}
	if err = f.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-interview-thread", "fixture-interview-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	return f.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"saved":true}`), "")
}

func agencyInterviewFixture(t *testing.T) (*store.Store, store.Actor, store.Interview, store.Opportunity) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Fixture Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Platform Engineer", Kind: "employment", Stage: "new", SourceURL: "https://example.test/role", OriginalText: "Maintain Go services with a small engineering team."})
	if err != nil {
		t.Fatal(err)
	}
	interview, _, err := db.CommissionInterview(ctx, owner, store.InterviewCommissionInput{RequestKey: "owner-invite", OpportunityID: opportunity.ID, Context: "The owner supplied the complete invitation: discuss Go services. No date was supplied."})
	if err != nil {
		t.Fatal(err)
	}
	return db, owner, interview, opportunity
}

func agencyInterviewRound(interview store.Interview) store.StartRoundInput {
	return store.StartRoundInput{RequestKey: "interview-work", Intent: "Prepare one actual interview", Outcome: "interview_prepare", ProfileVersion: interview.ProfileVersion, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{InputRefs: []string{"interview:" + interview.ID}, Resources: []string{"opportunity:" + interview.OpportunityID}, Operations: []string{store.RoundCodexTurn, store.RoundInterviewBriefSave, store.RoundJevRequest}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 2, Items: 1, Tools: 2, Turns: 1}}
}

func TestCommissionedInterviewWorkerRetainsBriefWhenFocusUnresolved(t *testing.T) {
	ctx := context.Background()
	db, owner, interview, _ := agencyInterviewFixture(t)
	runtime := &interviewFixtureRuntime{db: db, commission: interview}
	engine := &Engine{Store: db, Runtime: runtime, InterviewSources: interviewFixtureSources{}, InterviewFocus: interviewUnavailableFocus{}, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	input := agencyInterviewRound(interview)
	r, created, err := service.Start(ctx, owner, input)
	if err != nil || !created {
		t.Fatalf("start: %+v %v", r, err)
	}
	wait, done := context.WithTimeout(ctx, time.Second)
	defer done()
	if err := engine.WaitRoundStopped(wait, r.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := db.Interview(ctx, interview.ID)
	if err != nil || saved.RoundID != r.ID || len(saved.Brief) == 0 || len(saved.Focus) != 0 {
		t.Fatalf("saved brief: %+v %v", saved, err)
	}
	finished, err := db.Round(ctx, r.ID)
	if err != nil || finished.State != store.RoundCompleted || finished.DeliverableStatus != "partial" || runtime.calls != 1 || !strings.Contains(runtime.seen, interview.Context) {
		t.Fatalf("outcome=%+v calls=%d err=%v", finished, runtime.calls, err)
	}
	if _, created, err := service.Start(ctx, owner, input); err != nil || created || runtime.calls != 1 {
		t.Fatalf("replay launched work: created=%v calls=%d err=%v", created, runtime.calls, err)
	}
}

func TestStoppedInterviewDoesNotRedispatchUncertainTurn(t *testing.T) {
	ctx := context.Background()
	db, owner, interview, _ := agencyInterviewFixture(t)
	runtime := &interviewFixtureRuntime{db: db, commission: interview, entered: make(chan struct{}), hold: true}
	engine := &Engine{Store: db, Runtime: runtime, InterviewSources: interviewFixtureSources{}, InterviewFocus: interviewUnavailableFocus{}, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	r, _, err := service.Start(ctx, owner, agencyInterviewRound(interview))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.entered:
	case <-time.After(time.Second):
		t.Fatal("turn did not dispatch")
	}
	stopped, err := service.Stop(ctx, owner, r.ID)
	if err != nil || stopped.State != store.RoundPaused {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	if _, err := service.Resume(ctx, owner, r.ID); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("uncertain turn resumed without reconciliation: %v", err)
	}
	service.Reconciler = interviewObservedSuccess{}
	resumed, err := service.Resume(ctx, owner, r.ID)
	if err != nil || resumed.State != store.RoundRunning {
		t.Fatalf("reconciled resume: %+v %v", resumed, err)
	}
	wait, done := context.WithTimeout(ctx, time.Second)
	defer done()
	if err := engine.WaitRoundStopped(wait, r.ID); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 {
		t.Fatalf("turn dispatched %d times", runtime.calls)
	}
}

func TestInterviewUnresolvedFocusRemainsPartial(t *testing.T) {
	if !interviewFocusUnresolved(json.RawMessage(`{"selection":{"disposition":"unresolved"}}`)) {
		t.Fatal("unresolved focus shown complete")
	}
	if interviewFocusUnresolved(json.RawMessage(`{"selection":{"disposition":"selected"}}`)) {
		t.Fatal("selected focus shown unresolved")
	}
}

func agencyInterviewJevClient(t *testing.T, provider *httptest.Server) *jev.Client {
	t.Helper()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-key")
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = provider.URL + "/v1/systemone"
	cfg.MaxAttempts = 1
	client, err := jev.NewFromEnvironment(cfg, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCommissionedInterviewFocusRunsAfterSettledTurn(t *testing.T) {
	ctx := context.Background()
	db, owner, interview, _ := agencyInterviewFixture(t)
	runtime := &interviewFixtureRuntime{db: db, commission: interview}
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		round, err := db.ActiveRound(ctx)
		if err != nil {
			t.Errorf("active round: %v", err)
		}
		turn, err := db.RoundAttemptForRequest(ctx, round.ID, "interview:"+interview.ID)
		if err != nil || turn.State != store.AttemptSucceeded {
			t.Errorf("Jev called before turn settled: %+v %v", turn, err)
		}
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"interview_focus":{"type":"choice","choice":"go","probabilities":{"go":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer provider.Close()
	engine := &Engine{Store: db, Runtime: runtime, InterviewSources: interviewFixtureSources{}, InterviewFocus: jevservice.Service{Store: db, Client: agencyInterviewJevClient(t, provider)}, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	r, _, err := service.Start(ctx, owner, agencyInterviewRound(interview))
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := engine.WaitRoundStopped(wait, r.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := db.Interview(ctx, interview.ID)
	if err != nil || len(saved.Brief) == 0 || len(saved.Focus) == 0 || calls != 1 || runtime.calls != 1 {
		t.Fatalf("saved=%+v err=%v Jev calls=%d turns=%d", saved, err, calls, runtime.calls)
	}
	finished, err := db.Round(ctx, r.ID)
	if err != nil || finished.State != store.RoundCompleted || finished.DeliverableStatus != "complete" {
		t.Fatalf("round=%+v err=%v", finished, err)
	}
}

func TestInterviewResumeUsesPostTurnCapturedFocusWithoutAnotherCall(t *testing.T) {
	ctx := context.Background()
	db, owner, interview, opportunity := agencyInterviewFixture(t)
	runtime := &interviewFixtureRuntime{db: db, commission: interview}
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"interview_focus":{"type":"choice","choice":"go","probabilities":{"go":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer provider.Close()
	focus := jevservice.Service{Store: db, Client: agencyInterviewJevClient(t, provider)}
	engine := &Engine{Store: db, Runtime: runtime, InterviewSources: interviewFixtureSources{}, InterviewFocus: focus, Context: ctx}
	r, _, err := db.StartRound(ctx, owner, agencyInterviewRound(interview))
	if err != nil {
		t.Fatal(err)
	}
	r, err = db.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindInterviewRound(ctx, owner, interview.ID, r.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, codexservice.RoundTurnInput{RequestKey: "interview:" + interview.ID, ResourceID: "opportunity:" + opportunity.ID, Brief: "Prepare", Evidence: "owner context"}); err != nil {
		t.Fatal(err)
	}
	saved, err := db.Interview(ctx, interview.ID)
	if err != nil {
		t.Fatal(err)
	}
	var brief interviewprep.Brief
	if err := json.Unmarshal(saved.Brief, &brief); err != nil {
		t.Fatal(err)
	}
	input, err := brief.FocusInput(2500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := focus.RunInterviewFocus(ctx, jevservice.Binding{Actor: store.Actor{Kind: "agent", ID: "codex-runner"}, RoundID: r.ID, ResourceID: "opportunity:" + opportunity.ID, RequestKeyPrefix: "interview-focus:" + interview.ID, ProfileVersion: r.ProfileVersion, MaxReportedTokens: 2500}, input); err != nil {
		t.Fatal(err)
	}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	paused, err := service.Stop(ctx, owner, r.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("stop=%+v err=%v", paused, err)
	}
	resumed, err := service.Resume(ctx, owner, r.ID)
	if err != nil || resumed.Used != paused.Used {
		t.Fatalf("resume=%+v err=%v", resumed, err)
	}
	wait, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := engine.WaitRoundStopped(wait, r.ID); err != nil {
		t.Fatal(err)
	}
	saved, err = db.Interview(ctx, interview.ID)
	if err != nil || len(saved.Focus) == 0 || calls != 1 || runtime.calls != 1 {
		t.Fatalf("focus=%+v err=%v Jev calls=%d turns=%d", saved, err, calls, runtime.calls)
	}
}
