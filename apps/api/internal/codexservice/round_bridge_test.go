package codexservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestRoundContextUsesPrepareOpportunityAuthority(t *testing.T) {
	ctx := context.Background()
	svc, db := testService(t, testConfig())
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	company, _, err := db.CreateCompany(ctx, owner, store.CompanyInput{Name: "Employer"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := db.CreateOpportunity(ctx, owner, store.OpportunityInput{CompanyID: company.ID, Title: "Engineer", Kind: "employment", SourceURL: "https://example.invalid/job", OriginalText: "Build a service.", Stage: "new", WorkPattern: "remote", LocationText: "Brussels"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SetOwnerOpportunityDecision(ctx, owner, opportunity.ID, store.OwnerDecisionInput{RequestKey: "select", ExpectedOpportunityRevision: opportunity.Revision, ExpectedDecisionRevision: 0, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}
	profile, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "opportunity:" + opportunity.ID
	round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "prepare-context", Intent: "Prepare selected role", Outcome: "prepare", ProfileVersion: profile.Version,
		Scope:  store.RoundScope{Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool}, Delegates: []string{agent.ID}},
		Limits: store.RoundAllowance{Tools: 3, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActivateRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "prepare-turn", Operation: store.RoundCodexTurn, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
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
	session, _ := sdkSession(t, svc)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "round_context", Arguments: map[string]any{"roundId": round.ID, "capability": capability, "requestKey": "prepare-read"}})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("prepare context: %+v %v", result, err)
	}
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok || payload["outcome"] != "prepare" {
		t.Fatalf("prepare context: %+v %v", result, err)
	}
	current, err := db.Round(ctx, round.ID)
	if err != nil || current.Used.Tools != 2 {
		t.Fatalf("context charge: %+v %v", current.Used, err)
	}
	attempt, created, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "prepare-read", Operation: store.RoundContextTool, ResourceID: resource, Cost: store.RoundAllowance{Tools: 1}, BoundCapability: capability})
	if err != nil || created || attempt.ResourceID != resource || attempt.State != store.AttemptSucceeded {
		t.Fatalf("context authority: %+v created=%v %v", attempt, created, err)
	}
	if _, _, err := db.StopRound(ctx, owner, round.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.roundContextTool(ctx, roundContextArgs{RoundID: round.ID, Capability: capability, RequestKey: "stale-read"}); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stale prepare capability: %v", err)
	}
}

func TestRoundContextRejectsWrongOutcomeScope(t *testing.T) {
	for _, outcome := range []string{"prepare", "discover"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			svc, db := testService(t, testConfig())
			owner := store.Actor{Kind: "administrator", ID: "owner"}
			agent := store.Actor{Kind: "agent", ID: "codex-runner"}
			profile, err := db.CurrentPreferences(ctx)
			if err != nil {
				t.Fatal(err)
			}
			round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "wrong-context", Intent: "Inspect scope", Outcome: outcome, ProfileVersion: profile.Version,
				Scope:  store.RoundScope{Resources: []string{"board:wrong"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool}, Delegates: []string{agent.ID}},
				Limits: store.RoundAllowance{Tools: 3, Turns: 1}, Deadline: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ActivateRound(ctx, owner, round.ID); err != nil {
				t.Fatal(err)
			}
			turn, _, err := db.ReserveRoundAttempt(ctx, agent, round.ID, store.RoundAttemptInput{RequestKey: "turn", Operation: store.RoundCodexTurn, ResourceID: "board:wrong", Cost: store.RoundAllowance{Tools: 1, Turns: 1}})
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
			if _, err := svc.roundContextTool(ctx, roundContextArgs{RoundID: round.ID, Capability: capability, RequestKey: "wrong-read"}); !errors.Is(err, store.ErrFenced) {
				t.Fatalf("wrong context resource accepted: %v", err)
			}
			current, err := db.Round(ctx, round.ID)
			if err != nil || current.Used.Tools != 1 {
				t.Fatalf("wrong context charged: %+v %v", current.Used, err)
			}
		})
	}
}
