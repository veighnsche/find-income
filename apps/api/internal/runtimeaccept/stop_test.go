package runtimeaccept

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Boundary 1: Stop before any reservation. Later dispatches are refused at
// the authority check with no attempt row and no charge; the key stays
// reusable after resume.
func TestStopAtCheckBoundary(t *testing.T) {
	h := newHarness(t, nil, "stop before reserve", "t24-stop-check-1", nil)
	gen := h.generation(t)

	stopped, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "boundary check")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Generation != gen+1 || stopped.Uncertain != 0 || stopped.Released != 0 {
		t.Fatalf("stop output: %+v", stopped)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/page", newCounter().handler("boundary page"))
	board := fixtureServer(t, mux)

	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Generation: gen, Kind: researchcontract.ExecuteFetch,
		Request:        fetchRequest(board.URL + "/page"),
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-stop-check-op",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("stale-generation dispatch: %+v", cerr)
	}
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request:        fetchRequest(board.URL + "/page"),
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-stop-check-op",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStopped {
		t.Fatalf("paused-run dispatch: %+v", cerr)
	}
	if _, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-stop-check-op"); err == nil {
		t.Fatal("refused dispatch left an attempt row")
	}
	if ledger := h.ledger(t); ledger.Observed != (researchcontract.Allowance{}) ||
		ledger.Reserved != (researchcontract.Allowance{}) || ledger.Unknown {
		t.Fatalf("refused dispatch moved the ledger: %+v", ledger)
	}

	if _, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID); err != nil {
		t.Fatal(err)
	}
	out := h.dispatch(t, "t24-stop-check-op", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/page"))
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("post-resume dispatch: %s", out.Outcome)
	}
}

// Boundary 2: Stop between reserve and dispatch. The fence reaps the
// never-dispatched hold (refunding it), the dispatch point refuses with
// ErrFenced, and the same key stays stale after resume: re-issue under a
// new key.
func TestStopBetweenReserveAndDispatch(t *testing.T) {
	h := newHarness(t, nil, "stop between reserve and dispatch", "t24-stop-reserve-1", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/page", newCounter().handler("reserve-fence page"))
	board := fixtureServer(t, mux)
	req := fetchRequest(board.URL + "/page")
	fingerprint, err := req.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := h.stack.Authority.Reserve(h.ctx, h.runID, store.RoundResearchFetch, "t24-stop-reserve-op", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if ledger := h.ledger(t); ledger.Reserved.Requests != 1 || ledger.Reserved.Tools != 1 {
		t.Fatalf("reserved ledger: %+v", ledger.Reserved)
	}

	stopped, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "boundary check")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Released != 1 {
		t.Fatalf("stop output: %+v", stopped)
	}
	if ledger := h.ledger(t); ledger.Reserved != (researchcontract.Allowance{}) {
		t.Fatalf("reaped hold still reserved: %+v", ledger.Reserved)
	}
	// The dispatch fence point refuses directly: the hold can never run.
	if _, err := h.db.MarkRoundDispatched(h.ctx, h.runID, reservation.AttemptID); err != store.ErrFenced {
		t.Fatalf("mark dispatched past the fence: %v", err)
	}

	if _, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID); err != nil {
		t.Fatal(err)
	}
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request:        req,
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-stop-reserve-op",
	})
	cerr := contractErr(t, err)
	if cerr.Code != researchcontract.OutcomeStale || !strings.Contains(cerr.Detail, "re-issue under a new key") {
		t.Fatalf("fenced-key redispatch: %+v", cerr)
	}
	out := h.dispatch(t, "t24-stop-reserve-op-2", researchcontract.ExecuteFetch, req)
	if out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("re-issued dispatch: %s", out.Outcome)
	}
}

