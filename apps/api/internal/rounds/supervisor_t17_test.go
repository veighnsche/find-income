package rounds

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// T17: the supervisor issues the one-turn tool capability after dispatch
// and hands it to the runner; the runner's tool calls authenticate against
// it while the attempt is live, and it is invalid once the turn settles.
func TestSupervisorIssuesTurnCapabilityToRunner(t *testing.T) {
	h := newSupervisorHarness(t, SupervisorConfig{})
	ctx := context.Background()
	out := h.commission(t, "Capability-bound turn.", "run-cap")
	var got string
	var verified store.RoundToolAuthority
	h.turns.run = func(ctx context.Context, agent store.Actor, in RunnerTurnInput) (RunnerTurnOutput, error) {
		got = in.Capability
		var err error
		verified, err = h.db.VerifyRoundToolCapability(ctx, in.Capability, in.RunID)
		if err != nil {
			return RunnerTurnOutput{}, err
		}
		return RunnerTurnOutput{ThreadID: "thread-cap", TurnID: "turn-cap", Status: "completed"}, nil
	}
	turn, err := h.sup.ContinueRun(ctx, testAgent, TurnInput{RunID: out.RunID, RequestKey: "turn-cap"})
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("runner received no turn capability")
	}
	if verified.AttemptID != turn.AttemptID || verified.RoundID != out.RunID ||
		verified.Actor.ID != testAgent.ID || verified.Generation != out.Generation {
		t.Fatalf("capability binds the live attempt: %+v", verified)
	}
	// After settle the capability is invalid: revoked explicitly and fenced
	// by the finished attempt, either way unusable for further tool calls.
	if _, err := h.db.VerifyRoundToolCapability(ctx, got, out.RunID); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("settled capability still verifies: %v", err)
	}
}

// T17: the commission record journals the rubric source the caller
// resolved, so later audits can tell which brief store bound the run.
func TestSupervisorRecordsRubricSource(t *testing.T) {
	h := newSupervisorHarness(t, SupervisorConfig{})
	ctx := context.Background()
	out, err := h.sup.Commission(ctx, CommissionInput{Actor: testOwner,
		BriefText: "Sourced run.", AgentID: testAgent.ID, RubricVersion: "criteria-v9-test",
		RubricSource: "preferences_versions:current:v9", IdempotencyKey: "run-source"})
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := h.journal.List(ctx, out.RunID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.ID != "run."+out.RunID+".commissioned" {
			continue
		}
		var rec commissionRecord
		if err := json.Unmarshal(e.Payload, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.RubricSource != "preferences_versions:current:v9" || rec.RubricVersion != "criteria-v9-test" {
			t.Fatalf("commission record: %+v", rec)
		}
		return
	}
	t.Fatal("commission record not journaled")
}
