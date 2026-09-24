package runtimeaccept

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Unknown spend is never silent: while uncertain work stands the ledger
// and the product run view both report unknown, and with no dispatch
// observer wired resume stays paused without burning a reconciliation
// check.
func TestUnknownSpendStaysLatched(t *testing.T) {
	h := newHarness(t, nil, "unknown latch", "t24-unknown-1", nil)
	if fresh := h.ledger(t); fresh.Unknown {
		t.Fatalf("fresh run reports unknown: %+v", fresh)
	}

	g := newGate()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", g.handler("unknown posting body"))
	board := fixtureServer(t, mux)

	dispatchErr := make(chan error, 1)
	go func() {
		_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
			RunID: h.runID, Kind: researchcontract.ExecuteFetch,
			Request:        fetchRequest(board.URL + "/slow"),
			Bounds:         standardBounds(),
			IdempotencyKey: "t24-unknown-op",
		})
		dispatchErr <- err
	}()
	g.waitEntered(t)
	if _, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "unknown check"); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	if cerr := contractErr(t, awaitErr(t, dispatchErr)); cerr.Code != researchcontract.OutcomeStopped {
		t.Fatalf("fenced dispatch: %+v", cerr)
	}

	ledger := h.ledger(t)
	if !ledger.Unknown || ledger.Reserved.Requests != 1 || ledger.Observed.Requests != 0 {
		t.Fatalf("uncertain ledger: %+v", ledger)
	}
	view, err := h.stack.Research.ResearchRun(h.ctx, h.owner, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Usage.Unknown {
		t.Fatalf("run view hides unknown spend: %+v", view.Usage)
	}

	usedBefore := h.round(t).Used
	if _, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID); contractErr(t, err).Code != researchcontract.OutcomeUncertain {
		t.Fatalf("observerless resume: %v", err)
	}
	if round := h.round(t); round.State != store.RoundPaused {
		t.Fatalf("run state after unverified resume: %s", round.State)
	}
	if used := h.round(t).Used; used != usedBefore {
		t.Fatalf("observerless resume burned allowance: %+v -> %+v", usedBefore, used)
	}
	attempt, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-unknown-op")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.stack.Authority.ReconciliationFor(h.ctx, attempt.ID); contractErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("unexpected reconciliation record: %v", err)
	}
	if ledger := h.ledger(t); !ledger.Unknown {
		t.Fatal("unknown latch cleared while uncertain work stands")
	}
}

// T24 finding F1, FIXED (T24-fix rewrite; name kept for continuity). The
// production dispatch observer (codexservice) resolves research attempts as
// observed_failure with fenced evidence, so one resume reconciles the fenced
// flight and the run continues on its remaining allowance — no repeated
// check burns, no stranded pause. Proven here against the real observer
// code with a never-connected service instance (no transport involved).
func TestProdObserverLeavesResearchUnreconciled(t *testing.T) {
	h := newHarness(t, nil, "F1 fixed", "t24-f1-1", nil)
	svc, err := codexservice.New(h.ctx, h.db, codexservice.Config{
		Host: "isolated.test", User: "runner", IdentityFile: "/key", KnownHostsFile: "/known",
		Launcher: "/runner/launch", IsolationVerified: true, BridgeToken: strings.Repeat("t", 64),
		Model: "test-model", Effort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := h.stack.Supervisor.SetObserver(svc); err != nil {
		t.Fatal(err)
	}

	g := newGate()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", g.handler("f1 posting body"))
	mux.HandleFunc("/fresh", newCounter().handler("f1 fresh body"))
	board := fixtureServer(t, mux)

	dispatchErr := make(chan error, 1)
	go func() {
		_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
			RunID: h.runID, Kind: researchcontract.ExecuteFetch,
			Request:        fetchRequest(board.URL + "/slow"),
			Bounds:         standardBounds(),
			IdempotencyKey: "t24-f1-op",
		})
		dispatchErr <- err
	}()
	g.waitEntered(t)
	stopped, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "F1 check")
	if err != nil {
		t.Fatal(err)
	}
	close(g.release)
	if cerr := contractErr(t, awaitErr(t, dispatchErr)); cerr.Code != researchcontract.OutcomeStopped {
		t.Fatalf("fenced dispatch: %+v", cerr)
	}
	attempt, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-f1-op")
	if err != nil {
		t.Fatal(err)
	}

	// The production observer's verdict on a research attempt, called
	// directly: observed failure with fenced evidence.
	verdict, err := svc.ObserveDispatch(h.ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.State != store.AttemptObservedFailure || !strings.Contains(string(verdict.Evidence), "research_attempt_fenced") {
		t.Fatalf("prod verdict on research attempt: %+v", verdict)
	}

	// One resume reconciles the fenced flight (a single check) and the run
	// continues: generation rotates, credentials rotate, the attempt lands
	// observed_failure, and the ledger reads known — every charge accounted
	// (direct resolution, as in TestVerifiedReconcileResumesOnRemaining).
	credBefore := h.stack.Authority.CredentialVersion(h.runID)
	resumed, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reconciled != 1 || len(resumed.Unknown) != 0 {
		t.Fatalf("resume: %+v", resumed)
	}
	if resumed.Generation != stopped.Generation+1 {
		t.Fatalf("resume generation: %d after stop %d", resumed.Generation, stopped.Generation)
	}
	if v := h.stack.Authority.CredentialVersion(h.runID); v != credBefore+1 {
		t.Fatalf("credential version: %d, want %d", v, credBefore+1)
	}
	rec, err := h.stack.Authority.ReconciliationFor(h.ctx, attempt.ID)
	if err != nil || rec.State != string(store.AttemptObservedFailure) {
		t.Fatalf("reconciliation: %+v %v", rec, err)
	}
	ledger := h.ledger(t)
	if ledger.Observed.Requests != 2 || ledger.Observed.Tools != 2 || ledger.Reserved.Requests != 0 {
		t.Fatalf("observed after reconcile: %+v", ledger)
	}
	if resumed.Remaining.Requests != ledger.Enforced.Requests-2 ||
		resumed.Remaining.Tools != ledger.Enforced.Tools-2 {
		t.Fatalf("remaining after reconcile: %+v", resumed.Remaining)
	}
	if ledger.Unknown {
		t.Fatalf("ledger unknown after direct resolution: %+v", ledger)
	}
	if round := h.round(t); round.State != store.RoundRunning {
		t.Fatalf("run state after resume: %s", round.State)
	}
	// The run continues: fresh work dispatches, and a second resume is a
	// plain conflict (running) instead of another burned check.
	fresh := h.dispatch(t, "t24-f1-fresh", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/fresh"))
	if fresh.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("post-resume dispatch: %s", fresh.Outcome)
	}
	if _, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID); contractErr(t, err).Code != researchcontract.OutcomeConflict {
		t.Fatalf("second resume: %v", err)
	}
}

