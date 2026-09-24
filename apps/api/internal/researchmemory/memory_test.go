package researchmemory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func TestClaimGrantsLeaseAndLookupSeesIt(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()

	lookup, err := env.mem.Lookup(ctx, "round-1", testDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, lookup, researchcontract.OutcomeOK)

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	mustOutcome(t, claim, researchcontract.OutcomeOK)
	if claim.Lease == nil || claim.Lease.LeaseID == "" || claim.Lease.Owner != "attempt-1" || claim.Lease.Generation != 1 {
		t.Fatalf("bad lease: %+v", claim.Lease)
	}
	if want := env.clock.now().Add(time.Minute); !claim.Lease.ExpiresAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", claim.Lease.ExpiresAt, want)
	}

	lookup, err = env.mem.Lookup(ctx, "round-1", testDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, lookup, researchcontract.OutcomeClaimedElsewhere)

	// Same holder re-asserts its own live lease (replay/share).
	again := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	mustOutcome(t, again, researchcontract.OutcomeOK)
	if again.Lease.LeaseID != claim.Lease.LeaseID {
		t.Fatal("replay did not return the same lease")
	}

	// A different worker is fenced out.
	other, err := env.mem.Claim(ctx, "round-1", "attempt-2", 1, testDescriptor(), "key-2")
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, other, researchcontract.OutcomeClaimedElsewhere)
	if other.Lease != nil {
		t.Fatal("loser must not receive a lease")
	}
}

func TestConcurrentClaimsOneWins(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()

	// A second store handle on the same database file: two concurrent
	// writers, one winner, no busy errors escaping.
	db2, err := store.Open(ctx, env.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	mem2, err := NewMemory(db2, store.FixtureActor(), env.arts, env.auth,
		&MemoryOptions{Now: env.clock.now, LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]researchcontract.MemoryClaim, 2)
	errs := make([]error, 2)
	for i, m := range []*Memory{env.mem, mem2} {
		wg.Add(1)
		go func(i int, m *Memory) {
			defer wg.Done()
			<-start
			claim, err := m.Claim(ctx, "round-1", "attempt-1", 1, testDescriptor(), "key-race")
			results[i], errs[i] = claim, err
		}(i, m)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("claim errored: %v", err)
		}
	}
	// Both raced as the same owner: exactly one holds the lease and the
	// other replays/shares it — either way exactly one claim event lands.
	for _, r := range results {
		mustOutcome(t, r, researchcontract.OutcomeOK)
	}
	if results[0].Lease.LeaseID != results[1].Lease.LeaseID {
		t.Fatal("concurrent same-owner claims diverged")
	}
	var events []researchcontract.Event
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		events, _, err = store.ListRunEvents(ctx, r, "round-1", "", 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claims := 0
	for _, e := range events {
		if e.Kind == researchcontract.EventClaim {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("claim events = %d, want 1", claims)
	}
}

func TestConcurrentClaimsDifferentOwners(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	db2, err := store.Open(ctx, env.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	mem2, err := NewMemory(db2, store.FixtureActor(), env.arts, env.auth,
		&MemoryOptions{Now: env.clock.now, LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]researchcontract.MemoryClaim, 2)
	errs := make([]error, 2)
	owners := []string{"worker-a", "worker-b"}
	for i := range owners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := env.mem
			if i == 1 {
				m = mem2
			}
			<-start
			claim, err := m.Claim(ctx, "round-1", owners[i], 1, testDescriptor(), "key-"+owners[i])
			results[i], errs[i] = claim, err
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("claim errored: %v", err)
		}
	}
	wins, fenced := 0, 0
	for _, r := range results {
		switch r.Outcome {
		case researchcontract.OutcomeOK:
			wins++
		case researchcontract.OutcomeClaimedElsewhere:
			fenced++
		default:
			t.Fatalf("unexpected outcome %q", r.Outcome)
		}
	}
	if wins != 1 || fenced != 1 {
		t.Fatalf("wins=%d fenced=%d, want 1/1", wins, fenced)
	}
}

func TestObserveCommitsFreshAndReuse(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, err := desc.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	rec := testReceipt("rcpt-1", fp)
	out, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", RoundAttemptID: "attempt-1",
		ActualURLOrQuery: desc.URLOrQuery, Outcome: store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    rec, Capture: testCapture("<html>role</html>"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Late || out.ObservationID == "" || out.CaptureID == "" {
		t.Fatalf("bad observe output: %+v", out)
	}

	// Release-after-observe is an idempotent confirm.
	if err := env.mem.Release(ctx, claim.Lease.LeaseID, "attempt-1", 1, out.ObservationID); err != nil {
		t.Fatal(err)
	}

	var req store.ResearchRequest
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		req, err = store.GetResearchRequest(ctx, r, store.FixtureActorKind, store.FixtureActorID, fp)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if req.State != store.ResearchStateFresh || req.FreshUntil == "" || req.Lease != nil {
		t.Fatalf("bad committed row: %+v", req)
	}

	// The exact repeat reuses without a new lease (72h fetch TTL).
	lookup, err := env.mem.Lookup(ctx, "round-1", desc)
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, lookup, researchcontract.OutcomeReused)
	if len(lookup.Reusable) != 2 || lookup.Lease != nil {
		t.Fatalf("bad reuse: %+v", lookup)
	}
	repeat, err := env.mem.Claim(ctx, "round-1", "attempt-1", 1, desc, "key-2")
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, repeat, researchcontract.OutcomeReused)

	// Historical capture opens with no live lease.
	_, rc, err := env.caps.OpenCapture(ctx, out.CaptureID)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
}

