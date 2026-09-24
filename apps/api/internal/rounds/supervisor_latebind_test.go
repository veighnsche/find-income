package rounds

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Late binding for the T23 lazily built service deps: a supervisor built
// without turn deps binds them once via the Set* setters; construction-time
// deps always win over a late call.
func TestSupervisorLateBinding(t *testing.T) {
	h := newSupervisorHarness(t, SupervisorConfig{})
	ctx := context.Background()
	sup, err := NewSupervisor(SupervisorDeps{
		DB: h.db, Authority: h.auth, Journal: h.journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)

	if _, err := sup.ContinueRun(ctx, testAgent, TurnInput{RunID: "run", RequestKey: "k"}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ContinueRun unwired = %v, want ErrNotReady", err)
	}
	if _, err := sup.ObserveDispatch(ctx, "attempt"); err == nil {
		t.Fatal("ObserveDispatch unwired succeeded, want no-observer error")
	}
	if sup.control.Reconciler != nil {
		t.Fatal("control reconciler wired without an observer")
	}

	if err := sup.SetTurnRunner(nil); err == nil {
		t.Fatal("SetTurnRunner(nil) succeeded, want error")
	}
	if err := sup.SetConversation(nil); err == nil {
		t.Fatal("SetConversation(nil) succeeded, want error")
	}
	if err := sup.SetObserver(nil); err == nil {
		t.Fatal("SetObserver(nil) succeeded, want error")
	}

	turns, conv, obs := &fakeTurns{}, &fakeConversation{}, &fakeObserver{}
	if err := sup.SetTurnRunner(turns); err != nil {
		t.Fatalf("SetTurnRunner: %v", err)
	}
	if err := sup.SetConversation(conv); err != nil {
		t.Fatalf("SetConversation: %v", err)
	}
	if err := sup.SetObserver(obs); err != nil {
		t.Fatalf("SetObserver: %v", err)
	}
	if sup.turnRunner() != TurnRunner(turns) || sup.conversation() != ConversationControl(conv) || sup.dispatchObserver() != Reconciler(obs) {
		t.Fatal("late-bound deps not visible through the guarded readers")
	}
	if sup.control.Reconciler == nil {
		t.Fatal("SetObserver did not enable control reconciliation")
	}

	// Single-assignment: every second bind fails and keeps the first.
	if err := sup.SetTurnRunner(&fakeTurns{}); err == nil {
		t.Fatal("second SetTurnRunner succeeded, want error")
	}
	if err := sup.SetConversation(&fakeConversation{}); err == nil {
		t.Fatal("second SetConversation succeeded, want error")
	}
	if err := sup.SetObserver(&fakeObserver{}); err == nil {
		t.Fatal("second SetObserver succeeded, want error")
	}
	if sup.turnRunner() != TurnRunner(turns) {
		t.Fatal("second SetTurnRunner replaced the first binding")
	}

	// Behavioral proof: with the runner bound, ContinueRun passes the
	// readiness gate and fails on the invalid agent instead.
	_, err = sup.ContinueRun(ctx, store.Actor{}, TurnInput{RunID: "run", RequestKey: "k"})
	var contractErr *researchcontract.Error
	if !errors.As(err, &contractErr) || contractErr.Code != researchcontract.OutcomeInvalid {
		t.Fatalf("ContinueRun bound = %v, want invalid-agent contract error", err)
	}
}

// Construction-time deps take precedence: setters over them fail.
func TestSupervisorLateBindingConstructionWins(t *testing.T) {
	h := newSupervisorHarness(t, SupervisorConfig{})
	if err := h.sup.SetTurnRunner(&fakeTurns{}); err == nil {
		t.Fatal("SetTurnRunner over construction dep succeeded, want error")
	}
	if err := h.sup.SetConversation(&fakeConversation{}); err == nil {
		t.Fatal("SetConversation over construction dep succeeded, want error")
	}
	if err := h.sup.SetObserver(&fakeObserver{}); err == nil {
		t.Fatal("SetObserver over construction dep succeeded, want error")
	}
	if h.sup.turnRunner() != TurnRunner(h.turns) {
		t.Fatal("late bind replaced the construction-time turn runner")
	}
}

// Under contention exactly one late bind wins.
func TestSupervisorLateBindingSingleWinner(t *testing.T) {
	h := newSupervisorHarness(t, SupervisorConfig{})
	sup, err := NewSupervisor(SupervisorDeps{
		DB: h.db, Authority: h.auth, Journal: h.journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)

	const racers = 8
	var wg sync.WaitGroup
	wins := make(chan bool, racers)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wins <- sup.SetTurnRunner(&fakeTurns{}) == nil
		}()
	}
	wg.Wait()
	close(wins)
	won := 0
	for w := range wins {
		if w {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d racers won the late bind, want exactly 1", won)
	}
}