// A verifiable verdict settles the attempt and resumes the run on its
// remaining allowance. The verdict source is the only double here (see
// F1); the drain, charge, rotation, restore and journaling are the real
// path.
func TestVerifiedReconcileResumesOnRemaining(t *testing.T) {
	h := newHarness(t, nil, "verified reconcile", "t24-verify-1", nil)
	observer := &scriptObserver{state: store.AttemptObservedFailure,
		evidence: json.RawMessage(`{"code":"fenced_before_finish"}`)}
	if err := h.stack.Supervisor.SetObserver(observer); err != nil {
		t.Fatal(err)
	}

	g := newGate()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", g.handler("verify posting body"))
	mux.HandleFunc("/fresh", newCounter().handler("verify fresh body"))
	board := fixtureServer(t, mux)

	dispatchErr := make(chan error, 1)
	go func() {
		_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
			RunID: h.runID, Kind: researchcontract.ExecuteFetch,
			Request:        fetchRequest(board.URL + "/slow"),
			Bounds:         standardBounds(),
			IdempotencyKey: "t24-verify-op",
		})
		dispatchErr <- err
	}()
	g.waitEntered(t)
	stopped, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "verify check")
	if err != nil {
		t.Fatal(err)
	}
	close(g.release)
	if cerr := contractErr(t, awaitErr(t, dispatchErr)); cerr.Code != researchcontract.OutcomeStopped {
		t.Fatalf("fenced dispatch: %+v", cerr)
	}

	credBefore := h.stack.Authority.CredentialVersion(h.runID)
	resumed, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reconciled != 1 || len(resumed.Unknown) != 0 {
		t.Fatalf("resume: %+v", resumed)
	}
	if resumed.Generation != stopped.Generation+1 {
		t.Fatalf("resume generation: %d after stop %d", resumed.Generation, stopped.Generation)
	}
	if v := h.stack.Authority.CredentialVersion(h.runID); v != credBefore+1 {
		t.Fatalf("credential version: %d, want %d", v, credBefore+1)
	}
	ledger := h.ledger(t)
	if ledger.Observed.Requests != 2 || ledger.Observed.Tools != 2 || ledger.Reserved.Requests != 0 {
		t.Fatalf("observed after reconcile: %+v", ledger)
	}
	if resumed.Remaining.Requests != ledger.Enforced.Requests-2 ||
		resumed.Remaining.Tools != ledger.Enforced.Tools-2 {
		t.Fatalf("remaining after reconcile: %+v", resumed.Remaining)
	}
	// Direct resolution leaves no uncertain attempt and no unknown-settled
	// check, so the ledger reads known: every charge is accounted and the
	// attempt is terminal. The uncertainty history persists in the late
	// result, the late_observation event and the observed_failure state —
	// the Unknown latch (T06 §5) holds while work is unverified or a
	// check settled unknown, as the F1 test pins.
	if ledger.Unknown {
		t.Fatalf("ledger unknown after direct resolution: %+v", ledger)
	}
	fresh := h.dispatch(t, "t24-verify-fresh", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/fresh"))
	if fresh.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("post-resume dispatch: %s", fresh.Outcome)
	}
}
