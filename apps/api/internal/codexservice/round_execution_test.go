package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func roundTurnFixture(t *testing.T, db *store.Store) (store.Round, store.Actor, store.Actor) {
	return roundTurnFixtureWithDeadline(t, db, time.Now().Add(time.Hour))
}
func roundTurnFixtureWithDeadline(t *testing.T, db *store.Store, deadline time.Time) (store.Round, store.Actor, store.Actor) {
	t.Helper()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "commission", Intent: "Process supplied input", Outcome: "process_input", ProfileVersion: p.Version, Deadline: deadline, Scope: store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 4, Tools: 8, Turns: 3}})
	if err != nil || !created {
		t.Fatalf("start: %v", err)
	}
	r, err = db.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r, owner, agent
}
func turnInput() RoundTurnInput {
	return RoundTurnInput{RequestKey: "turn-one", ResourceID: "campaign:active", Brief: "Review supplied input", Evidence: "Synthetic owner input"}
}

func TestRoundTurnTerminalIsCorrelatedAndNeverRepeated(t *testing.T) {
	s, db := testService(t, testConfig())
	f := installRuntime(t, s)
	f.completeStatus = "completed"
	f.historyStatus = "completed"
	r, _, agent := roundTurnFixture(t, db)
	ctx := boundedContext(t)
	attempt, err := s.ExecuteRoundTurn(ctx, agent, r.ID, turnInput())
	if err != nil || attempt.State != store.AttemptSucceeded {
		t.Fatalf("turn: %+v %v", attempt, err)
	}
	remote, err := db.RoundRemoteDispatch(ctx, r.ID, attempt.ID)
	if err != nil || remote.ThreadID != "thread-a" || remote.TurnID != "turn-a" || remote.ObservedStatus != "completed" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	var launched struct {
		RoundID    string `json:"roundId"`
		AttemptID  string `json:"attemptId"`
		Capability string `json:"capability"`
	}
	if json.Unmarshal([]byte(f.text()), &launched) != nil || launched.RoundID != r.ID || launched.AttemptID != attempt.ID || launched.Capability == "" {
		t.Fatalf("turn was not scoped to its persisted attempt: %+v", launched)
	}
	if _, err := db.VerifyRoundToolCapability(ctx, launched.Capability, r.ID); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("completed turn retained tool authority: %v", err)
	}
	again, err := s.ExecuteRoundTurn(ctx, agent, r.ID, turnInput())
	if err != nil || again.ID != attempt.ID || f.count("thread/start") != 1 || f.count("turn/start") != 1 {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if current, _ := db.Round(ctx, r.ID); current.Used.Turns != 1 {
		t.Fatalf("turn charged more than once: %+v", current.Used)
	}
}

func TestRoundTurnStopFencesLateWorkAndHistoryReconciles(t *testing.T) {
	s, db := testService(t, testConfig())
	f := installRuntime(t, s)
	f.historyStatus = "completed"
	r, owner, agent := roundTurnFixture(t, db)
	ctx := boundedContext(t)
	done := make(chan error, 1)
	go func() { _, err := s.ExecuteRoundTurn(ctx, agent, r.ID, turnInput()); done <- err }()
	select {
	case <-f.started:
	case <-time.After(3 * time.Second):
		t.Fatal("turn not started")
	}
	// Wait for the returned IDs to be durably bound before stopping.
	var attempt store.RoundAttempt
	bound := false
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); {
		a, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "turn-one", Operation: store.RoundCodexTurn, ResourceID: "campaign:active", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
		if err == nil {
			attempt = a
			remote, e := db.RoundRemoteDispatch(ctx, r.ID, a.ID)
			bound = e == nil && remote.TurnID != ""
			if bound {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !bound {
		t.Fatal("turn IDs not bound")
	}
	controller := &rounds.Service{Store: db, Canceller: s}
	paused, err := controller.Stop(ctx, owner, r.ID)
	if err != nil || paused.State != store.RoundPaused {
		t.Fatalf("stop: %+v %v", paused, err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not settle")
	}
	if _, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "turn-two", Operation: store.RoundCodexTurn, ResourceID: "campaign:active", Cost: store.RoundAllowance{Tools: 1, Turns: 1}}); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stopped round accepted work: %v", err)
	}
	observation, err := s.ObserveDispatch(ctx, attempt.ID)
	if err != nil || observation.State != store.AttemptObservedSuccess {
		t.Fatalf("history: %+v %v", observation, err)
	}
	var evidence map[string]string
	if json.Unmarshal(observation.Evidence, &evidence) != nil || evidence["turnId"] != "turn-a" {
		t.Fatal("uncorrelated evidence")
	}
}

