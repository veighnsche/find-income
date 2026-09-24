package codexservice

import (
	"context"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/offercomparison"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type offerComparisonPrepareArgs struct {
	RoundID      string                        `json:"roundId"`
	Capability   string                        `json:"capability"`
	RequestKey   string                        `json:"requestKey"`
	IntakeID     string                        `json:"intakeId"`
	Offers       []offercomparison.Offer       `json:"offers"`
	Alternatives []offercomparison.Alternative `json:"alternatives"`
}

func (s *Service) offerComparisonPrepareTool(ctx context.Context, args offerComparisonPrepareArgs) (map[string]any, error) {
	if args.RoundID == "" || args.Capability == "" || args.RequestKey == "" || len(args.RequestKey) > 100 || args.IntakeID == "" || len(args.Alternatives) < 1 {
		return nil, store.ErrInvalid
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return nil, err
	}
	round, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return nil, err
	}
	resource := "offer_intake:" + args.IntakeID
	if round.State != store.RoundRunning || round.Outcome != "compare_offers" || round.Generation != authority.Generation || len(round.Scope.Resources) != 2 || round.Scope.Resources[0] != resource || round.Scope.Resources[1] != "campaign:active" || !scopeContains(round.Scope.Operations, store.RoundPrepareOfferComparison) || !time.Now().Before(round.Deadline) {
		return nil, store.ErrFenced
	}
	intake, err := s.db.OfferIntake(ctx, args.IntakeID)
	if err != nil {
		return nil, err
	}
	for _, offer := range args.Offers {
		if offer.Employer == "Unknown employer" {
			continue
		}
		found := false
		for _, source := range intake.Sources {
			if source.OfferID == offer.ID && strings.Contains(source.Body, offer.Employer) {
				found = true
				break
			}
		}
		if !found {
			return nil, store.ErrInvalid
		}
	}
	comparison, err := offercomparison.Prepare(offercomparison.Input{Sources: intake.Sources, Offers: args.Offers, Alternatives: args.Alternatives})
	if err != nil {
		return nil, err
	}
	result, created, err := s.db.ApplyRoundMutation(ctx, authority.Actor, args.RoundID, store.RoundMutationInput{RequestKey: args.RequestKey, Operation: store.RoundPrepareOfferComparison, ResourceID: resource, ExpectedRevision: 1, Capability: args.Capability, OfferComparison: &store.OfferComparisonMutationInput{IntakeID: args.IntakeID, Comparison: comparison}})
	if err != nil {
		return nil, err
	}
	return map[string]any{"comparisonId": result.EntityID, "inputSha256": comparison.InputSHA256, "created": created, "missing": comparison.Missing, "pay": comparison.Pay}, nil
}
