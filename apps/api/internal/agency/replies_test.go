package agency

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/replydraft"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type replyFixtureRuntime struct {
	db         *store.Store
	commission store.ReplyProcessing
	calls      int
	seen       string
}

func (f *replyFixtureRuntime) CheckRound(context.Context, string) error { return nil }
func (f *replyFixtureRuntime) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
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
	capability, err := f.db.IssueRoundToolCapability(ctx, roundID, turn.ID, agent.ID)
	if err != nil {
		return turn, err
	}
	thread, err := f.db.OwnerCorrespondenceThread(ctx, f.dbRoundActor(ctx, roundID), f.commission.ThreadID)
	if err != nil {
		return turn, err
	}
	_ = thread
	opportunity, err := f.db.Opportunity(ctx, f.opportunityID(ctx))
	if err != nil {
		return turn, err
	}
	if _, _, err = f.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: input.RequestKey + "/update", Operation: store.RoundReplyUpdateSave, ResourceID: input.ResourceID, ExpectedRevision: opportunity.Revision, ReplyUpdate: &store.ReplyUpdateMutation{ProcessingID: f.commission.ID, ThreadID: f.commission.ThreadID, OpportunityID: opportunity.ID}, Capability: capability}); err != nil {
		return turn, err
	}
	messages, err := f.db.CorrespondenceThreadMessages(ctx, f.dbRoundActor(ctx, roundID), f.commission.ThreadID)
	if err != nil || len(messages) == 0 {
		return turn, err
	}
	bodies := map[string]string{messages[0].ID: messages[0].Body}
	draft, err := replydraft.ValidateDraft(replydraft.DraftInput{ProcessingID: f.commission.ID, ThreadID: f.commission.ThreadID, Body: "Thank you for the Tuesday invitation.", Citations: []replydraft.DraftCitation{{MessageID: messages[0].ID, Excerpt: "interview on Tuesday"}}, Unknowns: []string{"Format unknown."}}, bodies)
	if err != nil {
		return turn, err
	}
	encoded, _ := json.Marshal(draft)
	if _, _, err = f.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: input.RequestKey + "/draft", Operation: store.RoundReplyDraftSave, ResourceID: input.ResourceID, ExpectedRevision: 1, ReplyDraft: &store.ReplyDraftMutation{ProcessingID: f.commission.ID, ThreadID: f.commission.ThreadID, DraftJSON: encoded}, Capability: capability}); err != nil {
		return turn, err
	}
	if err = f.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, "fixture-reply-thread"); err != nil {
		return turn, err
	}
	if err = f.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-reply-thread", "fixture-reply-turn"); err != nil {
		return turn, err
	}
	if err = f.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, "fixture-reply-thread", "fixture-reply-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	return f.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"saved":true}`), "")
}

func (f *replyFixtureRuntime) dbRoundActor(ctx context.Context, roundID string) string {
	r, err := f.db.Round(ctx, roundID)
	if err != nil {
		return ""
	}
	return r.Actor.ID
}

func (f *replyFixtureRuntime) opportunityID(ctx context.Context) string {
	processing, err := f.db.ReplyProcessing(ctx, f.commission.ID)
	if err != nil || processing.OpportunityID == "" {
		page, err := f.db.ListOpportunities(ctx, store.OpportunityListOptions{Limit: 1})
		if err != nil || len(page.Items) == 0 {
			return ""
		}
		return page.Items[0].ID
	}
	return processing.OpportunityID
}

