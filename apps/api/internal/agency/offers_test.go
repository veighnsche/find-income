package agency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/offercomparison"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type offerRuntimeFixture struct {
	db       *store.Store
	turns    int
	evidence string
}

func (f *offerRuntimeFixture) CheckRound(context.Context, string) error { return nil }
func (f *offerRuntimeFixture) ExecuteRoundTurn(ctx context.Context, agent store.Actor, roundID string, input codexservice.RoundTurnInput) (store.RoundAttempt, error) {
	f.turns++
	f.evidence = input.Evidence
	turn, _, err := f.db.ReserveRoundAttempt(ctx, agent, roundID, store.RoundAttemptInput{RequestKey: input.RequestKey, Operation: store.RoundCodexTurn, ResourceID: input.ResourceID, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		return turn, err
	}
	turn, err = f.db.MarkRoundDispatched(ctx, roundID, turn.ID)
	if err != nil {
		return turn, err
	}
	if err = f.db.BindRoundThread(ctx, roundID, turn.ID, turn.Generation, "offer-fixture-thread"); err != nil {
		return turn, err
	}
	if err = f.db.BindRoundTurn(ctx, roundID, turn.ID, turn.Generation, "offer-fixture-thread", "offer-fixture-turn"); err != nil {
		return turn, err
	}
	if err = f.db.ObserveRoundTurn(ctx, roundID, turn.ID, turn.Generation, "offer-fixture-thread", "offer-fixture-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		return turn, err
	}
	capability, err := f.db.IssueRoundToolCapability(ctx, roundID, turn.ID, agent.ID)
	if err != nil {
		return turn, err
	}
	intake, err := f.db.OfferIntake(ctx, strings.TrimPrefix(input.ResourceID, "offer_intake:"))
	if err != nil {
		return turn, err
	}
	source := intake.Sources[0]
	comparison, err := offercomparison.Prepare(offercomparison.Input{Sources: intake.Sources, Offers: []offercomparison.Offer{{ID: source.OfferID, Employer: "Fixture Labs", EmployerCitation: &offercomparison.Citation{SourceID: source.ID, Excerpt: "Fixture Labs"}, Engagement: "unknown", Pay: offercomparison.PayTerm{AmountKind: "unknown", Period: fit.UnknownPeriod, Basis: fit.UnknownBasis}, Holiday: offercomparison.HolidayTerm{Treatment: "unknown"}}}, Alternatives: []offercomparison.Alternative{{ID: "clarify-hours", Kind: "clarify", Why: offercomparison.CitedText{Text: "Clarify the weekly hours", Citations: []offercomparison.Citation{{SourceID: source.ID, Excerpt: "Clarify weekly hours"}}}}}})
	if err != nil {
		return turn, err
	}
	_, _, err = f.db.ApplyRoundMutation(ctx, agent, roundID, store.RoundMutationInput{RequestKey: "save-comparison", Operation: store.RoundPrepareOfferComparison, ResourceID: input.ResourceID, ExpectedRevision: 1, Capability: capability, OfferComparison: &store.OfferComparisonMutationInput{IntakeID: intake.ID, Comparison: comparison}})
	if err != nil {
		return turn, err
	}
	return f.db.FinishRoundAttempt(ctx, agent, roundID, turn.ID, true, json.RawMessage(`{"saved":true}`), "")
}

func TestCommissionedOfferComparisonPersistsAndChargesCapturedTradeoff(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := store.Actor{Kind: "administrator", ID: "offer-owner"}
	text := "Fixture Labs makes an offer. Clarify weekly hours before deciding."
	intake, _, err := db.CreateOfferIntake(ctx, owner, "offer-request", []string{text}, "I value predictable hours.")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "offer_intake:" + intake.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "offer-request", Intent: "Compare offers", Outcome: "compare_offers", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Minute), Scope: store.RoundScope{InputRefs: []string{resource}, Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundPrepareOfferComparison, store.RoundJevRequest}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 5, Items: 1, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"offer_tradeoff":{"type":"choice","choice":"clarify-hours","probabilities":{"clarify-hours":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":100,"output_tokens":10}}`))
	}))
	defer server.Close()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-offer-key")
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = server.URL + "/v1/systemone"
	cfg.Timeout = time.Second
	cfg.MaxAttempts = 1
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	decisions, adviceCalls, closeAdvice := recommendationDecisions(t, db, "home_review_comparison")
	defer closeAdvice()
	runtime := &offerRuntimeFixture{db: db}
	engine := &Engine{Store: db, Runtime: runtime, Tradeoffs: jevservice.Service{Store: db, Client: client}, Decisions: decisions, Context: ctx}
	if err := engine.LaunchRound(ctx, round); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err := db.Round(ctx, round.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == store.RoundCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != store.RoundCompleted || current.DeliverableStatus != "complete" || current.Used.Requests != 3 || calls != 1 || adviceCalls.Load() != 1 || runtime.turns != 1 {
		t.Fatalf("commission: round=%+v tradeoff=%d advice=%d turns=%d", current, calls, adviceCalls.Load(), runtime.turns)
	}
	if !strings.Contains(runtime.evidence, text) || !strings.Contains(runtime.evidence, "I value predictable hours") {
		t.Fatal("complete owner context omitted from turn")
	}
	saved, err := db.OfferComparisonByRoundForOwner(ctx, owner, round.ID)
	if err != nil || !saved.Current || saved.TradeoffStatus != "selected" || saved.Tradeoff.Alternative.ID != "clarify-hours" {
		t.Fatalf("saved tradeoff: %+v %v", saved, err)
	}
	var outcome offerReport
	if json.Unmarshal(current.Report, &outcome) != nil || outcome.ComparisonID != saved.ID || outcome.Recommendation == nil ||
		outcome.Recommendation.Status != "selected" || outcome.Recommendation.Action != "review_comparison" {
		t.Fatalf("saved comparison advice: %s", current.Report)
	}
	attempts, err := db.JevAttemptsForRound(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.RoundAttemptID == outcome.Recommendation.DecisionAttemptID &&
			(strings.Contains(string(attempt.LogicalRequestJSON), text) || strings.Contains(string(attempt.LogicalRequestJSON), "I value predictable hours")) {
			t.Fatal("raw offer or owner priorities entered bounded advice request")
		}
	}
	if verdict := ReadHomeRecommendationCurrentness(ctx, db, current); verdict.Status != "current" {
		t.Fatalf("saved comparison advice changed on read: %+v", verdict)
	}
	if _, err := db.OfferComparisonByRoundForOwner(ctx, store.Actor{Kind: "administrator", ID: "other"}, round.ID); err != store.ErrNotFound {
		t.Fatalf("cross-owner read: %v", err)
	}
}

