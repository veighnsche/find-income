package codexservice

import (
	"context"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
	"github.com/veighnsche/find-income-dashboard/api/internal/offercomparison"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestCommissionedAdviceScopeReachesActualRoundTools(t *testing.T) {
	for _, outcome := range []string{"prepare", "compare_offers", "interview_prepare", "interview_debrief", "process_replies"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			svc, db := testService(t, testConfig())
			owner := store.Actor{Kind: "administrator", ID: "owner"}
			agent := store.Actor{Kind: "agent", ID: "codex-runner"}
			profile, err := db.CurrentPreferences(ctx)
			if err != nil {
				t.Fatal(err)
			}
			resource := "opportunity:fixture"
			var intake store.OfferIntake
			if outcome == "compare_offers" {
				intake, _, err = db.CreateOfferIntake(ctx, owner, "tool-scope-offer", []string{"Fixture Labs makes an offer. Clarify weekly hours."}, "")
				if err != nil {
					t.Fatal(err)
				}
				resource = "offer_intake:" + intake.ID
			} else if outcome == "interview_debrief" {
				resource = "interview:fixture"
			} else if outcome == "process_replies" {
				resource = "thread:fixture"
			}
			round, _, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: "tool-scope-" + outcome, Intent: "Use the commissioned tool scope", Outcome: outcome, ProfileVersion: profile.Version,
				Scope:  store.RoundScope{InputRefs: []string{resource}, Resources: []string{resource, "campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool, store.RoundPrepareOfferComparison, store.RoundJevRequest}, Delegates: []string{agent.ID}},
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
			if outcome == "compare_offers" {
				source := intake.Sources[0]
				result, err := svc.offerComparisonPrepareTool(ctx, offerComparisonPrepareArgs{RoundID: round.ID, Capability: capability, RequestKey: "save-offer", IntakeID: intake.ID,
					Offers:       []offercomparison.Offer{{ID: source.OfferID, Employer: "Fixture Labs", EmployerCitation: &offercomparison.Citation{SourceID: source.ID, Excerpt: "Fixture Labs"}, Engagement: "unknown", Pay: offercomparison.PayTerm{AmountKind: "unknown", Period: fit.UnknownPeriod, Basis: fit.UnknownBasis}, Holiday: offercomparison.HolidayTerm{Treatment: "unknown"}}},
					Alternatives: []offercomparison.Alternative{{ID: "clarify-hours", Kind: "clarify", Why: offercomparison.CitedText{Text: "Clarify weekly hours", Citations: []offercomparison.Citation{{SourceID: source.ID, Excerpt: "Clarify weekly hours"}}}}}})
				if err != nil || result["created"] != true {
					t.Fatalf("two-resource offer_comparison_prepare rejected: %+v %v", result, err)
				}
			}
		})
	}
}
