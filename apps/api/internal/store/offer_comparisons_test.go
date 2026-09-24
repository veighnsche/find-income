package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
	"github.com/veighnsche/find-income-dashboard/api/internal/offercomparison"
)

func TestOfferIntakeComparisonIsOwnerBoundAndRoundFenced(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := Actor{Kind: "administrator", ID: "owner"}
	agent := Actor{Kind: "agent", ID: "codex-runner"}
	text := "Fixture Labs offers a role. Please clarify weekly hours before deciding."
	intake, created, err := db.CreateOfferIntake(ctx, owner, "compare-1", []string{text}, "")
	if err != nil || !created {
		t.Fatalf("intake: %v %v", created, err)
	}
	replay, created, err := db.CreateOfferIntake(ctx, owner, "compare-1", []string{text}, "")
	if err != nil || created || replay.ID != intake.ID {
		t.Fatalf("intake replay: %+v %v %v", replay, created, err)
	}
	if _, _, err := db.CreateOfferIntake(ctx, owner, "compare-1", []string{"changed"}, ""); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed text replay: %v", err)
	}
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource := "offer_intake:" + intake.ID
	r, _, err := db.StartRound(ctx, owner, StartRoundInput{RequestKey: "compare-1", Intent: "Compare complete offer", Outcome: "compare_offers", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour), Scope: RoundScope{InputRefs: []string{resource}, Resources: []string{resource, "campaign:active"}, Operations: []string{RoundCodexTurn, RoundPrepareOfferComparison, RoundJevRequest}, Delegates: []string{agent.ID}}, Limits: RoundAllowance{Requests: 4, Items: 1, Tools: 3, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	r, err = db.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, RoundAttemptInput{RequestKey: "turn", Operation: RoundCodexTurn, ResourceID: resource, Cost: RoundAllowance{Tools: 1, Turns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	cap, err := db.IssueRoundToolCapability(ctx, r.ID, turn.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := intake.Sources[0]
	input := offercomparison.Input{Sources: intake.Sources, Offers: []offercomparison.Offer{{ID: source.OfferID, Employer: "Fixture Labs", EmployerCitation: &offercomparison.Citation{SourceID: source.ID, Excerpt: "Fixture Labs"}, Engagement: "unknown", Pay: offercomparison.PayTerm{AmountKind: "unknown", Period: fit.UnknownPeriod, Basis: fit.UnknownBasis}, Holiday: offercomparison.HolidayTerm{Treatment: "unknown"}}}, Alternatives: []offercomparison.Alternative{{ID: "clarify-hours", Kind: "clarify", Why: offercomparison.CitedText{Text: "Clarify weekly hours", Citations: []offercomparison.Citation{{SourceID: source.ID, Excerpt: "clarify weekly hours"}}}}}}
	comparison, err := offercomparison.Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	mutation := RoundMutationInput{RequestKey: "save", Operation: RoundPrepareOfferComparison, ResourceID: resource, ExpectedRevision: 1, Capability: cap, OfferComparison: &OfferComparisonMutationInput{IntakeID: intake.ID, Comparison: comparison}}
	tampered := mutation
	tampered.RequestKey = "tampered"
	tamperedComparison := comparison
	tamperedComparison.Input.Sources = append([]offercomparison.Source(nil), comparison.Input.Sources...)
	tamperedComparison.Input.Sources[0].Body = "altered"
	tampered.OfferComparison = &OfferComparisonMutationInput{IntakeID: intake.ID, Comparison: tamperedComparison}
	if _, _, err := db.ApplyRoundMutation(ctx, agent, r.ID, tampered); !errors.Is(err, ErrFenced) {
		t.Fatalf("tampered source accepted: %v", err)
	}
	result, created, err := db.ApplyRoundMutation(ctx, agent, r.ID, mutation)
	if err != nil || !created {
		t.Fatalf("save: %+v %v %v", result, created, err)
	}
	saved, err := db.OfferComparisonForOwner(ctx, owner, result.EntityID)
	if err != nil || !saved.Current || saved.TradeoffStatus != "pending" || saved.Comparison.InputSHA256 != comparison.InputSHA256 {
		t.Fatalf("read: %+v %v", saved, err)
	}
	if _, err := db.OfferComparisonForOwner(ctx, Actor{Kind: "administrator", ID: "another-owner"}, result.EntityID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross owner read: %v", err)
	}
	if same, created, err := db.ApplyRoundMutation(ctx, agent, r.ID, mutation); err != nil || created || same.EntityID != result.EntityID {
		t.Fatalf("mutation replay: %+v %v %v", same, created, err)
	}
	if _, _, err := db.StopRound(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	tampered.RequestKey = "after-stop"
	if _, _, err := db.ApplyRoundMutation(ctx, agent, r.ID, tampered); !errors.Is(err, ErrFenced) {
		t.Fatalf("stopped write: %v", err)
	}
}