// Boundary 3: Stop during a real in-flight operation. The completion lands
// as late evidence only (late_result_json + one late_observation event,
// never a current result or checkpoint line), the unknown latch holds, and
// after verified reconciliation the run resumes on its remaining allowance.
// The same request retries honestly: it reuses the committed cancellation
// report instead of redispatching, never a blind replay of late bytes.
func TestStopDuringFlightKeepsLateEvidence(t *testing.T) {
	h := newHarness(t, nil, "stop during flight", "t24-stop-flight-1", nil)
	observer := &scriptObserver{state: store.AttemptObservedFailure,
		evidence: json.RawMessage(`{"code":"fenced_before_finish"}`)}
	if err := h.stack.Supervisor.SetObserver(observer); err != nil {
		t.Fatal(err)
	}

	g := newGate()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", g.handler("slow posting body"))
	mux.HandleFunc("/other", newCounter().handler("other posting body"))
	board := fixtureServer(t, mux)

	dispatchErr := make(chan error, 1)
	go func() {
		_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
			RunID: h.runID, Kind: researchcontract.ExecuteFetch,
			Request:        fetchRequest(board.URL + "/slow"),
			Bounds:         standardBounds(),
			IdempotencyKey: "t24-stop-flight-op",
		})
		dispatchErr <- err
	}()
	g.waitEntered(t)

	stopped, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "owner stopped mid-flight")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Uncertain != 1 {
		t.Fatalf("stop output: %+v", stopped)
	}
	close(g.release)
	if cerr := contractErr(t, awaitErr(t, dispatchErr)); cerr.Code != researchcontract.OutcomeStopped {
		t.Fatalf("fenced dispatch: %+v", cerr)
	}

	attempt, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-stop-flight-op")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != store.AttemptUncertain {
		t.Fatalf("fenced attempt state: %s", attempt.State)
	}
	if len(attempt.LateResult) == 0 || len(attempt.Result) != 0 {
		t.Fatalf("late attempt: result=%d late=%d", len(attempt.Result), len(attempt.LateResult))
	}
	var late struct {
		Outcome       researchcontract.Outcome          `json:"outcome"`
		ObservationID string                            `json:"observationId"`
		CaptureID     string                            `json:"captureId"`
		Receipt       researchcontract.ExecutionReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(attempt.LateResult, &late); err != nil {
		t.Fatal(err)
	}
	// The gate holds the fixture response until after Stop returns, so the
	// fence deterministically cancels the fetch first: the late result
	// carries the canceled observation with no capture bytes, and the
	// receipt honestly reports unknown usage (the request went out, the
	// response never arrived).
	if late.ObservationID == "" {
		t.Fatalf("late result without observation: %s", attempt.LateResult)
	}
	if late.Receipt.Status != researchcontract.ReceiptCanceled || !late.Receipt.UnknownUsage {
		t.Fatalf("late receipt: %+v", late.Receipt)
	}
	lateIDs := []string{late.ObservationID}
	if late.CaptureID != "" {
		lateIDs = append(lateIDs, late.CaptureID)
	}
	results, err := h.db.RoundResults(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range results {
		for _, id := range lateIDs {
			if strings.Contains(string(raw), id) {
				t.Fatalf("late completion wrote a current result: %s", raw)
			}
		}
	}
	kinds := journalKinds(h.journal(t))
	if kinds[researchcontract.EventLateObservation] != 1 {
		t.Fatalf("late journal: %v", kinds)
	}
	cp, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range lateIDs {
		if containsID(cp.EvidenceIDs, id) {
			t.Fatalf("checkpoint carries late evidence %s", id)
		}
	}
	ledger := h.ledger(t)
	if !ledger.Unknown || ledger.Reserved.Requests != 1 || ledger.Observed.Requests != 0 {
		t.Fatalf("uncertain ledger: %+v", ledger)
	}

	// Verified reconciliation settles the attempt and resumes the run on
	// its remaining allowance: one request+tool for the attempt, one more
	// pair for the check.
	resumed, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Reconciled != 1 || len(resumed.Unknown) != 0 {
		t.Fatalf("resume: %+v", resumed)
	}
	if len(observer.calls) != 1 || observer.calls[0] != attempt.ID {
		t.Fatalf("observer calls: %v", observer.calls)
	}
	rec, err := h.stack.Authority.ReconciliationFor(h.ctx, attempt.ID)
	if err != nil || rec.State != string(store.AttemptObservedFailure) {
		t.Fatalf("reconciliation: %+v %v", rec, err)
	}
	round := h.round(t)
	if round.State != store.RoundRunning {
		t.Fatalf("run state after resume: %s", round.State)
	}
	ledger = h.ledger(t)
	if ledger.Observed.Requests != 2 || ledger.Observed.Tools != 2 {
		t.Fatalf("observed after reconcile: %+v", ledger.Observed)
	}
	if resumed.Remaining.Requests != ledger.Enforced.Requests-2 ||
		resumed.Remaining.Tools != ledger.Enforced.Tools-2 {
		t.Fatalf("remaining after reconcile: %+v (ledger %+v)", resumed.Remaining, ledger)
	}

	// The old key names its state honestly instead of replaying blindly:
	// the attempt settled via reconciliation with no stored dispatch
	// result, so the caller must re-issue.
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request:        fetchRequest(board.URL + "/slow"),
		Bounds:         standardBounds(),
		IdempotencyKey: "t24-stop-flight-op",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("settled-key redispatch: %+v", cerr)
	}
	// Same request, new key: the fence-canceled fetch committed as a
	// cached negative (the lease is attempt-scoped, so memory commits
	// normally while the supervisor fences the attempt result), so the
	// retry reuses the cancellation report instead of redispatching —
	// no blind replay, no silent drop.
	retry := h.dispatch(t, "t24-stop-flight-retry", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/slow"))
	if retry.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("retry after fence: %s", retry.Outcome)
	}
	if retry.Receipt.Status != researchcontract.ReceiptCanceled {
		t.Fatalf("retry receipt: %+v", retry.Receipt)
	}
	if n := g.hits.Load(); n != 1 {
		t.Fatalf("retry after fence dispatched %d times", n)
	}
	// Fresh work proceeds on the remaining allowance.
	fresh := h.dispatch(t, "t24-stop-flight-fresh", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/other"))
	if fresh.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("post-resume fresh dispatch: %s", fresh.Outcome)
	}
}