func TestOfferComparisonRemainsReadableWhenJevUnavailable(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	intake, _, err := db.CreateOfferIntake(ctx, owner, "offline-jev", []string{"Fixture Labs offer. Clarify weekly hours before deciding."}, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "offer_intake:" + intake.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "offline-jev", Intent: "Compare offers", Outcome: "compare_offers", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Minute), Scope: store.RoundScope{Resources: []string{resource}, Operations: []string{store.RoundCodexTurn, store.RoundPrepareOfferComparison, store.RoundJevRequest}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 4, Items: 1, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &offerRuntimeFixture{db: db}
	engine := &Engine{Store: db, Runtime: runtime, Context: ctx}
	if err := engine.LaunchRound(ctx, round); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := db.Round(ctx, round.ID)
		if current.State == store.RoundCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.OfferComparisonByRoundForOwner(ctx, owner, round.ID)
	if err != nil || current.State != store.RoundCompleted || current.DeliverableStatus != "partial" || current.Used.Requests != 1 || runtime.turns != 1 || !saved.Current || saved.TradeoffStatus != "unavailable" {
		t.Fatalf("saved partial: round=%+v comparison=%+v err=%v", current, saved, err)
	}
}

func TestOfferTradeoffCapturedBeforeStopRecoversWithoutReplayOrCharge(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	intake, _, err := db.CreateOfferIntake(ctx, owner, "offer-recover", []string{"Fixture Labs offer. Clarify weekly hours before deciding."}, "")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "offer_intake:" + intake.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "offer-recover", Intent: "Compare offers", Outcome: "compare_offers", ProfileVersion: profile.Version, Deadline: time.Now().Add(time.Minute), Scope: store.RoundScope{InputRefs: []string{resource}, Resources: []string{resource}, Operations: []string{store.RoundCodexTurn, store.RoundPrepareOfferComparison, store.RoundJevRequest}, Delegates: []string{"codex-runner"}}, Limits: store.RoundAllowance{Requests: 4, Items: 1, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &offerRuntimeFixture{db: db}
	if _, err := runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, round.ID, codexservice.RoundTurnInput{RequestKey: "compare:" + intake.ID, ResourceID: resource, Brief: "Compare", Evidence: "complete"}); err != nil {
		t.Fatal(err)
	}
	comparison, err := db.OfferComparisonByRound(ctx, round.ID)
	if err != nil || !comparison.Current {
		t.Fatalf("comparison: %+v %v", comparison, err)
	}
	input, err := comparison.Comparison.TradeoffInput(store.OfferTradeoffMaxReportedTokens)
	if err != nil {
		t.Fatal(err)
	}
	stopErrors := make(chan error, 1)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _, stopErr := db.StopRound(ctx, owner, round.ID)
		if stopErr == nil {
			_, stopErr = db.PauseStoppedRound(ctx, owner, round.ID)
		}
		stopErrors <- stopErr
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"offer_tradeoff":{"type":"choice","choice":"clarify-hours","probabilities":{"clarify-hours":0.9,"__unresolved__":0.1},"confidence":0.8}},"usage":{"input_tokens":100,"output_tokens":10}}`))
	}))
	defer server.Close()
	t.Setenv(jev.CredentialEnvironmentVariable, "synthetic-offer-key")
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = server.URL + "/v1/systemone"
	cfg.Timeout = time.Second
	cfg.MaxAttempts = 1
	client, err := jev.NewFromEnvironment(cfg, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	tradeoffs := jevservice.Service{Store: db, Client: client}
	_, err = tradeoffs.RunOfferTradeoff(ctx, jevservice.Binding{Actor: store.Actor{Kind: "agent", ID: "codex-runner"}, RoundID: round.ID, ResourceID: resource, RequestKeyPrefix: "offer-tradeoff:" + comparison.ID, ProfileVersion: profile.Version, MaxReportedTokens: store.OfferTradeoffMaxReportedTokens}, input)
	if err == nil {
		t.Fatal("stopped attempt unexpectedly finished current round")
	}
	if stopErr := <-stopErrors; stopErr != nil {
		t.Fatal(stopErr)
	}
	paused, err := db.Round(ctx, round.ID)
	if err != nil || paused.State != store.RoundPaused || paused.Used.Requests != 2 {
		t.Fatalf("paused: %+v %v", paused, err)
	}
	engine := &Engine{Store: db, Runtime: runtime, Tradeoffs: tradeoffs, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	if _, err := service.Resume(ctx, owner, round.ID); err != nil {
		t.Fatalf("local resume: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := db.Round(ctx, round.ID)
		if current.State == store.RoundCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, err := db.Round(ctx, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := db.OfferComparisonByRound(ctx, round.ID)
	if err != nil || current.State != store.RoundCompleted || current.Used.Requests != 2 || calls != 1 || runtime.turns != 1 || saved.TradeoffStatus != "selected" {
		t.Fatalf("recovered: round=%+v comparison=%+v calls=%d turns=%d err=%v", current, saved, calls, runtime.turns, err)
	}
}

func TestIncompleteOfferTradeoffCaptureStaysPausedWithoutReconcileCharge(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	intake, _, err := db.CreateOfferIntake(ctx, owner, "missing-capture", []string{"Fixture Labs offer."}, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "offer_intake:" + intake.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "missing-capture", Intent: "Compare offers", Outcome: "compare_offers", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Minute), Scope: store.RoundScope{Resources: []string{resource}, Operations: []string{store.RoundJevRequest}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 3, Tools: 2}})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "offer-tradeoff:lost/0", Operation: store.RoundJevRequest, ResourceID: resource, Cost: store.RoundAllowance{Requests: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.StopRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PauseStoppedRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	runtime := &offerRuntimeFixture{db: db}
	engine := &Engine{Store: db, Runtime: runtime, Context: ctx}
	service := &rounds.Service{Store: db, Readiness: engine, Worker: engine}
	if _, err := service.Resume(ctx, owner, round.ID); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("incomplete capture resumed: %v", err)
	}
	paused, err := db.Round(ctx, round.ID)
	if err != nil || paused.State != store.RoundPaused || paused.Used.Requests != 1 || paused.Used.Tools != 0 {
		t.Fatalf("charged/released incomplete capture: %+v %v", paused, err)
	}
}
