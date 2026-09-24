package runtimeaccept

import (
	"net/http"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Single-check paths refuse cleanly at exhaustion: the second reservation
// against a one-request run fails budget_exhausted with no attempt row and
// no charge, and a supervisor dispatch past exhaustion is refused at its
// own authority check the same way.
func TestAuthorityExhaustionRefusesCleanly(t *testing.T) {
	allow := &generated.ResearchAllowance{TimeMs: 15 * 60 * 1000,
		MaxActions: 1, MaxJev: 0, MaxTurns: 1, MaxConcurrent: 1}
	h := newHarness(t, allow, "clean exhaustion", "t24-exhaust-1", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/page", newCounter().handler("exhaustion page"))
	board := fixtureServer(t, mux)
	fp, err := fetchRequest(board.URL + "/page").Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Authority.Reserve(h.ctx, h.runID, store.RoundResearchFetch, "t24-exhaust-first", fp); err != nil {
		t.Fatal(err)
	}
	ledger := h.ledger(t)
	if ledger.Reserved.Requests != 1 {
		t.Fatalf("reserved after one hold: %+v", ledger.Reserved)
	}
	if _, err := h.stack.Authority.Reserve(h.ctx, h.runID, store.RoundResearchFetch, "t24-exhaust-second", fp); contractErr(t, err).Code != researchcontract.OutcomeBudgetExhausted {
		t.Fatalf("second reservation: %v", err)
	}
	if _, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-exhaust-second"); err == nil {
		t.Fatal("refused reservation left an attempt row")
	}
	if after := h.ledger(t); after != ledger {
		t.Fatalf("refused reservation moved the ledger: %+v -> %+v", ledger, after)
	}
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request:        fetchRequest(board.URL + "/page"),
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-exhaust-dispatch",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeBudgetExhausted {
		t.Fatalf("past-exhaustion dispatch: %+v", cerr)
	}
	if _, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-exhaust-dispatch"); err == nil {
		t.Fatal("refused dispatch left an attempt row")
	}
	if after := h.ledger(t); after != ledger {
		t.Fatalf("refused dispatch moved the ledger: %+v -> %+v", ledger, after)
	}
}

// T24 finding F2, FIXED (T24-fix rewrite of the _F2Repro repro; name kept).
// Release-on-pre-claim-refusal: the supervisor's reserve consumes the final
// allowance unit, the executor's own authority check refuses it pre-claim,
// and the supervisor releases its hold with a full refund instead of
// stranding the attempt dispatched/uncertain. The dispatch surfaces
// budget_exhausted with no charge and the key settles terminally (stale on
// redispatch, the failures precedent).
//
// Contract-owner scope note: the approved ruling fixes the burn and the
// strand, not the double-check conservatism. Every attempt at the final
// unit through supervisor Dispatch refuses cleanly — the unit stays
// unspendable there (provision pool >= planned research ops + 1);
// single-check paths (turns, Jev, saves) are unaffected.
func TestFinalAllowanceUnit_F2Repro(t *testing.T) {
	allow := &generated.ResearchAllowance{TimeMs: 15 * 60 * 1000,
		MaxActions: 2, MaxJev: 0, MaxTurns: 1, MaxConcurrent: 1}
	h := newHarness(t, allow, "F2 fixed", "t24-f2-1", nil)

	counts := newCounter()
	mux := http.NewServeMux()
	mux.HandleFunc("/one", counts.handler("first posting body"))
	mux.HandleFunc("/two", counts.handler("second posting body"))
	mux.HandleFunc("/three", counts.handler("third posting body"))
	board := fixtureServer(t, mux)

	first := h.dispatch(t, "t24-f2-first", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/one"))
	if first.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("first dispatch: %s", first.Outcome)
	}

	// The final unit refuses cleanly: budget_exhausted, not uncertain.
	_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request:        fetchRequest(board.URL + "/two"),
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-f2-last",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeBudgetExhausted {
		t.Fatalf("final-unit dispatch: %+v", cerr)
	}
	// The attempt unwound terminally (cancelled, refusal code) instead of
	// stranding dispatched; the refusal was pre-claim so the origin was
	// never contacted.
	refused, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-f2-last")
	if err != nil {
		t.Fatal(err)
	}
	if refused.State != store.AttemptCancelled || refused.ErrorCode != store.AbandonedPreClaimCode {
		t.Fatalf("refused attempt: state=%s code=%s", refused.State, refused.ErrorCode)
	}
	if len(refused.Result) != 0 {
		t.Fatal("refused attempt stored a result")
	}
	if n := counts.get("/two"); n != 0 {
		t.Fatalf("refused dispatch contacted the origin %d times", n)
	}
	// No charge held: Used stands at the first dispatch, the ledger shows
	// it observed with nothing reserved, and the run is still running.
	if round := h.round(t); round.State != store.RoundRunning || round.Used.Requests != 1 {
		t.Fatalf("run after refusal: state=%s used=%+v", round.State, round.Used)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 1 || ledger.Reserved.Requests != 0 {
		t.Fatalf("ledger after refusal: %+v", ledger)
	}
	// The refusal journals as a run.dispatch_refused companion carrying the
	// refusal cause.
	refusedEvent := false
	for _, e := range h.journal(t) {
		if e.Kind == rounds.SuperviseEventDispatchRefused && e.AttemptID == refused.ID &&
			e.Outcome == researchcontract.OutcomeBudgetExhausted {
			refusedEvent = true
		}
	}
	if !refusedEvent {
		t.Fatal("no run.dispatch_refused event for the refused attempt")
	}
	// The settled key names the fence honestly (stale, the failures
	// precedent) with no new charge — it is neither barred nor dangling.
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request: fetchRequest(board.URL + "/two"), Bounds: standardBounds(),
		IdempotencyKey: "t24-f2-last",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("refused-key redispatch: %+v", cerr)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 1 || ledger.Reserved.Requests != 0 {
		t.Fatalf("refused-key redispatch charged: %+v", ledger)
	}
	// Nothing dangles: Stop finds no uncertain work and the run resumes
	// with nothing to reconcile.
	stopped, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "F2 fence check")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Uncertain != 0 || stopped.Released != 0 {
		t.Fatalf("stop output: %+v", stopped)
	}
	resumed, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reconciled != 0 || len(resumed.Unknown) != 0 {
		t.Fatalf("resume output: %+v", resumed)
	}
	if resumed.Remaining.Requests != 1 {
		t.Fatalf("remaining after resume: %+v", resumed.Remaining)
	}
	// Residual conservatism (pinned, see the scope note): another attempt
	// at the final unit refuses cleanly again, still with no net charge —
	// repeated attempts never burn.
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request:        fetchRequest(board.URL + "/three"),
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-f2-again",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeBudgetExhausted {
		t.Fatalf("repeat final-unit dispatch: %+v", cerr)
	}
	if round := h.round(t); round.Used.Requests != 1 {
		t.Fatalf("repeat refusal burned allowance: %+v", round.Used)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 1 || ledger.Reserved.Requests != 0 {
		t.Fatalf("ledger after repeat refusal: %+v", ledger)
	}
}