// Boundary 4: Stop during a model turn. The turn's remote completion past
// the fence keeps as late evidence only; the checkpoint never carries the
// late NextWork.
func TestStopDuringTurnKeepsLateEvidence(t *testing.T) {
	h := newHarness(t, nil, "stop during turn", "t24-stop-turn-1", nil)
	runner := newScriptRunner("thread-late-1", "turn-late-1")
	runner.release = make(chan struct{})
	runner.ignoreCancel = true // the remote call completed anyway
	runner.nextWork = []string{"late-next-work"}
	if err := h.stack.Supervisor.SetTurnRunner(runner); err != nil {
		t.Fatal(err)
	}
	observer := &scriptObserver{state: store.AttemptObservedFailure,
		evidence: json.RawMessage(`{"code":"fenced_before_finish"}`)}
	if err := h.stack.Supervisor.SetObserver(observer); err != nil {
		t.Fatal(err)
	}

	turnErr := make(chan error, 1)
	go func() {
		_, err := h.stack.Supervisor.ContinueRun(h.ctx, h.agent, rounds.TurnInput{
			RunID: h.runID, RequestKey: "t24-stop-turn-1",
		})
		turnErr <- err
	}()
	runner.waitEntered(t)

	stopped, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "owner stopped mid-turn")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Uncertain != 1 {
		t.Fatalf("stop output: %+v", stopped)
	}
	close(runner.release)
	if cerr := contractErr(t, awaitErr(t, turnErr)); cerr.Code != researchcontract.OutcomeStopped {
		t.Fatalf("fenced turn: %+v", cerr)
	}

	attempt, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-stop-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != store.AttemptUncertain || len(attempt.LateResult) == 0 || len(attempt.Result) != 0 {
		t.Fatalf("late turn: state=%s result=%d late=%d", attempt.State, len(attempt.Result), len(attempt.LateResult))
	}
	cp, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cp.NextWork {
		if w == "late-next-work" {
			t.Fatalf("checkpoint carries late next work: %v", cp.NextWork)
		}
	}
	kinds := journalKinds(h.journal(t))
	if kinds[researchcontract.EventLateObservation] != 1 {
		t.Fatalf("late journal: %v", kinds)
	}

	if _, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID); err != nil {
		t.Fatal(err)
	}
	if round := h.round(t); round.State != store.RoundRunning {
		t.Fatalf("run state after resume: %s", round.State)
	}
}

