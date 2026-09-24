package store

import (
	"context"
	"testing"
)

// T23 op fix: D's jevassess handler reserves "jev_assess" (its
// ReserveOperation const), which had no cost-table entry, so real
// Authority.Reserve rejected it as an unknown operation. It charges one
// request like the legacy jev.request op, which stays untouched.
func TestRoundJevAssessOperationCostAndReserve(t *testing.T) {
	cost, ok := RoundOperationCost(RoundJevAssess)
	if !ok || cost != (RoundAllowance{Requests: 1}) {
		t.Fatalf("jev_assess cost = %+v,%v, want {Requests:1},true", cost, ok)
	}
	if legacy, ok := RoundOperationCost(RoundJevRequest); !ok || legacy != (RoundAllowance{Requests: 1}) {
		t.Fatalf("legacy jev.request cost = %+v,%v, want unchanged {Requests:1},true", legacy, ok)
	}

	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input := roundInput(t, s, "jev-assess-round", RoundAllowance{Requests: 2, Tools: 1, Turns: 1})
	input.Scope.Operations = append(input.Scope.Operations, RoundJevAssess)
	r, created, err := s.StartRound(ctx, roundOwner(), input)
	if err != nil || !created {
		t.Fatalf("start: %+v %v", r, err)
	}
	if r, err = s.ActivateRound(ctx, roundOwner(), r.ID); err != nil {
		t.Fatal(err)
	}
	a, created, err := s.ReserveRoundAttempt(ctx, roundOwner(), r.ID, RoundAttemptInput{
		RequestKey: "jev-assess-1", Operation: RoundJevAssess,
		ResourceID: "source:example", Cost: cost})
	if err != nil || !created {
		t.Fatalf("reserve: %+v %v", a, err)
	}
}
