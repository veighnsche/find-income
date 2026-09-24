package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRoundToolCapabilityFencesOldTurnAfterStopResume(t *testing.T) {
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
	r, _, err := s.StartRound(ctx, owner, StartRoundInput{RequestKey: "bound", Intent: "Find work", Outcome: "process_input",
		ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope: RoundScope{Resources: []string{"campaign:active"}, Operations: []string{RoundCodexTurn, RoundCreateCompany},
			Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 5, Items: 2, Tools: 5, Turns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	newTurn := func(key string) RoundAttempt {
		turn, _, err := s.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{
			RequestKey: key, Operation: RoundCodexTurn, ResourceID: "campaign:active", Cost: RoundAllowance{Tools: 1, Turns: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
			t.Fatal(err)
		}
		return turn
	}
	oldTurn := newTurn("old-turn")
	oldCap, err := s.IssueRoundToolCapability(ctx, r.ID, oldTurn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyRoundToolCapability(ctx, oldCap, "other-round"); !errors.Is(err, ErrFenced) {
		t.Fatalf("cross-round capability accepted: %v", err)
	}
	stopping, pending, err := s.StopRound(ctx, owner, r.ID)
	if err != nil || stopping.State != RoundStopping || len(pending) != 1 {
		t.Fatalf("stop: %+v %v", pending, err)
	}
	if _, err := s.VerifyRoundToolCapability(ctx, oldCap, r.ID); !errors.Is(err, ErrFenced) {
		t.Fatalf("old capability survived Stop: %v", err)
	}
	paused, err := s.PauseStoppedRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	check, err := s.BeginRoundReconciliation(ctx, r.ID, oldTurn.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRoundReconciliation(ctx, check, AttemptObservedFailure, json.RawMessage(`{"status":"confirmed_failed"}`)); err != nil {
		t.Fatal(err)
	}
	r, err = s.ResumeRound(ctx, owner, r.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	nextTurn := newTurn("new-turn")
	newCap, err := s.IssueRoundToolCapability(ctx, r.ID, nextTurn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := RoundMutationInput{RequestKey: "company-after-resume", Operation: RoundCreateCompany,
		ResourceID: "campaign:active", ExpectedRevision: p.Version, Company: &CompanyInput{Name: "Synthetic"}, Capability: oldCap}
	if _, _, err := s.ApplyRoundMutation(ctx, agent, r.ID, input); !errors.Is(err, ErrFenced) {
		t.Fatalf("old turn wrote after Resume: %v", err)
	}
	input.Capability = newCap
	if _, created, err := s.ApplyRoundMutation(ctx, agent, r.ID, input); err != nil || !created {
		t.Fatalf("new bound turn failed: %v %v", created, err)
	}
}
