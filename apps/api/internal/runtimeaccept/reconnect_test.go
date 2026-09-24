package runtimeaccept

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Crash between operations: close and reopen the store, rewire a fresh
// stack, and reconnect. The run is paused with a rotated generation, the
// journal and checkpoint stand, resume continues on the remaining
// allowance, and an exact repeat reuses without redispatching.
func TestCrashReconnectsAndReuses(t *testing.T) {
	h := newHarness(t, nil, "crash reconnect brief", "t24-crash-1", nil)
	hits := newCounter()
	mux := http.NewServeMux()
	mux.HandleFunc("/role", hits.handler("crash posting body with marker CRASH-1"))
	board := fixtureServer(t, mux)

	fetched := h.dispatch(t, "t24-crash-fetch", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/role"))
	if fetched.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("fetch: %s", fetched.Outcome)
	}
	cpBefore, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	journalBefore := len(h.journal(t))
	genBefore := h.generation(t)

	// Crash: close without stopping.
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopen(t, h)

	round := h.round(t)
	if round.State != store.RoundPaused {
		t.Fatalf("reopened run state: %s", round.State)
	}
	if round.Generation != genBefore+1 {
		t.Fatalf("reopened generation: %d, want %d", round.Generation, genBefore+1)
	}
	if n := len(h.journal(t)); n != journalBefore {
		t.Fatalf("journal after reopen: %d events, want %d", n, journalBefore)
	}
	cpAfter, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cpAfter.EvidenceIDs) != len(cpBefore.EvidenceIDs) {
		t.Fatalf("checkpoint after reopen: %v, want %v", cpAfter.EvidenceIDs, cpBefore.EvidenceIDs)
	}
	if ledger := h.ledger(t); ledger.Unknown {
		t.Fatal("clean crash reports unknown spend")
	}

	resumed, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reconciled != 0 || len(resumed.Unknown) != 0 {
		t.Fatalf("clean resume: %+v", resumed)
	}
	ledger := h.ledger(t)
	if want := ledger.Enforced.Requests - 1; resumed.Remaining.Requests != want {
		t.Fatalf("remaining after resume: %+v", resumed.Remaining)
	}

	again := h.dispatch(t, "t24-crash-fetch-2", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/role"))
	if again.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("post-restart repeat: %s", again.Outcome)
	}
	if n := hits.get("/role"); n != 1 {
		t.Fatalf("repeat redispatched (%d hits)", n)
	}
	if body := h.captureBytes(t, again.CaptureID); !strings.Contains(body, "CRASH-1") {
		t.Fatalf("reused capture lost the posting: %q", body)
	}
}