func agencyReplyFixture(t *testing.T) (*store.Store, store.Actor, store.ReplyProcessing, store.Opportunity) {
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
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Backend Engineer", Kind: "employment", Stage: "new", SourceURL: "https://example.test/reply", OriginalText: "Build Go services."})
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := db.ConnectCorrespondenceAccount(ctx, owner, store.CorrespondenceAccountInput{Provider: "fake", ExternalAccountID: "owner@example.test", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = db.SyncCorrespondenceThreads(ctx, owner, account.ID, []store.CorrespondenceThreadSnapshot{{ProviderThreadID: "thread-1", Subject: "Interview", LastMessageAt: "2026-09-24T10:00:00Z",
		Messages: []store.CorrespondenceMessageSnapshot{{ProviderMessageID: "m-1", Sender: "recruiter@example.test", Recipients: []string{"owner@example.test"}, SentAt: "2026-09-24T10:00:00Z", Body: "We invite you to interview on Tuesday."}}}})
	if err != nil {
		t.Fatal(err)
	}
	threads, err := db.OwnerCorrespondenceThreads(ctx, owner.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads: %+v %v", threads, err)
	}
	processing, _, err := db.CommissionReplyProcessing(ctx, owner, store.ReplyProcessingCommissionInput{RequestKey: "owner-thread", ThreadID: threads[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	return db, owner, processing, opportunity
}

func agencyReplyRound(processing store.ReplyProcessing, threadID string) store.StartRoundInput {
	return store.StartRoundInput{RequestKey: "reply-work", Intent: "Process one actual thread", Outcome: "process_replies", ProfileVersion: processing.ProfileVersion, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{InputRefs: []string{"replies:" + processing.ID}, Resources: []string{"thread:" + threadID, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundJevRequest, store.RoundReplyUpdateSave, store.RoundReplyDraftSave}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 5, Items: 2, Tools: 6, Turns: 1}}
}

func agencyReplyJevService(t *testing.T, db *store.Store, choice string) (jevservice.Service, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		probabilities := map[string]float64{}
		for id := range wire.Questions["reply_intent"].Criteria {
			probabilities[id] = 0
		}
		probabilities[choice] = 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"reply_intent": map[string]any{"type": "choice", "choice": choice, "confidence": 0.9, "probabilities": probabilities}}, "usage": map[string]any{"input_tokens": 6, "output_tokens": 2}})
	}))
	t.Setenv(jev.CredentialEnvironmentVariable, "fixture-only")
	cfg := jev.DefaultConfig()
	cfg.Enabled, cfg.Endpoint, cfg.Timeout, cfg.MaxAttempts = true, server.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return jevservice.Service{Store: db, Client: client}, server.Close
}

