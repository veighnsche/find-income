package rounds

import (
	"context"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// T23 authority scope: the jevassess pre-provider-call hold reserves through
// Authority like research dispatch. Unrelated reviewed ops still route to
// their owning service.
func TestAuthorityJevAssessReserveRoundTrip(t *testing.T) {
	ctx := context.Background()
	scope := testResearchScope()
	scope.Operations = append(scope.Operations, store.RoundJevAssess)
	db, r := openResearchRound(t, scope, store.RoundAllowance{Requests: 4, Tools: 4})
	auth := mustAuthority(t, db, testOwner)

	first, err := auth.Reserve(ctx, r.ID, store.RoundJevAssess, "jev-1", "payload")
	if err != nil {
		t.Fatal(err)
	}
	if first.AttemptID == "" || first.AttemptID != first.ID || first.Operation != store.RoundJevAssess {
		t.Fatalf("reservation shape: %+v", first)
	}
	replay, err := auth.Reserve(ctx, r.ID, store.RoundJevAssess, "jev-1", "payload")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("stable replay: %+v %v", replay, err)
	}
	if err := auth.Release(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	round, err := db.Round(ctx, r.ID)
	if err != nil || round.Used.Requests != 0 {
		t.Fatalf("released hold still charged: %+v %v", round.Used, err)
	}

	if cerr := checkErr(t, func() error {
		_, err := auth.Reserve(ctx, r.ID, store.RoundCreateCompany, "k", "p")
		return err
	}()); cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "operation" ||
		!strings.Contains(cerr.Detail, "reserves through the owning service") {
		t.Fatalf("unrelated op: %+v", cerr)
	}
}

// A supervisor-commissioned run scopes jev_assess, so the assessor's hold
// passes the store fence on the real T23 path.
func TestCommissionedRunScopesJevAssess(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "jev assess scope", "jev-scope-1")

	agentAuth, err := NewAuthority(h.db, testAgent)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agentAuth.Reserve(ctx, out.RunID, store.RoundJevAssess, "jev-k", "hash")
	if err != nil {
		t.Fatalf("agent jev_assess reserve on commissioned run: %v", err)
	}
	if res.Operation != store.RoundJevAssess || res.RunID != out.RunID {
		t.Fatalf("reservation shape: %+v", res)
	}
}
