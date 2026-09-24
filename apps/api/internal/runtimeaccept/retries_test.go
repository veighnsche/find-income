package runtimeaccept

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Retries draw from the same owner-visible run ledger: a failed request
// charges once and its key must be re-issued (failures store no replayable
// result), a new-key repeat serves the cached negative without
// redispatching, a fresh request charges again and captures, a success
// replays free, and an exact-request repeat reuses the original receipt.
// Invalid-descriptor handling is pinned separately (executor pre-reserve
// validation plus the F3 supervisor-path repro below).
func TestRetriesDrawFromSameLedger(t *testing.T) {
	allow := &generated.ResearchAllowance{TimeMs: 15 * 60 * 1000,
		MaxActions: 8, MaxJev: 0, MaxTurns: 1, MaxConcurrent: 1}
	h := newHarness(t, allow, "retry ledger", "t24-retry-1", nil)

	var flaky atomic.Int64 // 0 -> 404, 1 -> 200
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if flaky.Load() == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body><p>flaky posting body</p></body></html>")
	})
	board := fixtureServer(t, mux)
	req := fetchRequest(board.URL + "/flaky")

	// A failed request charges one attempt and persists as a
	// distinguishable negative (T06 maps the failed receipt to invalid).
	failed := h.dispatch(t, "t24-retry-first", researchcontract.ExecuteFetch, req)
	if failed.Outcome != researchcontract.OutcomeInvalid {
		t.Fatalf("missing page outcome: %s", failed.Outcome)
	}
	if failed.Receipt.Status == researchcontract.ReceiptOK {
		t.Fatalf("missing page receipt: %+v", failed.Receipt)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 1 {
		t.Fatalf("observed after failure: %+v", ledger.Observed)
	}

	// Failed attempts store no result, so the same key cannot replay: it
	// names the fence honestly (re-issue under a new key) with no new
	// dispatch and no new charge.
	_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request: req, Bounds: standardBounds(), IdempotencyKey: "t24-retry-first",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("failed-key redispatch: %+v", cerr)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("failed-key redispatch hit %d times", n)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 1 {
		t.Fatalf("failed-key redispatch recharged: %+v", ledger.Observed)
	}

	// A new-key retry serves the cached negative without redispatching:
	// failures are freshness-cached like successes (the designed escape
	// is a justified refresh through the research_memory tool, not a
	// blind Dispatch repeat).
	negReuse := h.dispatch(t, "t24-retry-second", researchcontract.ExecuteFetch, req)
	if negReuse.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("negative repeat outcome: %s", negReuse.Outcome)
	}
	if negReuse.Receipt.Status == researchcontract.ReceiptOK {
		t.Fatalf("negative repeat receipt: %+v", negReuse.Receipt)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("negative repeat redispatched (%d hits)", n)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 2 || ledger.Reserved.Requests != 0 {
		t.Fatalf("observed after negative repeat: %+v", ledger)
	}

	// The healed bytes are a fresh request (distinct fingerprint): the
	// retry charges a new attempt and captures them.
	flaky.Store(1)
	healed := fetchRequest(board.URL + "/flaky?healed=1")
	retried := h.dispatch(t, "t24-retry-third", researchcontract.ExecuteFetch, healed)
	if retried.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("retry outcome: %s", retried.Outcome)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("retry hits: %d", n)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 3 {
		t.Fatalf("observed after retry: %+v", ledger.Observed)
	}

	// A success replays free: the same key serves the stored output with
	// no new dispatch and no new charge.
	replay, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteFetch,
		Request: healed, Bounds: standardBounds(), IdempotencyKey: "t24-retry-third",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("replay: %+v", replay)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("replay redispatched (%d hits)", n)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 3 {
		t.Fatalf("replay recharged: %+v", ledger.Observed)
	}

	// An exact-request repeat under another key reuses the original
	// backend-issued receipt without redispatching.
	reused := h.dispatch(t, "t24-retry-repeat", researchcontract.ExecuteFetch, healed)
	if reused.Outcome != researchcontract.OutcomeReused {
		t.Fatalf("repeat outcome: %s", reused.Outcome)
	}
	if reused.Receipt.ID != retried.Receipt.ID {
		t.Fatal("reuse did not serve the original receipt")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("repeat redispatched (%d hits)", n)
	}
	if ledger := h.ledger(t); ledger.Observed.Requests != 4 || ledger.Reserved.Requests != 0 {
		t.Fatalf("observed after repeat: %+v", ledger)
	}

	if ledger := h.ledger(t); ledger.Enforced.Requests != 8 {
		t.Fatalf("enforced moved: %+v", ledger.Enforced)
	}
}