func TestExpiredLeaseTakeoverAfterReconciliation(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	env.clock.advance(2 * time.Minute)

	// Prior attempt (reserved, non-terminal) with no reconciliation: no takeover.
	denied, err := env.mem.Claim(ctx, "round-1", "worker-b", 1, desc, "key-b")
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, denied, researchcontract.OutcomeClaimedElsewhere)

	env.auth.recon["attempt-1"] = researchcontract.Reconciliation{
		AttemptID: "attempt-1", State: "reconciled_uncertain", RecordedAt: env.clock.now()}
	taken, err := env.mem.Claim(ctx, "round-1", "worker-b", 1, desc, "key-b")
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, taken, researchcontract.OutcomeOK)
	if taken.Lease.LeaseID != claim.Lease.LeaseID || taken.Lease.Owner != "worker-b" {
		t.Fatalf("bad takeover lease: %+v", taken.Lease)
	}
	var events []researchcontract.Event
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		events, _, err = store.ListRunEvents(ctx, r, "round-1", "", 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == researchcontract.EventLeaseTakeover {
			found = true
		}
	}
	if !found {
		t.Fatal("missing lease_takeover event")
	}
}

func TestTakeoverViaLocalTerminalAttempt(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()

	claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	env.clock.advance(2 * time.Minute)
	// Mark the prior attempt terminal directly (test-only ledger edit).
	if err := env.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		_, err := db.ExecContext(ctx, `UPDATE round_attempts SET state='succeeded' WHERE id='attempt-1'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	taken, err := env.mem.Claim(ctx, "round-1", "worker-b", 1, testDescriptor(), "key-b")
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, taken, researchcontract.OutcomeOK)
}

func TestStaleWorkerFencedAndLate(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")

	// Wrong generation on renew/release fails fenced.
	if _, err := env.mem.Renew(ctx, claim.Lease.LeaseID, "attempt-1", 2); err == nil {
		t.Fatal("renew with stale generation succeeded")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeStale)
	}
	if err := env.mem.Release(ctx, claim.Lease.LeaseID, "intruder", 1, ""); err == nil {
		t.Fatal("release by foreign worker succeeded")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeStale)
	}

	// The stale worker's content lands as an immutable late observation.
	rec := testReceipt("rcpt-late", fp)
	out, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 2,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    rec, Capture: testCapture("<html>late</html>"),
	})
	mustContractCode(t, err, researchcontract.OutcomeStale)
	if !out.Late || out.ObservationID == "" {
		t.Fatalf("bad late output: %+v", out)
	}
	var obs store.ResearchObservation
	var req store.ResearchRequest
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		obs, err = store.GetResearchObservation(ctx, r, out.ObservationID)
		if err != nil {
			return err
		}
		req, err = store.GetResearchRequest(ctx, r, store.FixtureActorKind, store.FixtureActorID, fp)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !obs.IsLate || obs.Outcome != store.ObservationLate || obs.CaptureID == "" {
		t.Fatalf("bad late row: %+v", obs)
	}
	if req.State != store.ResearchStateClaimed || req.LatestObservationID != "" {
		t.Fatalf("late content moved request state: %+v", req)
	}

	// A late observation can never commit or release.
	if err := env.mem.Release(ctx, claim.Lease.LeaseID, "attempt-1", 1, out.ObservationID); err == nil {
		t.Fatal("late observation committed state")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeStale)
	}
}

func TestRenewAndAbandon(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	env.clock.advance(30 * time.Second)
	renewed, err := env.mem.Renew(ctx, claim.Lease.LeaseID, "attempt-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := env.clock.now().Add(time.Minute); !renewed.ExpiresAt.Equal(want) {
		t.Fatalf("renewed expiry = %v, want %v", renewed.ExpiresAt, want)
	}

	// Expired leases cannot be renewed.
	env.clock.advance(2 * time.Minute)
	if _, err := env.mem.Renew(ctx, claim.Lease.LeaseID, "attempt-1", 1); err == nil {
		t.Fatal("renew of expired lease succeeded")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeStale)
	}

	// The owner can still abandon its expired lease back to free.
	if err := env.mem.Release(ctx, claim.Lease.LeaseID, "attempt-1", 1, ""); err != nil {
		t.Fatal(err)
	}
	fp, _ := desc.Fingerprint()
	var req store.ResearchRequest
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		req, err = store.GetResearchRequest(ctx, r, store.FixtureActorKind, store.FixtureActorID, fp)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if req.State != store.ResearchStateFree {
		t.Fatalf("state = %q, want free", req.State)
	}
	again := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-3")
	mustOutcome(t, again, researchcontract.OutcomeOK)
}

func TestUncertainRefusesClaimAllowsJustifiedRefresh(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	rec := testReceipt("rcpt-u", fp)
	rec.Status = researchcontract.ReceiptUncertain
	if _, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationUncertain,
		Provenance: researchcontract.ProvenanceFetchedResponse, Receipt: rec,
	}); err != nil {
		t.Fatal(err)
	}
	// No auto-retry of an unknown outcome.
	retry, err := env.mem.Claim(ctx, "round-1", "attempt-1", 1, desc, "key-2")
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, retry, researchcontract.OutcomeUncertain)
	if len(retry.Failed) != 1 {
		t.Fatalf("uncertain must name its attempt: %+v", retry)
	}
	// Uncertain never expires.
	env.clock.advance(30 * 24 * time.Hour)
	still, err := env.mem.Lookup(ctx, "round-1", desc)
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, still, researchcontract.OutcomeUncertain)

	// A deliberate refresh with a reason re-opens the request.
	refreshed, err := env.mem.Refresh(ctx, "round-1", "attempt-1", 1, desc, store.RefreshReasonCoverageGap)
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, refreshed, researchcontract.OutcomeOK)
	if refreshed.RefreshFrom == "" {
		t.Fatal("refresh must link its prior observation")
	}
	if _, err := env.mem.Refresh(ctx, "round-1", "attempt-1", 1, desc, "curiosity"); err == nil {
		t.Fatal("unjustified refresh succeeded")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeInvalid)
	}
}

func TestNegativeCachingPerOutcome(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()

	observeNegative := func(desc researchcontract.RequestDescriptor, id, outcome, code string) {
		t.Helper()
		claim, err := env.mem.Claim(ctx, "round-1", "attempt-1", 1, desc, "key-"+id)
		if err != nil {
			t.Fatal(err)
		}
		mustOutcome(t, claim, researchcontract.OutcomeOK)
		fp, _ := desc.Fingerprint()
		rec := testReceipt("rcpt-"+id, fp)
		if outcome == store.ObservationFailed {
			rec.Status = researchcontract.ReceiptFailed
		}
		if _, err := env.mem.Observe(ctx, ObserveInput{
			LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
			RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
			Outcome: outcome, Provenance: researchcontract.ProvenanceFetchedResponse,
			Receipt: rec, ErrorCode: code,
		}); err != nil {
			t.Fatal(err)
		}
	}
	freshUntil := func(desc researchcontract.RequestDescriptor) (state, until string) {
		t.Helper()
		fp, _ := desc.Fingerprint()
		var req store.ResearchRequest
		if err := env.db.Read(ctx, func(r store.Reader) error {
			var err error
			req, err = store.GetResearchRequest(ctx, r, store.FixtureActorKind, store.FixtureActorID, fp)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return req.State, req.NegativeUntil
	}

	empty := testDescriptor()
	observeNegative(empty, "empty", store.ObservationEmpty, "")
	if state, until := freshUntil(empty); state != store.ResearchStateExhausted {
		t.Fatalf("empty state = %q", state)
	} else if until == "" {
		t.Fatal("empty must set a negative TTL")
	}
	lookup, err := env.mem.Lookup(ctx, "round-1", empty)
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, lookup, researchcontract.OutcomeReused)
	if len(lookup.Failed) != 1 || len(lookup.Reusable) != 0 {
		t.Fatalf("negative reuse must populate failed only: %+v", lookup)
	}

	// A 404 caches 24h, a generic failure 15m, blocked 30m.
	missing := testDescriptor()
	missing.URLOrQuery = "https://example.com/jobs/missing"
	observeNegative(missing, "404", store.ObservationFailed, ErrorCodeHTTP404)
	if _, until := freshUntil(missing); !withinTTL(t, until, env.clock.now(), 24*time.Hour) {
		t.Fatalf("404 negative_until = %q", until)
	}
	broken := testDescriptor()
	broken.URLOrQuery = "https://example.com/jobs/broken"
	observeNegative(broken, "fail", store.ObservationFailed, "http_500")
	if _, until := freshUntil(broken); !withinTTL(t, until, env.clock.now(), 15*time.Minute) {
		t.Fatalf("failed negative_until = %q", until)
	}
	blocked := testDescriptor()
	blocked.URLOrQuery = "https://example.com/jobs/blocked"
	observeNegative(blocked, "blocked", store.ObservationBlocked, "")
	if _, until := freshUntil(blocked); !withinTTL(t, until, env.clock.now(), 30*time.Minute) {
		t.Fatalf("blocked negative_until = %q", until)
	}

	// Past the empty TTL the request is claimable again.
	env.clock.advance(61 * time.Minute)
	retry := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-empty-2")
	mustOutcome(t, retry, researchcontract.OutcomeOK)
}

func withinTTL(t *testing.T, raw string, now time.Time, want time.Duration) bool {
	t.Helper()
	parsed, err := parseTime(raw)
	if err != nil {
		t.Fatalf("unparseable TTL %q: %v", raw, err)
	}
	return parsed.Equal(now.Add(want))
}

func TestRefreshFreshRequiresReason(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	if _, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    testReceipt("rcpt-1", fp), Capture: testCapture("<html>fresh</html>"),
	}); err != nil {
		t.Fatal(err)
	}
	plain, err := env.mem.Claim(ctx, "round-1", "attempt-1", 1, desc, "key-2")
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, plain, researchcontract.OutcomeReused)
	if plain.Lease != nil {
		t.Fatal("plain claim on fresh must not grant a lease")
	}
	justified, err := env.mem.Refresh(ctx, "round-1", "attempt-1", 1, desc, store.RefreshReasonStale)
	if err != nil {
		t.Fatal(err)
	}
	mustOutcome(t, justified, researchcontract.OutcomeOK)
	if justified.Lease == nil {
		t.Fatal("justified refresh must grant a lease")
	}
}

func TestTruncationExplicitness(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	// Incomplete capture without a note is rejected.
	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	truncated := testCapture("<html>part…</html>")
	truncated.Completeness = store.CaptureTruncated
	_, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    testReceipt("rcpt-t", fp), Capture: truncated,
	})
	mustContractCode(t, err, researchcontract.OutcomeInvalid)

	// A truncated receipt with a "complete" capture is rejected.
	rec := testReceipt("rcpt-t2", fp)
	rec.Truncated = true
	_, err = env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    rec, Capture: testCapture("<html>all</html>"),
	})
	mustContractCode(t, err, researchcontract.OutcomeInvalid)

	// Explicit truncation commits.
	rec2 := testReceipt("rcpt-t3", fp)
	rec2.Truncated = true
	out, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    rec2, Capture: truncated, TruncationNote: "first 16 bytes of 64",
	})
	if err != nil {
		t.Fatal(err)
	}
	var obs store.ResearchObservation
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		obs, err = store.GetResearchObservation(ctx, r, out.ObservationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if obs.TruncationNote == "" {
		t.Fatal("truncation note lost")
	}
}

func TestObserveValidation(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()
	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	base := ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    testReceipt("rcpt-v", fp), Capture: testCapture("<html>v</html>"),
	}
	invalid := researchcontract.OutcomeInvalid
	notFound := researchcontract.OutcomeNotFound
	cases := []struct {
		name   string
		want   researchcontract.Outcome
		mutate func(*ObserveInput)
	}{
		{"success needs capture", invalid, func(in *ObserveInput) { in.Capture = nil }},
		{"model note firewall", invalid, func(in *ObserveInput) {
			in.Provenance = researchcontract.ProvenanceModelNote
		}},
		{"receipt fingerprint mismatch", invalid, func(in *ObserveInput) {
			in.Receipt.Fingerprint = store.FixtureSHA256("other")
		}},
		{"failed receipt needs failed outcome", invalid, func(in *ObserveInput) {
			in.Receipt.Status = researchcontract.ReceiptFailed
		}},
		{"reused receipt records nothing", invalid, func(in *ObserveInput) {
			in.Receipt.Status = researchcontract.ReceiptReused
		}},
		{"server-assigned late outcome", invalid, func(in *ObserveInput) {
			in.Outcome = store.ObservationLate
		}},
		{"unknown outcome", invalid, func(in *ObserveInput) { in.Outcome = "maybe" }},
		{"unknown run", notFound, func(in *ObserveInput) { in.RoundID = "round-nope" }},
		{"unknown attempt", notFound, func(in *ObserveInput) { in.RoundAttemptID = "attempt-nope" }},
	}
	for i, tc := range cases {
		in := base
		tc.mutate(&in)
		// Unique receipt per case: pre-commit files persist past txn failure.
		in.Receipt.ID = "rcpt-v-" + string(rune('a'+i))
		if _, err := env.mem.Observe(ctx, in); err == nil {
			t.Errorf("%s: accepted", tc.name)
		} else {
			mustContractCode(t, err, tc.want)
		}
	}
	// Duplicate receipt ids conflict.
	if _, err := env.mem.Observe(ctx, base); err != nil {
		t.Fatal(err)
	}
	dup := base
	dup.Capture = testCapture("<html>v2</html>")
	if _, err := env.mem.Observe(ctx, dup); err == nil {
		t.Fatal("duplicate receipt accepted")
	} else {
		mustContractCode(t, err, researchcontract.OutcomeConflict)
	}
}

func TestExpireLeasesSweep(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()

	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	if _, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    testReceipt("rcpt-1", fp), Capture: testCapture("<html>e</html>"),
	}); err != nil {
		t.Fatal(err)
	}
	refreshed, err := env.mem.Refresh(ctx, "round-1", "attempt-1", 1, desc, store.RefreshReasonStale)
	if err != nil {
		t.Fatal(err)
	}
	env.clock.advance(2 * time.Minute)
	n, err := env.mem.ExpireLeases(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sweep journaled %d events, want 1", n)
	}
	// Idempotent: a second sweep journals nothing new.
	n, err = env.mem.ExpireLeases(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second sweep journaled %d events, want 0", n)
	}
	_ = refreshed
}

func TestPreRoleCaptureNeedsNoRecords(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()
	var companies, opps int
	if err := env.db.Read(ctx, func(r store.Reader) error {
		var err error
		if err = r.QueryRowContext(ctx, `SELECT COUNT(*) FROM companies`).Scan(&companies); err != nil {
			return err
		}
		return r.QueryRowContext(ctx, `SELECT COUNT(*) FROM opportunities`).Scan(&opps)
	}); err != nil {
		t.Fatal(err)
	}
	if companies != 0 || opps != 0 {
		t.Fatalf("fixture leaked records: %d companies, %d opps", companies, opps)
	}
	desc := testDescriptor()
	fp, _ := desc.Fingerprint()
	claim := claimAs(t, env.mem, "round-1", "attempt-1", 1, "key-1")
	out, err := env.mem.Observe(ctx, ObserveInput{
		LeaseID: claim.Lease.LeaseID, Owner: "attempt-1", Generation: 1,
		RoundID: "round-1", ActualURLOrQuery: desc.URLOrQuery,
		Outcome:    store.ObservationSuccess,
		Provenance: researchcontract.ProvenanceFetchedResponse,
		Receipt:    testReceipt("rcpt-pre", fp), Capture: testCapture("<html>pre-role</html>"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.CaptureID == "" {
		t.Fatal("pre-role capture not recorded")
	}
}

func TestNotesRoundTrip(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	ctx := context.Background()

	note, err := env.mem.AddNote(ctx, NoteInput{
		RoundID: "round-1", BriefProfileVersion: 1, BriefRubricVersion: "rubric-v1",
		Intent: "night-shift nursing roles near Ghent", Conclusion: "two promising boards",
	})
	if err != nil {
		t.Fatal(err)
	}
	if note.ID == "" {
		t.Fatal("note id missing")
	}
	found, err := env.mem.SearchNotes(ctx, "nursing Ghent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != note.ID {
		t.Fatalf("FTS found %d notes, want 1", len(found))
	}
	if _, err := env.mem.AddNote(ctx, NoteInput{RoundID: "round-1"}); err == nil {
		t.Fatal("empty intent accepted")
	}
}