func TestRoundTurnMalformedStartPausesUncertainWithoutRetry(t *testing.T) {
	s, db := testService(t, testConfig())
	f := installRuntime(t, s)
	f.badThreadReply = true
	r, _, agent := roundTurnFixture(t, db)
	ctx := boundedContext(t)
	_, err := s.ExecuteRoundTurn(ctx, agent, r.ID, turnInput())
	if !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("expected uncertain: %v", err)
	}
	round, err := db.Round(ctx, r.ID)
	if err != nil || round.State != store.RoundPaused || !round.ReconciliationRequired {
		t.Fatalf("round: %+v %v", round, err)
	}
	if f.count("turn/start") != 0 {
		t.Fatal("turn started without persisted thread ID")
	}
	_, err = s.ExecuteRoundTurn(ctx, agent, r.ID, turnInput())
	if err == nil || f.count("thread/start") != 1 {
		t.Fatalf("uncertain dispatch repeated: %v", err)
	}
}
func TestRoundTurnDeadlineFencesRemoteIntentAndReleasesSlot(t *testing.T) {
	s, db := testService(t, testConfig())
	f := installRuntime(t, s)
	r, _, agent := roundTurnFixtureWithDeadline(t, db, time.Now().Add(800*time.Millisecond))
	ctx := boundedContext(t)
	_, err := s.ExecuteRoundTurn(ctx, agent, r.ID, turnInput())
	if !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("expected uncertain deadline, got %v", err)
	}
	if f.count("turn/start") != 1 {
		t.Fatalf("turn count %d", f.count("turn/start"))
	}
	current, e := db.Round(ctx, r.ID)
	if e != nil || current.State != store.RoundFailed || current.StopReason != "deadline_reached" || !current.ReconciliationRequired {
		t.Fatalf("deadline did not fence: %+v %v", current, e)
	}
}

// T24 F1: the production observer resolves every research.* attempt as an
// observed failure with fenced evidence (mirroring the source.search
// precedent), while genuinely unsupported operations stay uncertain.
func TestObserveDispatchResolvesResearchAttempts(t *testing.T) {
	s, db := testService(t, testConfig())
	ctx := boundedContext(t)
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "observe-research",
		Intent: "Observe research attempts", Outcome: "process_input", ProfileVersion: p.Version,
		Deadline: time.Now().Add(time.Hour),
		Scope: store.RoundScope{Resources: []string{"research", "evidence:e1"}, Operations: []string{
			store.RoundResearchSearch, store.RoundResearchFetch, store.RoundResearchBrowse,
			store.RoundResearchAPI, store.RoundResearchExec, store.RoundCorrectEvidence,
		}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 8, Items: 8, Tools: 8, Turns: 1}})
	if err != nil || !created {
		t.Fatalf("start: %v", err)
	}
	if r, err = db.ActivateRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	for _, op := range store.ResearchOperations() {
		cost, ok := store.RoundOperationCost(op)
		if !ok {
			t.Fatalf("no cost for %s", op)
		}
		attempt, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{
			RequestKey: "observe-" + op, Operation: op,
			ResourceID: store.ResearchAuthorityResource, Cost: cost})
		if err != nil {
			t.Fatal(err)
		}
		verdict, err := s.ObserveDispatch(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if verdict.State != store.AttemptObservedFailure ||
			!strings.Contains(string(verdict.Evidence), "research_attempt_fenced") {
			t.Fatalf("verdict on %s: %+v", op, verdict)
		}
	}
	// The fallback is preserved for operations no observer understands.
	cost, _ := store.RoundOperationCost(store.RoundCorrectEvidence)
	other, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{
		RequestKey: "observe-other", Operation: store.RoundCorrectEvidence,
		ResourceID: "evidence:e1", Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := s.ObserveDispatch(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.State != store.AttemptUncertain ||
		!strings.Contains(string(verdict.Evidence), "unsupported_attempt") {
		t.Fatalf("fallback verdict: %+v", verdict)
	}
}