// Context reconstruction: with the checkpoint row lost and no stored
// thread, the next turn rebuilds its brief from the commission record,
// journal and ledger, journals run.recovered, and settles normally.
func TestContextReconstructionAfterCheckpointLoss(t *testing.T) {
	h := newHarness(t, nil, "reconstruction brief text", "t24-reconstruct-1", nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/role", newCounter().handler("reconstruction posting body"))
	board := fixtureServer(t, mux)

	fetched := h.dispatch(t, "t24-reconstruct-fetch", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/role"))
	if fetched.Outcome != researchcontract.OutcomeOK {
		t.Fatal("fetch failed")
	}
	steered, err := h.stack.Research.SteerResearch(h.ctx, httpapi.SteerResearchInput{
		Actor: h.owner, RunID: h.runID, Body: "Prefer remote-friendly roles.",
	})
	if err != nil || steered.MessageId == "" {
		t.Fatalf("steer: %+v %v", steered, err)
	}

	// Lose the checkpoint row (crash between journal and checkpoint).
	if err := h.db.ResearchWrite(h.ctx, func(db store.ResearchDB) error {
		_, err := db.ExecContext(h.ctx, `DELETE FROM run_checkpoints WHERE round_id=?`, h.runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Reads stay pure: a missing checkpoint reads not_found, never rebuilt.
	if _, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID); contractErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("missing checkpoint read: %v", err)
	}

	runner := newScriptRunner("thread-rebuilt-1", "turn-rebuilt-1")
	runner.nextWork = []string{"follow-rebuilt"}
	if err := h.stack.Supervisor.SetTurnRunner(runner); err != nil {
		t.Fatal(err)
	}
	turn, err := h.stack.Supervisor.ContinueRun(h.ctx, h.agent, rounds.TurnInput{
		RunID: h.runID, RequestKey: "t24-reconstruct-turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != "completed" || turn.ThreadID != "thread-rebuilt-1" {
		t.Fatalf("rebuilt turn: %+v", turn)
	}
	if !strings.Contains(turn.BriefUsed, "reconstruction brief text") {
		t.Fatalf("rebuilt brief lost the commission: %q", turn.BriefUsed)
	}
	if !strings.Contains(turn.BriefUsed, "Prefer remote-friendly roles.") {
		t.Fatalf("rebuilt brief lost the steer: %q", turn.BriefUsed)
	}
	kinds := journalKinds(h.journal(t))
	if kinds[rounds.SuperviseEventRecovered] != 1 {
		t.Fatalf("recovery journal: %v", kinds)
	}
	cp, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(cp.EvidenceIDs, fetched.ObservationID) || !containsID(cp.EvidenceIDs, fetched.CaptureID) {
		t.Fatalf("rebuilt checkpoint lost evidence: %v", cp.EvidenceIDs)
	}
	if cp.RubricVersion != h.rubric {
		t.Fatalf("rebuilt checkpoint rubric: %q, want %q", cp.RubricVersion, h.rubric)
	}
	var thread string
	if err := h.db.Read(h.ctx, func(r store.Reader) error {
		var err error
		thread, err = store.LatestRoundThreadTx(h.ctx, r, h.runID)
		return err
	}); err != nil || thread != "thread-rebuilt-1" {
		t.Fatalf("bound thread: %q %v", thread, err)
	}
}

// Crash during a real in-flight operation: reopening marks the dispatched
// attempt uncertain, verified reconciliation settles it on the remaining
// allowance, and the old key stays barred while fresh work proceeds.
func TestCrashDuringFlightReconcilesOnRemaining(t *testing.T) {
	h := newHarness(t, nil, "crash during flight", "t24-crash-flight-1", nil)

	g := newGate()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", g.handler("flight posting body"))
	mux.HandleFunc("/fresh", newCounter().handler("fresh posting body"))
	board := fixtureServer(t, mux)

	dispatchErr := make(chan error, 1)
	go func() {
		_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
			RunID: h.runID, Kind: researchcontract.ExecuteFetch,
			Request:        fetchRequest(board.URL + "/slow"),
			Bounds:         standardBounds(),
			IdempotencyKey: "t24-crash-flight-op",
		})
		dispatchErr <- err
	}()
	g.waitEntered(t)

	// Crash mid-flight: the commit fails against the closed store and the
	// dispatch settles uncertain without touching durable state.
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	if cerr := contractErr(t, awaitErr(t, dispatchErr)); cerr.Code != researchcontract.OutcomeUncertain {
		t.Fatalf("crashed dispatch: %+v", cerr)
	}
	reopen(t, h)

	round := h.round(t)
	if round.State != store.RoundPaused {
		t.Fatalf("reopened run state: %s", round.State)
	}
	attempt, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-crash-flight-op")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != store.AttemptUncertain {
		t.Fatalf("crashed attempt state: %s", attempt.State)
	}
	if ledger := h.ledger(t); !ledger.Unknown {
		t.Fatal("crashed run does not report unknown spend")
	}

	observer := &scriptObserver{state: store.AttemptObservedFailure,
		evidence: json.RawMessage(`{"code":"process_interrupted_before_commit"}`)}
	if err := h.stack.Supervisor.SetObserver(observer); err != nil {
		t.Fatal(err)
	}
	resumed, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reconciled != 1 {
		t.Fatalf("resume: %+v", resumed)
	}
	ledger := h.ledger(t)
	if ledger.Observed.Requests != 2 || ledger.Observed.Tools != 2 {
		t.Fatalf("observed after reconcile: %+v", ledger.Observed)
	}
	if resumed.Remaining.Requests != ledger.Enforced.Requests-2 {
		t.Fatalf("remaining after reconcile: %+v", resumed.Remaining)
	}

	// The crashed key stays barred (no blind replay); fresh work proceeds
	// on the remaining allowance.
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request:        fetchRequest(board.URL + "/slow"),
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-crash-flight-op",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("crashed-key redispatch: %+v", cerr)
	}
	fresh := h.dispatch(t, "t24-crash-flight-fresh", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/fresh"))
	if fresh.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("post-crash fresh dispatch: %s", fresh.Outcome)
	}
}

// reopen closes the crashed supervisor handle and rewires a fresh stack on
// the same data dir, like a process restart.
func reopen(t *testing.T, h *harness) {
	t.Helper()
	h.stack.Supervisor.Close()
	db, err := store.Open(h.ctx, h.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.db = db
	h.stack = wireStack(t, h.ctx, db, h.dir, h.provider, nil)
	t.Cleanup(h.stack.Supervisor.Close)
}