// The wired executor validates before touching authority: a non-read-only
// verb is rejected with no attempt row and no charge.
func TestExecutorValidatesPreReserve(t *testing.T) {
	allow := &generated.ResearchAllowance{TimeMs: 15 * 60 * 1000,
		MaxActions: 8, MaxJev: 0, MaxTurns: 1, MaxConcurrent: 1}
	h := newHarness(t, allow, "executor validation", "t24-execval-1", nil)
	before := h.ledger(t)

	bad := fetchRequest("http://127.0.0.1:9/flaky")
	bad.Method = "PUT"
	_, err := h.stack.Executor.Execute(h.ctx, researchcontract.ExecuteInput{
		Kind: researchcontract.ExecuteAPI, Request: bad,
		Bounds: standardBounds(), IdempotencyKey: "t24-execval-badverb",
		RunID: h.runID, Generation: h.generation(t),
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeInvalid {
		t.Fatalf("bad verb: %+v", cerr)
	}
	if _, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-execval-badverb"); err == nil {
		t.Fatal("invalid request left an attempt row")
	}
	if after := h.ledger(t); after != before {
		t.Fatalf("invalid request moved the ledger: %+v -> %+v", before, after)
	}
}

// T24 finding F3, FIXED (T24-fix rewrite of the _F3Repro repro; name kept).
// Release-on-pre-claim-refusal: the executor validates the descriptor
// pre-claim (zero allowance), the supervisor releases its reserve hold with
// a full refund, and the dispatch surfaces the executor's invalid verdict
// verbatim. The key settles terminally without a stored result — same-key
// redispatch reports stale with no charge, exactly the failures precedent
// ("failures store no result", TestRetriesDrawFromSameLedger) — so the
// caller corrects the descriptor and re-issues under a new key.
func TestInvalidDescriptorBurnsAttempt_F3Repro(t *testing.T) {
	allow := &generated.ResearchAllowance{TimeMs: 15 * 60 * 1000,
		MaxActions: 8, MaxJev: 0, MaxTurns: 1, MaxConcurrent: 1}
	h := newHarness(t, allow, "F3 fixed", "t24-f3-1", nil)
	before := h.ledger(t)

	bad := fetchRequest("http://127.0.0.1:9/flaky")
	bad.Operation = researchcontract.OperationAPI // genuine api descriptor, bad verb
	bad.Method = "PUT"
	_, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteAPI,
		Request: bad, Bounds: standardBounds(), IdempotencyKey: "t24-f3-badverb",
	})
	// Invalid surfaces (the executor's verdict, verbatim), not uncertain.
	cerr := contractErr(t, err)
	if cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "request.method" {
		t.Fatalf("bad-verb dispatch: %+v", cerr)
	}
	// The attempt unwound terminally (cancelled, refusal code) with no
	// stored result instead of stranding dispatched.
	refused, err := h.db.RoundAttemptForRequest(h.ctx, h.runID, "t24-f3-badverb")
	if err != nil {
		t.Fatal(err)
	}
	if refused.State != store.AttemptCancelled || refused.ErrorCode != store.AbandonedPreClaimCode {
		t.Fatalf("refused attempt: state=%s code=%s", refused.State, refused.ErrorCode)
	}
	if len(refused.Result) != 0 {
		t.Fatal("refused attempt stored a result")
	}
	// No charge, no fence: the ledger is untouched and the run is still on
	// its first generation.
	if after := h.ledger(t); after != before {
		t.Fatalf("refused dispatch moved the ledger: %+v -> %+v", before, after)
	}
	if round := h.round(t); round.State != store.RoundRunning || round.Generation != 1 {
		t.Fatalf("run after refusal: state=%s gen=%d", round.State, round.Generation)
	}
	// The refusal journals as a run.dispatch_refused companion carrying the
	// refusal cause.
	refusedEvent := false
	for _, e := range h.journal(t) {
		if e.Kind == rounds.SuperviseEventDispatchRefused && e.AttemptID == refused.ID &&
			e.Outcome == researchcontract.OutcomeInvalid {
			refusedEvent = true
		}
	}
	if !refusedEvent {
		t.Fatal("no run.dispatch_refused event for the refused attempt")
	}
	// The settled key follows the failures precedent: same-key redispatch
	// reports stale with no new charge, and the corrected request succeeds
	// under a new key.
	_, err = h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
		RunID: h.runID, Kind: researchcontract.ExecuteAPI,
		Request: bad, Bounds: standardBounds(), IdempotencyKey: "t24-f3-badverb",
	})
	if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("refused-key redispatch: %+v", cerr)
	}
	if after := h.ledger(t); after != before {
		t.Fatalf("refused-key redispatch moved the ledger: %+v -> %+v", before, after)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", newCounter().handler("corrected posting body"))
	board := fixtureServer(t, mux)
	good := fetchRequest(board.URL + "/ok")
	fixed := h.dispatch(t, "t24-f3-corrected", researchcontract.ExecuteFetch, good)
	if fixed.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("corrected dispatch: %s", fixed.Outcome)
	}
}
