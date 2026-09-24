package researchservice

import (
	"context"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestLoopDrivesTurnsSequentially(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Looped run.", "run-loop", nil).View.RunId
	outcome, err := h.svc.RunLoop(ctx, testAgent, run, LoopOpts{MaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.StopReason != LoopTurnsExhausted || len(outcome.Turns) != 2 {
		t.Fatalf("loop: %+v", outcome)
	}
	for i, turn := range outcome.Turns {
		if turn.Index != i+1 || turn.ThreadID == "" || turn.TurnID == "" ||
			turn.Status != "completed" || turn.Replayed || len(turn.NextWork) != 1 {
			t.Fatalf("turn %d: %+v", i, turn)
		}
	}
	// A second loop over the same keys replays settled turns, never
	// duplicating them.
	replay, err := h.svc.RunLoop(ctx, testAgent, run, LoopOpts{MaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Turns) != 2 || !replay.Turns[0].Replayed || !replay.Turns[1].Replayed {
		t.Fatalf("replay: %+v", replay)
	}
	if replay.Turns[0].AttemptID != outcome.Turns[0].AttemptID {
		t.Fatal("replay addressed a different turn slot")
	}
}

func TestLoopStopsOnBudgetExhaustion(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Short run.", "run-short", &generated.ResearchAllowance{
		TimeMs: 60000, MaxActions: 5, MaxJev: 1, MaxTurns: 1, MaxConcurrent: 1}).View.RunId
	outcome, err := h.svc.RunLoop(ctx, testAgent, run, LoopOpts{MaxTurns: 5})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.StopReason != LoopBudgetExhausted || len(outcome.Turns) != 1 {
		t.Fatalf("budget stop: %+v", outcome)
	}
}

func TestLoopStopsOnInterruptedTurn(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Interrupted run.", "run-interrupt", nil).View.RunId
	h.turns.run = func(_ context.Context, _ store.Actor, in rounds.RunnerTurnInput) (rounds.RunnerTurnOutput, error) {
		return rounds.RunnerTurnOutput{ThreadID: "thread-i", TurnID: "turn-i", Status: "interrupted"}, nil
	}
	outcome, err := h.svc.RunLoop(ctx, testAgent, run, LoopOpts{MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.StopReason != LoopTurnInterrupted || len(outcome.Turns) != 1 {
		t.Fatalf("interrupt stop: %+v", outcome)
	}
}

func TestLoopNeverRetriesUncertain(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Uncertain run.", "run-uncertain", nil).View.RunId
	h.turns.run = func(context.Context, store.Actor, rounds.RunnerTurnInput) (rounds.RunnerTurnOutput, error) {
		return rounds.RunnerTurnOutput{}, errors.New("runtime transport lost")
	}
	outcome, err := h.svc.RunLoop(ctx, testAgent, run, LoopOpts{MaxTurns: 3})
	if err == nil {
		t.Fatal("uncertain turn returned no error")
	}
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) || cerr.Code != researchcontract.OutcomeUncertain {
		t.Fatalf("uncertain error: %v", err)
	}
	if outcome.RunID != run || len(outcome.Turns) != 0 {
		t.Fatalf("partial outcome: %+v", outcome)
	}
	// The failed key stays held-dispatched for stop/reconcile: reusing it
	// conflicts instead of blindly re-running.
	_, err = h.sup.ContinueRun(ctx, testAgent, rounds.TurnInput{RunID: run, RequestKey: "loop-turn-001"})
	if contractCode(t, err) != researchcontract.OutcomeConflict {
		t.Fatalf("held key: %v", err)
	}
}

func TestLoopCancellationAndValidation(t *testing.T) {
	h := newAdapterHarness(t)
	ctx := context.Background()
	run := h.commission(t, "Cancelled run.", "run-cancel", nil).View.RunId
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := h.svc.RunLoop(canceled, testAgent, run, LoopOpts{MaxTurns: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled loop: %v", err)
	}
	if _, err := h.svc.RunLoop(ctx, testOwner, run, LoopOpts{MaxTurns: 1}); contractCode(t, err) != researchcontract.OutcomeInvalid {
		t.Fatalf("owner actor: %v", err)
	}
	if _, err := h.svc.RunLoop(ctx, testAgent, "run-missing", LoopOpts{MaxTurns: 1}); contractCode(t, err) != researchcontract.OutcomeNotFound {
		t.Fatalf("missing run: %v", err)
	}
}
