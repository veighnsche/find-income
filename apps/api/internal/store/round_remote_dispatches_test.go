package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRoundRemoteDispatchRequiresCorrelatedTerminalObservation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-runner"}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "remote", Intent: "Find work", Outcome: "process_input",
		ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Resources: []string{"campaign:active"}, Operations: []string{RoundCodexTurn},
			Delegates: []string{agent.ID}}, Limits: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{
		RequestKey: "turn", Operation: RoundCodexTurn, ResourceID: "campaign:active", Cost: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundAttempt(ctx, agent, r.ID, attempt.ID, true, json.RawMessage(`{"done":true}`), ""); !errors.Is(err, ErrUncertain) {
		t.Fatalf("unobserved turn finished: %v", err)
	}
	if err := s.BindRoundThread(ctx, r.ID, attempt.ID, attempt.Generation, "thread-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindRoundThread(ctx, r.ID, attempt.ID, attempt.Generation, "thread-b"); !errors.Is(err, ErrFenced) {
		t.Fatalf("rebound thread: %v", err)
	}
	if err := s.BindRoundTurn(ctx, r.ID, attempt.ID, attempt.Generation, "thread-a", "turn-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveRoundTurn(ctx, r.ID, attempt.ID, attempt.Generation, "thread-a", "wrong", "completed", json.RawMessage(`{}`)); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong turn observed: %v", err)
	}
	if err := s.ObserveRoundTurn(ctx, r.ID, attempt.ID, attempt.Generation, "thread-a", "turn-a", "unknown", json.RawMessage(`{"status":"unknown"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundAttempt(ctx, agent, r.ID, attempt.ID, true, json.RawMessage(`{"done":true}`), ""); !errors.Is(err, ErrUncertain) {
		t.Fatalf("unknown turn finished: %v", err)
	}
	if err := s.ObserveRoundTurn(ctx, r.ID, attempt.ID, attempt.Generation, "thread-a", "turn-a", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundAttempt(ctx, agent, r.ID, attempt.ID, true, json.RawMessage(`{"done":true}`), ""); err != nil {
		t.Fatal(err)
	}
}

func TestRoundRemoteIdentifiersSurviveStopAsEvidenceOnly(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-runner"}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "late-remote", Intent: "Find work", Outcome: "process_input",
		ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Resources: []string{"campaign:active"}, Operations: []string{RoundCodexTurn},
			Delegates: []string{agent.ID}}, Limits: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{
		RequestKey: "turn", Operation: RoundCodexTurn, ResourceID: "campaign:active", Cost: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkRoundDispatched(ctx, r.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := s.MarkRoundDispatchUncertain(ctx, r.ID, attempt.ID, attempt.Generation, "turn_start_unknown")
	if err != nil || paused.State != RoundPaused || !paused.ReconciliationRequired {
		t.Fatalf("uncertain dispatch not fenced: %+v %v", paused, err)
	}
	if err := s.BindRoundThread(ctx, r.ID, attempt.ID, attempt.Generation, "late-thread"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindRoundTurn(ctx, r.ID, attempt.ID, attempt.Generation, "late-thread", "late-turn"); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveRoundTurn(ctx, r.ID, attempt.ID, attempt.Generation, "late-thread", "late-turn", "completed", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundAttempt(ctx, agent, r.ID, attempt.ID, true, json.RawMessage(`{"done":true}`), ""); !errors.Is(err, ErrFenced) {
		t.Fatalf("late remote result became current: %v", err)
	}
	d, err := s.RoundRemoteDispatch(ctx, r.ID, attempt.ID)
	if err != nil || d.ThreadID != "late-thread" || d.TurnID != "late-turn" || d.ObservedStatus != "completed" {
		t.Fatalf("late evidence lost: %+v %v", d, err)
	}
}
