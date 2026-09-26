package codexservice

import (
	"context"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestCommissionedAdviceScopeReachesActualRoundTools(t *testing.T) {
	ctx := context.Background()
	svc, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "opportunity:fixture"
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "tool-scope-prepare", Intent: "Use the commissioned tool scope", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{InputRefs: []string{resource}, Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundJevRequest}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Requests: 5, Items: 1, Tools: 5, Turns: 1}, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	round, err = db.ActivateRound(ctx, owner, round.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, round.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	capability, err := db.IssueRoundToolCapability(ctx, round.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.roundContextTool(ctx, roundContextArgs{RoundID: round.ID, Capability: capability, RequestKey: "context"}); err != nil {
		t.Fatalf("two-resource round_context rejected: %v", err)
	}
}