// Boundary 5: Stop fences record writes. A save under the stale generation
// is rejected with nothing written, previously saved audited records stand,
// and the same write succeeds after resume rotates authority.
func TestStopFencesRecordWrites(t *testing.T) {
	h := newHarness(t, nil, "stop fences writes", "t24-stop-write-1", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/role", newCounter().handler("Northwind Traders seeks a Senior Backend Engineer in Berlin. Full-time role."))
	board := fixtureServer(t, mux)
	fetched := h.dispatch(t, "t24-stop-write-fetch", researchcontract.ExecuteFetch, fetchRequest(board.URL+"/role"))
	if fetched.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("fetch: %s", fetched.Outcome)
	}
	capID := fetched.Receipt.CaptureID
	body := h.captureBytes(t, capID)
	ss, se := spanOf(t, body, "Senior Backend Engineer")
	ts, te := spanOf(t, body, "Northwind Traders")
	link := func(start, end int64) researchcontract.EvidenceLink {
		return researchcontract.EvidenceLink{CaptureID: capID, SpanStart: start, SpanEnd: end,
			ExcerptSHA256: excerptSHA(body, int(start), int(end))}
	}
	h.provider.verdicts["q-write-1"] = "new"
	assessed, err := h.stack.Assessor.Assess(h.ctx, researchcontract.AssessInput{
		Purpose: "identity_match",
		Questions: []researchcontract.AssessQuestion{{
			ID: "q-write-1", Text: "Is this a new opening?",
			Alternatives: []researchcontract.AssessAlternative{
				{ID: "new", Label: "new", EvidenceRefs: []researchcontract.EvidenceRef{{CaptureID: capID, SpanStart: ss, SpanEnd: se}}},
				{ID: "same", Label: "same", EvidenceRefs: []researchcontract.EvidenceRef{{CaptureID: capID, SpanStart: ts, SpanEnd: te}}},
			},
			AbstainAllowed: true,
		}},
		ProfileVersion: h.profile, RubricVersion: h.rubric,
		SourceRefs: []researchcontract.EvidenceRef{
			{CaptureID: capID, SpanStart: ss, SpanEnd: se},
			{CaptureID: capID, SpanStart: ts, SpanEnd: te},
		},
		IdempotencyKey: "t24-stop-write-assess", RunID: h.runID, Generation: h.generation(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	saver, err := h.stack.NewSaverFor(h.agent)
	if err != nil {
		t.Fatal(err)
	}
	gen := h.generation(t)
	saved, err := saver.Save(h.ctx, researchcontract.SaveBatch{
		Items: []researchcontract.SaveItem{
			{Op: researchcontract.SaveCreateCompany,
				Fields:        map[string]string{"name": "Northwind Traders"},
				EvidenceLinks: []researchcontract.EvidenceLink{link(ts, te)}},
			{Op: researchcontract.SaveCreateOpportunity,
				Fields: map[string]string{"companyId": "batch:0", "title": "Senior Backend Engineer",
					"kind": "employment", "sourceUrl": board.URL + "/role", "originalText": body,
					"locationText": "Berlin", "vacancyComplete": "true"},
				EvidenceLinks:    []researchcontract.EvidenceLink{link(ss, se), link(ts, te)},
				AssessmentIDs:    []string{assessed.ID},
				IdentityDecision: &researchcontract.IdentityDecision{Decision: "new"}},
		},
		IdempotencyKey: "t24-stop-write-save", RunID: h.runID, Generation: gen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Outcome != researchcontract.OutcomeOK || len(saved.Saved) != 2 {
		t.Fatalf("save: %+v", saved)
	}
	oppID := saved.Saved[1].RecordID
	rev1 := saved.Saved[1].Revision
	sightingsBefore := recordSightings(t, h, oppID)

	if _, err := h.stack.Supervisor.Stop(h.ctx, h.owner, h.runID, "write fence check"); err != nil {
		t.Fatal(err)
	}
	// Saved audited records stand across the fence.
	opp, err := h.db.Opportunity(h.ctx, oppID)
	if err != nil || opp.Revision != rev1 {
		t.Fatalf("saved record after stop: %+v %v", opp, err)
	}
	if n := recordSightings(t, h, oppID); n != sightingsBefore {
		t.Fatalf("sightings after stop: %d, want %d", n, sightingsBefore)
	}

	update := func(key string, generation int64) (researchcontract.SaveOutput, error) {
		return saver.Save(h.ctx, researchcontract.SaveBatch{
			Items: []researchcontract.SaveItem{
				{Op: researchcontract.SaveUpdateOpportunity, RecordID: oppID, ExpectedRevision: rev1,
					Fields: map[string]string{"title": "Senior Backend Engineer",
						"kind": "employment", "sourceUrl": board.URL + "/role", "originalText": body,
						"locationText": "Berlin", "vacancyComplete": "true"},
					EvidenceLinks: []researchcontract.EvidenceLink{link(ss, se)},
					AssessmentIDs: []string{assessed.ID},
					IdentityDecision: &researchcontract.IdentityDecision{Decision: "same",
						Candidates: []researchcontract.CandidateIdentity{{CandidateID: oppID, Kind: "opportunity", Revision: rev1}}}},
			},
			IdempotencyKey: key, RunID: h.runID, Generation: generation,
		})
	}
	// A late write under the stale generation is rejected with nothing
	// written.
	out, err := update("t24-stop-write-update", gen)
	if err == nil && out.Outcome == researchcontract.OutcomeOK {
		t.Fatalf("stale-generation write accepted: %+v", out)
	}
	if cerr, ok := asContractErr(err); ok && cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("stale-generation write: %+v", cerr)
	}
	if opp, err := h.db.Opportunity(h.ctx, oppID); err != nil || opp.Revision != rev1 {
		t.Fatalf("late write mutated the record: %+v %v", opp, err)
	}
	if n := recordSightings(t, h, oppID); n != sightingsBefore {
		t.Fatalf("late write added sightings: %d, want %d", n, sightingsBefore)
	}

	// The same write succeeds after resume rotates authority.
	if _, err := h.stack.Supervisor.Resume(h.ctx, h.owner, h.runID); err != nil {
		t.Fatal(err)
	}
	out, err = update("t24-stop-write-update-2", h.generation(t))
	if err != nil || out.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("post-resume write: %+v %v", out, err)
	}
	if opp, err := h.db.Opportunity(h.ctx, oppID); err != nil || opp.Revision != rev1+1 {
		t.Fatalf("record after post-resume write: %+v %v", opp, err)
	}
}

func recordSightings(t *testing.T, h *harness, oppID string) int {
	t.Helper()
	var n int
	if err := h.db.Read(h.ctx, func(r store.Reader) error {
		rows, err := store.ListRecordSightingsByOpportunity(h.ctx, r, oppID)
		if err != nil {
			return err
		}
		n = len(rows)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func asContractErr(err error) (*researchcontract.Error, bool) {
	var cerr *researchcontract.Error
	if err == nil {
		return nil, false
	}
	if errors.As(err, &cerr) {
		return cerr, true
	}
	return nil, false
}