func TestCommissionedReplyWorkerProcessesThreadAndSavesDraft(t *testing.T) {
	ctx := context.Background()
	db, owner, processing, _ := agencyReplyFixture(t)
	intent, closeServer := agencyReplyJevService(t, db, "interview_invitation")
	defer closeServer()
	thread, err := db.OwnerCorrespondenceThread(ctx, owner.ID, processing.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &replyFixtureRuntime{db: db, commission: processing}
	engine := &Engine{Store: db, Runtime: runtime, ReplyIntent: intent, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	input := agencyReplyRound(processing, thread.ID)
	r, created, err := service.Start(ctx, owner, input)
	if err != nil || !created {
		t.Fatalf("start: %+v %v", r, err)
	}
	wait, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	if err := engine.WaitRoundStopped(wait, r.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := db.ReplyProcessing(ctx, processing.ID)
	if err != nil || saved.RoundID != r.ID || saved.Intent != "interview_invitation" || saved.OpportunityID == "" {
		t.Fatalf("saved processing: %+v %v", saved, err)
	}
	if _, err := db.ReplyDraftForProcessing(ctx, processing.ID); err != nil {
		t.Fatalf("draft: %v", err)
	}
	finished, err := db.Round(ctx, r.ID)
	if err != nil || finished.State != store.RoundCompleted || runtime.calls != 1 || !strings.Contains(runtime.seen, "interview on Tuesday") {
		t.Fatalf("outcome=%+v calls=%d err=%v", finished, runtime.calls, err)
	}
	var report replyOutcome
	if json.Unmarshal(finished.Report, &report) != nil || report.Code != "replies_processed" || !report.DraftSaved || !report.UpdatesSaved {
		t.Fatalf("report=%+v", report)
	}
	if _, created, err := service.Start(ctx, owner, input); err != nil || created || runtime.calls != 1 {
		t.Fatalf("replay launched work: created=%v calls=%d err=%v", created, runtime.calls, err)
	}
}

func TestReplyUnresolvedIntentRemainsPartialWithDraft(t *testing.T) {
	ctx := context.Background()
	db, owner, processing, _ := agencyReplyFixture(t)
	intent, closeServer := agencyReplyJevService(t, db, "__unresolved__")
	defer closeServer()
	thread, err := db.OwnerCorrespondenceThread(ctx, owner.ID, processing.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &replyFixtureRuntime{db: db, commission: processing}
	engine := &Engine{Store: db, Runtime: runtime, ReplyIntent: intent, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	r, _, err := service.Start(ctx, owner, agencyReplyRound(processing, thread.ID))
	if err != nil {
		t.Fatal(err)
	}
	wait, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	if err := engine.WaitRoundStopped(wait, r.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := db.Round(ctx, r.ID)
	if err != nil || finished.State != store.RoundCompleted || finished.DeliverableStatus != "partial" {
		t.Fatalf("outcome=%+v err=%v", finished, err)
	}
	var report replyOutcome
	if json.Unmarshal(finished.Report, &report) != nil || report.Code != "replies_intent_unresolved" || !report.DraftSaved {
		t.Fatalf("report=%+v", report)
	}
}

func stoppingReplyDecisions(t *testing.T, db *store.Store) (jevservice.Service, *atomic.Int32, func()) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var wire struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		probabilities := map[string]float64{}
		for id := range wire.Questions["reply_intent"].Criteria {
			probabilities[id] = 0
		}
		probabilities["interview_invitation"] = 1
		active, err := db.ActiveRound(context.Background())
		if err == nil {
			_, _, err = db.StopRound(context.Background(), active.Actor, active.ID)
		}
		if err == nil {
			_, err = db.PauseStoppedRound(context.Background(), active.Actor, active.ID)
		}
		if err != nil {
			t.Errorf("stop during Jev response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"reply_intent": map[string]any{"type": "choice", "choice": "interview_invitation", "confidence": 0.9, "probabilities": probabilities}}, "usage": map[string]any{"input_tokens": 6, "output_tokens": 2}})
	}))
	t.Setenv(jev.CredentialEnvironmentVariable, "fixture-only")
	cfg := jev.DefaultConfig()
	cfg.Enabled, cfg.Endpoint, cfg.Timeout, cfg.MaxAttempts = true, server.URL+"/v1/systemone", time.Second, 1
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return jevservice.Service{Store: db, Client: client}, calls, server.Close
}

func TestCapturedReplyIntentResumesLocallyWithoutAnotherCharge(t *testing.T) {
	ctx := context.Background()
	db, owner, processing, _ := agencyReplyFixture(t)
	thread, err := db.OwnerCorrespondenceThread(ctx, owner.ID, processing.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := db.CorrespondenceThreadMessages(ctx, owner.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	round, _, err := db.StartRound(ctx, owner, agencyReplyRound(processing, thread.ID))
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindReplyRound(ctx, owner, processing.ID, round.ID); err != nil {
		t.Fatal(err)
	}
	intent, calls, closeServer := stoppingReplyDecisions(t, db)
	defer closeServer()
	engine := &Engine{Store: db, ReplyIntent: intent, Context: ctx}
	r, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, status := engine.assessReplyIntent(ctx, r, processing, thread, messages); status != "unavailable" {
		t.Fatalf("stopped assess status=%s", status)
	}
	paused, err := db.Round(ctx, round.ID)
	if err != nil || paused.State != store.RoundPaused || calls.Load() != 1 {
		t.Fatalf("paused=%+v calls=%d err=%v", paused, calls.Load(), err)
	}
	bridge, err := codexservice.New(ctx, db, codexservice.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	runtime := &replyFixtureRuntime{db: db, commission: processing}
	engine.Runtime = runtime
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine, Reconciler: bridge}
	resumed, err := service.Resume(ctx, owner, round.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	_ = resumed
	wait, done := context.WithTimeout(ctx, 5*time.Second)
	defer done()
	if err := engine.WaitRoundStopped(wait, round.ID); err != nil {
		t.Fatal(err)
	}
	attempt, err := db.RoundAttemptForRequest(ctx, round.ID, "reply-intent:"+processing.ID+"/0")
	if err != nil || attempt.State != store.AttemptObservedSuccess || calls.Load() != 1 {
		t.Fatalf("intent was re-dispatched: attempt=%+v calls=%d err=%v", attempt, calls.Load(), err)
	}
	saved, err := db.ReplyProcessing(ctx, processing.ID)
	if err != nil || saved.Intent != "interview_invitation" {
		t.Fatalf("intent not applied after resume: %+v %v", saved, err)
	}
	finished, err := db.Round(ctx, round.ID)
	if err != nil || finished.State != store.RoundCompleted {
		t.Fatalf("resumed outcome=%+v err=%v", finished, err)
	}
	var report replyOutcome
	if json.Unmarshal(finished.Report, &report) != nil || report.Code != "replies_processed" {
		t.Fatalf("resumed report=%+v", report)
	}
}
