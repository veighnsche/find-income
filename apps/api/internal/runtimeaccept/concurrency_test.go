package runtimeaccept

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/httpapi/generated"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
)

// Two real research operations run concurrently under one finite allowance:
// both settle from the same ledger, the enforced limits never move, and a
// duplicate concurrent claim dispatches exactly once. (Allowance exhaustion
// itself is pinned in TestAuthorityExhaustionRefusesCleanly; spending the
// final unit through Dispatch is T24 finding F2.)
func TestTwoRealOpsShareOneAllowance(t *testing.T) {
	allow := &generated.ResearchAllowance{TimeMs: 15 * 60 * 1000,
		MaxActions: 8, MaxJev: 0, MaxTurns: 1, MaxConcurrent: 2}
	h := newHarness(t, allow, "concurrency under one allowance", "t24-conc-1", nil)
	before := h.ledger(t)
	if before.Enforced.Requests != 8 || before.Enforced.Items != 8 ||
		before.Enforced.Tools != 9 || before.Enforced.Turns != 1 {
		t.Fatalf("commissioned ledger: %+v", before.Enforced)
	}

	// Rendezvous: each handler waits until both requests have arrived, so a
	// green run proves the two operations truly overlapped inside the
	// MaxConcurrent=2 bound instead of serializing.
	var arrivals atomic.Int64
	bothHere := make(chan struct{})
	var once sync.Once
	meet := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if arrivals.Add(1) == 2 {
				once.Do(func() { close(bothHere) })
			}
			select {
			case <-bothHere:
			case <-time.After(60 * time.Second):
				t.Error("rendezvous partner never arrived")
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html><body><p>"+body+"</p></body></html>")
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/a", meet("role alpha listing body"))
	mux.HandleFunc("/b", meet("role beta listing body"))
	board := fixtureServer(t, mux)

	type settled struct {
		out rounds.DispatchOutput
		err error
	}
	results := make(chan settled, 2)
	var wg sync.WaitGroup
	for _, tc := range []struct{ key, path string }{
		{"t24-conc-a", "/a"},
		{"t24-conc-b", "/b"},
	} {
		wg.Add(1)
		go func(key, path string) {
			defer wg.Done()
			out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
				RunID: h.runID, Kind: researchcontract.ExecuteFetch,
				Request:        fetchRequest(board.URL + path),
				Bounds:         standardBounds(),
				IdempotencyKey: key,
			})
			results <- settled{out, err}
		}(tc.key, tc.path)
	}
	wg.Wait()
	close(results)

	var outs []rounds.DispatchOutput
	for r := range results {
		if r.err != nil {
			t.Fatalf("concurrent dispatch: %v", r.err)
		}
		if r.out.Outcome != researchcontract.OutcomeOK {
			t.Fatalf("concurrent dispatch outcome: %s", r.out.Outcome)
		}
		outs = append(outs, r.out)
	}
	select {
	case <-bothHere:
	default:
		t.Fatal("operations did not overlap inside the rendezvous")
	}
	if outs[0].CaptureID == outs[1].CaptureID {
		t.Fatal("distinct pages share one capture id")
	}
	if !strings.Contains(h.captureBytes(t, outs[0].CaptureID), "alpha") &&
		!strings.Contains(h.captureBytes(t, outs[1].CaptureID), "alpha") {
		t.Fatal("captured bytes do not carry the fixture postings")
	}

	ledger := h.ledger(t)
	if ledger.Enforced != before.Enforced {
		t.Fatalf("enforced moved: %+v -> %+v", before.Enforced, ledger.Enforced)
	}
	if ledger.Observed.Requests != 2 || ledger.Observed.Tools != 2 ||
		ledger.Observed.Items != 0 || ledger.Observed.Turns != 0 {
		t.Fatalf("observed after two ops: %+v", ledger.Observed)
	}
	if ledger.Reserved != (researchcontract.Allowance{}) {
		t.Fatalf("reserved not drained: %+v", ledger.Reserved)
	}
	if ledger.Unknown {
		t.Fatal("clean concurrent run reports unknown spend")
	}
	cp, err := h.stack.Supervisor.Checkpoint(h.ctx, h.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{outs[0].ObservationID, outs[0].CaptureID, outs[1].ObservationID, outs[1].CaptureID} {
		if !containsID(cp.EvidenceIDs, id) {
			t.Fatalf("checkpoint lost %s (%v)", id, cp.EvidenceIDs)
		}
	}
	kinds := journalKinds(h.journal(t))
	if kinds["run.commissioned"] != 1 || kinds["run.dispatched"] != 2 || kinds["run.observed"] != 2 {
		t.Fatalf("supervisor journal: %v", kinds)
	}
	if kinds["claim"] == 0 || kinds["capture"] == 0 || kinds["observation"] == 0 {
		t.Fatalf("research journal missing C kinds: %v", kinds)
	}

	// Duplicate concurrent claim: same fingerprint, two keys. Exactly one
	// dispatch happens; the loser honestly reports claimed_elsewhere (lease
	// still live) or reused (winner already committed), never a second hit.
	dups := newCounter()
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/dup", dups.handler("shared posting body"))
	board2 := fixtureServer(t, mux2)
	dupResults := make(chan settled, 2)
	for _, key := range []string{"t24-conc-dup-1", "t24-conc-dup-2"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			out, err := h.stack.Supervisor.Dispatch(h.ctx, rounds.DispatchInput{
				RunID: h.runID, Kind: researchcontract.ExecuteFetch,
				Request:        fetchRequest(board2.URL + "/dup"),
				Bounds:         standardBounds(),
				IdempotencyKey: key,
			})
			dupResults <- settled{out, err}
		}(key)
	}
	wg.Wait()
	close(dupResults)
	var oks, alt int
	for r := range dupResults {
		if r.err != nil {
			t.Fatalf("duplicate dispatch: %v", r.err)
		}
		switch r.out.Outcome {
		case researchcontract.OutcomeOK:
			oks++
		case researchcontract.OutcomeClaimedElsewhere, researchcontract.OutcomeReused:
			alt++
		default:
			t.Fatalf("duplicate outcome: %s", r.out.Outcome)
		}
	}
	if oks != 1 || alt != 1 {
		t.Fatalf("duplicate pair: %d ok + %d alternate", oks, alt)
	}
	if n := dups.get("/dup"); n != 1 {
		t.Fatalf("duplicate claim dispatched %d times", n)
	}
	ledger = h.ledger(t)
	if ledger.Observed.Requests != 4 || ledger.Observed.Tools != 4 {
		t.Fatalf("observed after four ops: %+v", ledger.Observed)
	}
	if ledger.Enforced != before.Enforced {
		t.Fatalf("enforced moved: %+v -> %+v", before.Enforced, ledger.Enforced)
	}
	if ledger.Unknown {
		t.Fatal("clean concurrent run reports unknown spend")
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
