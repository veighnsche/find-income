package rounds

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var (
	testOwner = store.Actor{Kind: "administrator", ID: "owner"}
	testAgent = store.Actor{Kind: "agent", ID: "agent-1"}
)

func testResearchScope() store.RoundScope {
	return store.RoundScope{
		Operations: store.ResearchOperations(),
		Resources:  []string{store.ResearchAuthorityResource},
		Delegates:  []string{testAgent.ID},
	}
}

// openResearchRound starts and activates a running research round on a fresh
// t.TempDir database. One active round fits per database, so every test that
// needs a distinct round state opens its own.
func openResearchRound(t *testing.T, scope store.RoundScope, limits store.RoundAllowance) (*store.Store, store.Round) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := db.StartRound(ctx, testOwner, store.StartRoundInput{
		RequestKey: "research-commission", Intent: "Find sourced roles", Outcome: "research_run",
		ProfileVersion: p.Version, Scope: scope, Limits: limits, Deadline: time.Now().Add(time.Hour),
	})
	if err != nil || !created {
		t.Fatalf("start: %+v %v %v", r, created, err)
	}
	r, err = db.ActivateRound(ctx, testOwner, r.ID)
	if err != nil || r.State != store.RoundRunning {
		t.Fatalf("activate: %+v %v", r, err)
	}
	return db, r
}

func mustAuthority(t *testing.T, db *store.Store, actor store.Actor) *Authority {
	t.Helper()
	a, err := NewAuthority(db, actor)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func checkErr(t *testing.T, err error) *researchcontract.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected a contract error, got nil")
	}
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *researchcontract.Error, got %T (%v)", err, err)
	}
	return cerr
}

func TestNewAuthorityValidation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewAuthority(nil, testOwner); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := NewAuthority(db, store.Actor{}); err == nil {
		t.Fatal("empty actor accepted")
	}
	if _, err := NewAuthority(db, testAgent); err != nil {
		t.Fatalf("valid constructor: %v", err)
	}
}

func TestAuthorityCheckReportsWhichFailed(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 2, Tools: 2})
	owner := mustAuthority(t, db, testOwner)

	if err := owner.Check(ctx, researchcontract.CheckInput{RunID: r.ID, Generation: r.Generation,
		Permission: researchcontract.PermissionResearchDispatch, Now: time.Now()}); err != nil {
		t.Fatalf("valid check: %v", err)
	}

	if cerr := checkErr(t, owner.Check(ctx, researchcontract.CheckInput{RunID: "nope",
		Generation: 1, Permission: researchcontract.PermissionResearchDispatch})); cerr.Code != researchcontract.OutcomeNotFound || cerr.Field != "run" {
		t.Fatalf("run failure: %+v", cerr)
	}
	if cerr := checkErr(t, owner.Check(ctx, researchcontract.CheckInput{RunID: r.ID,
		Generation: r.Generation + 1, Permission: researchcontract.PermissionResearchDispatch})); cerr.Code != researchcontract.OutcomeStale || cerr.Field != "generation" {
		t.Fatalf("generation failure: %+v", cerr)
	}
	if cerr := checkErr(t, owner.Check(ctx, researchcontract.CheckInput{RunID: r.ID, Generation: r.Generation,
		Permission: researchcontract.PermissionResearchDispatch, Now: r.Deadline.Add(time.Hour)})); cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "deadline" {
		t.Fatalf("deadline failure: %+v", cerr)
	}
	if cerr := checkErr(t, owner.Check(ctx, researchcontract.CheckInput{RunID: r.ID, Generation: r.Generation,
		Permission: "bogus"})); cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "permission" {
		t.Fatalf("unknown permission: %+v", cerr)
	}

	agent := mustAuthority(t, db, testAgent)
	if err := agent.Check(ctx, researchcontract.CheckInput{RunID: r.ID, Generation: r.Generation,
		Permission: researchcontract.PermissionResearchDispatch}); err != nil {
		t.Fatalf("delegated agent dispatch: %v", err)
	}
	if cerr := checkErr(t, agent.Check(ctx, researchcontract.CheckInput{RunID: r.ID, Generation: r.Generation,
		Permission: researchcontract.PermissionRunControl})); cerr.Code != researchcontract.OutcomeForbidden {
		t.Fatalf("agent run.control: %+v", cerr)
	}
	if err := owner.Check(ctx, researchcontract.CheckInput{RunID: r.ID, Generation: r.Generation,
		Permission: researchcontract.PermissionRunControl}); err != nil {
		t.Fatalf("owner run.control: %v", err)
	}

	// Under-scoped round: owner passes the actor gate but the scope grants nothing.
	jevOnly := store.RoundScope{Operations: []string{store.RoundJevRequest}, Resources: []string{"jev"}}
	db2, r2 := openResearchRound(t, jevOnly, store.RoundAllowance{Requests: 2, Tools: 2})
	owner2 := mustAuthority(t, db2, testOwner)
	if cerr := checkErr(t, owner2.Check(ctx, researchcontract.CheckInput{RunID: r2.ID, Generation: r2.Generation,
		Permission: researchcontract.PermissionResearchDispatch})); cerr.Code != researchcontract.OutcomeForbidden || cerr.Field != "permission" {
		t.Fatalf("scope denial: %+v", cerr)
	}

	// Exhausted allowance names what remains (zeros) while run.control stays free.
	db3, r3 := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 1, Tools: 1})
	owner3 := mustAuthority(t, db3, testOwner)
	if _, err := owner3.Reserve(ctx, r3.ID, store.RoundResearchFetch, "only", "p1"); err != nil {
		t.Fatal(err)
	}
	cerr := checkErr(t, owner3.Check(ctx, researchcontract.CheckInput{RunID: r3.ID, Generation: r3.Generation,
		Permission: researchcontract.PermissionResearchDispatch}))
	if cerr.Code != researchcontract.OutcomeBudgetExhausted || cerr.Field != "allowance" ||
		!strings.Contains(cerr.Detail, "remaining requests=0") {
		t.Fatalf("allowance failure: %+v", cerr)
	}
	if err := owner3.Check(ctx, researchcontract.CheckInput{RunID: r3.ID, Generation: r3.Generation,
		Permission: researchcontract.PermissionRunControl}); err != nil {
		t.Fatalf("run.control on exhausted run: %v", err)
	}
}

func TestAuthorityCheckRunLiveness(t *testing.T) {
	ctx := context.Background()

	queuedDB, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer queuedDB.Close()
	p, err := queuedDB.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := queuedDB.StartRound(ctx, testOwner, store.StartRoundInput{
		RequestKey: "q", Intent: "i", Outcome: "research_run", ProfileVersion: p.Version,
		Scope: testResearchScope(), Limits: store.RoundAllowance{Requests: 1, Tools: 1},
		Deadline: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedAuth := mustAuthority(t, queuedDB, testOwner)
	if cerr := checkErr(t, queuedAuth.Check(ctx, researchcontract.CheckInput{RunID: queued.ID,
		Generation: queued.Generation, Permission: researchcontract.PermissionResearchDispatch})); cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "run" {
		t.Fatalf("queued round: %+v", cerr)
	}

	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 2, Tools: 2})
	auth := mustAuthority(t, db, testOwner)
	if _, _, err := db.StopRound(ctx, testOwner, r.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := db.PauseStoppedRound(ctx, testOwner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := checkErr(t, auth.Check(ctx, researchcontract.CheckInput{RunID: r.ID, Generation: paused.Generation,
		Permission: researchcontract.PermissionResearchDispatch})); cerr.Code != researchcontract.OutcomeStopped || cerr.Field != "run" {
		t.Fatalf("paused round: %+v", cerr)
	}

	db2, r2 := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 2, Tools: 2})
	auth2 := mustAuthority(t, db2, testOwner)
	if _, err := db2.FinishRound(ctx, testOwner, r2.ID, store.RoundCompleted, "done", "full", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if cerr := checkErr(t, auth2.Check(ctx, researchcontract.CheckInput{RunID: r2.ID, Generation: 99,
		Permission: researchcontract.PermissionResearchDispatch})); cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("generation still reported before terminal state: %+v", cerr)
	}
}

func TestAuthorityReserveReplayConflictAndValidation(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 4, Tools: 4})
	auth := mustAuthority(t, db, testOwner)

	first, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "key-1", "payload-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.AttemptID == "" || first.AttemptID != first.ID || first.RunID != r.ID ||
		first.Generation != r.Generation || first.IdempotencyKey != "key-1" || first.PayloadHash != "payload-a" ||
		first.Operation != store.RoundResearchFetch || first.ReservedAt.IsZero() {
		t.Fatalf("reservation shape: %+v", first)
	}
	replay, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "key-1", "payload-a")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("stable replay: %+v %v", replay, err)
	}
	other, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "key-2", "payload-a")
	if err != nil || other.ID == first.ID {
		t.Fatalf("same payload under a new key must be a new reservation: %+v %v", other, err)
	}
	if cerr := checkErr(t, func() error {
		_, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "key-1", "payload-b")
		return err
	}()); cerr.Code != researchcontract.OutcomeConflict {
		t.Fatalf("same key + different payload: %+v", cerr)
	}
	round, err := db.Round(ctx, r.ID)
	if err != nil || round.Used.Requests != 2 || round.Used.Tools != 2 {
		t.Fatalf("replay double-charged: %+v %v", round.Used, err)
	}

	for name, op := range map[string]string{"unknown": "bogus.op", "non-research": store.RoundFetchSource} {
		if cerr := checkErr(t, func() error {
			_, err := auth.Reserve(ctx, r.ID, op, "k-"+name, "p")
			return err
		}()); cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "operation" {
			t.Fatalf("%s operation: %+v", name, cerr)
		}
	}
	if _, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "", "p"); checkErr(t, err).Field != "idempotencyKey" {
		t.Fatalf("empty key: %v", err)
	}
	if _, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "k", ""); checkErr(t, err).Field != "payloadHash" {
		t.Fatalf("empty payload: %v", err)
	}
	if cerr := checkErr(t, func() error {
		_, err := auth.Reserve(ctx, "nope", store.RoundResearchFetch, "k", "p")
		return err
	}()); cerr.Code != researchcontract.OutcomeNotFound {
		t.Fatalf("unknown run: %+v", cerr)
	}
}

func TestAuthorityConcurrentReservationsCannotExceedAllowance(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 4, Tools: 4})
	auth := mustAuthority(t, db, testOwner)

	const racers = 12
	var wg sync.WaitGroup
	type outcome struct {
		id  string
		err error
	}
	outcomes := make(chan outcome, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := auth.Reserve(ctx, r.ID, store.RoundResearchSearch,
				"race-key-"+string(rune('a'+i)), "payload-hash")
			o := outcome{err: err}
			if err == nil {
				o.id = res.ID
			}
			outcomes <- o
		}(i)
	}
	wg.Wait()
	close(outcomes)

	succeeded := map[string]bool{}
	budgetFailures := 0
	for o := range outcomes {
		if o.err == nil {
			if succeeded[o.id] {
				t.Fatalf("duplicate reservation id %s", o.id)
			}
			succeeded[o.id] = true
			continue
		}
		var cerr *researchcontract.Error
		if !errors.As(o.err, &cerr) || cerr.Code != researchcontract.OutcomeBudgetExhausted {
			t.Fatalf("racer error is not budget_exhausted: %v", o.err)
		}
		budgetFailures++
	}
	if len(succeeded) != 4 || budgetFailures != racers-4 {
		t.Fatalf("allowance race: %d succeeded, %d exhausted (want 4/%d)", len(succeeded), budgetFailures, racers-4)
	}
	round, err := db.Round(ctx, r.ID)
	if err != nil || round.Used.Requests != 4 || round.Used.Tools != 4 {
		t.Fatalf("charged usage: %+v %v", round.Used, err)
	}
}

func TestAuthorityReplayStableAcrossCredentialRotation(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 4, Tools: 4})
	auth := mustAuthority(t, db, testOwner)

	first, err := auth.Reserve(ctx, r.ID, store.RoundResearchAPI, "cred-key", "payload")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			auth.RotateCredentials(r.ID)
		}()
	}
	wg.Wait()
	if v := auth.CredentialVersion(r.ID); v != 8 {
		t.Fatalf("credential version: %d", v)
	}
	replay, err := auth.Reserve(ctx, r.ID, store.RoundResearchAPI, "cred-key", "payload")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay after rotation: %+v %v", replay, err)
	}
	round, err := db.Round(ctx, r.ID)
	if err != nil || round.Used.Requests != 1 {
		t.Fatalf("rotation changed charging: %+v %v", round.Used, err)
	}
	if _, err := auth.Reserve(ctx, r.ID, store.RoundResearchAPI, "post-rotation", "payload"); err != nil {
		t.Fatalf("reserve after rotation: %v", err)
	}
}

func TestAuthorityStopFencesDispatchViaGeneration(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 4, Tools: 4})
	auth := mustAuthority(t, db, testOwner)

	held, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "held", "p")
	if err != nil {
		t.Fatal(err)
	}
	stopping, _, err := db.StopRound(ctx, testOwner, r.ID)
	if err != nil || stopping.Generation != r.Generation+1 {
		t.Fatalf("stop rotates generation: %+v %v", stopping, err)
	}
	if cerr := checkErr(t, func() error {
		_, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "after-stop", "p")
		return err
	}()); cerr.Code != researchcontract.OutcomeStopped {
		t.Fatalf("reserve after stop: %+v", cerr)
	}
	if _, err := db.MarkRoundDispatched(ctx, r.ID, held.AttemptID); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("pre-stop reservation dispatched after rotation: %v", err)
	}
	paused, err := db.PauseStoppedRound(ctx, testOwner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := db.ResumeRound(ctx, testOwner, r.ID, paused.Generation)
	if err != nil || resumed.State != store.RoundRunning {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if _, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "after-resume", "p"); err != nil {
		t.Fatalf("reserve after resume: %v", err)
	}
	if _, err := db.MarkRoundDispatched(ctx, r.ID, held.AttemptID); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stale-generation reservation dispatched: %v", err)
	}
}

func TestAuthorityReleaseRefundsAndPinsStates(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 4, Tools: 4})
	auth := mustAuthority(t, db, testOwner)

	held, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "drop", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Release(ctx, held.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	round, err := db.Round(ctx, r.ID)
	if err != nil || round.Used.Requests != 0 || round.Used.Tools != 0 {
		t.Fatalf("release did not refund: %+v %v", round.Used, err)
	}
	attempt, err := db.RoundAttempt(ctx, held.ID)
	if err != nil || attempt.State != store.AttemptCancelled {
		t.Fatalf("released state: %+v %v", attempt, err)
	}
	if cerr := checkErr(t, auth.Release(ctx, held.ID)); cerr.Code != researchcontract.OutcomeInvalid {
		t.Fatalf("double release: %+v", cerr)
	}
	if cerr := checkErr(t, auth.Release(ctx, "nope")); cerr.Code != researchcontract.OutcomeNotFound {
		t.Fatalf("unknown release: %+v", cerr)
	}

	dispatched, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "keep", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, r.ID, dispatched.ID); err != nil {
		t.Fatal(err)
	}
	if cerr := checkErr(t, auth.Release(ctx, dispatched.ID)); cerr.Code != researchcontract.OutcomeInvalid ||
		!strings.Contains(cerr.Detail, "dispatched") {
		t.Fatalf("release after dispatch: %+v", cerr)
	}

	// Cleanup after Stop still refunds: release unwinds a hold, never new work.
	db2, r2 := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 2, Tools: 2})
	auth2 := mustAuthority(t, db2, testOwner)
	stale, err := auth2.Reserve(ctx, r2.ID, store.RoundResearchFetch, "stale", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db2.StopRound(ctx, testOwner, r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db2.PauseStoppedRound(ctx, testOwner, r2.ID); err != nil {
		t.Fatal(err)
	}
	if err := auth2.Release(ctx, stale.ID); err != nil {
		t.Fatalf("release after stop: %v", err)
	}
	round2, err := db2.Round(ctx, r2.ID)
	if err != nil || round2.Used.Requests != 0 {
		t.Fatalf("post-stop refund: %+v %v", round2.Used, err)
	}
}

func TestAuthorityUsageLedger(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 10, Items: 10, Tools: 10, Turns: 2})
	auth := mustAuthority(t, db, testOwner)

	ledger, err := auth.Usage(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Enforced.Requests != 10 || ledger.Enforced.Turns != 2 ||
		ledger.Reserved.Requests != 0 || ledger.Observed.Requests != 0 || ledger.Unknown {
		t.Fatalf("fresh ledger: %+v", ledger)
	}

	done, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "done", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, r.ID, done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, testOwner, r.ID, done.ID, true, json.RawMessage(`{"ok":true}`), ""); err != nil {
		t.Fatal(err)
	}
	ledger, err = auth.Usage(ctx, r.ID)
	if err != nil || ledger.Reserved.Requests != 0 || ledger.Observed.Requests != 1 ||
		ledger.Observed.Tools != 1 || ledger.Unknown {
		t.Fatalf("observed ledger: %+v %v", ledger, err)
	}

	live, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "live", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, r.ID, live.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.StopRound(ctx, testOwner, r.ID); err != nil {
		t.Fatal(err)
	}
	ledger, err = auth.Usage(ctx, r.ID)
	if err != nil || ledger.Reserved.Requests != 1 || ledger.Reserved.Tools != 1 ||
		ledger.Observed.Requests != 1 || !ledger.Unknown {
		t.Fatalf("uncertain ledger: %+v %v", ledger, err)
	}
	if _, err := auth.Usage(ctx, "nope"); checkErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("unknown run usage: %v", err)
	}
}

func TestAuthorityReconciliationForTakeover(t *testing.T) {
	ctx := context.Background()

	// Terminal attempts report their terminal state.
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 4, Tools: 4})
	auth := mustAuthority(t, db, testOwner)
	okRes, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "ok", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkRoundDispatched(ctx, r.ID, okRes.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRoundAttempt(ctx, testOwner, r.ID, okRes.ID, true, json.RawMessage(`{"ok":true}`), ""); err != nil {
		t.Fatal(err)
	}
	rec, err := auth.ReconciliationFor(ctx, okRes.ID)
	if err != nil || rec.State != string(store.AttemptSucceeded) || rec.RecordedAt.IsZero() {
		t.Fatalf("terminal reconciliation: %+v %v", rec, err)
	}
	pending, err := auth.Reserve(ctx, r.ID, store.RoundResearchFetch, "pending", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ReconciliationFor(ctx, pending.ID); checkErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("unsettled attempt: %v", err)
	}

	// Expired-lease shape: uncertain attempt + unknown check = C takeover input.
	db2, r2 := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 6, Tools: 6})
	auth2 := mustAuthority(t, db2, testOwner)
	lost, err := auth2.Reserve(ctx, r2.ID, store.RoundResearchFetch, "lost", "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db2.MarkRoundDispatched(ctx, r2.ID, lost.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db2.StopRound(ctx, testOwner, r2.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := db2.PauseStoppedRound(ctx, testOwner, r2.ID)
	if err != nil {
		t.Fatal(err)
	}
	check, err := db2.BeginRoundReconciliation(ctx, r2.ID, lost.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth2.ReconciliationFor(ctx, lost.ID); checkErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("pending check is not a settlement: %v", err)
	}
	if _, err := db2.FinishRoundReconciliation(ctx, check, store.AttemptUncertain, json.RawMessage(`{"code":"check_failed"}`)); err != nil {
		t.Fatal(err)
	}
	rec, err = auth2.ReconciliationFor(ctx, lost.ID)
	if err != nil || rec.State != store.ReconciledUncertain || rec.RecordedAt.IsZero() {
		t.Fatalf("expired-lease reconciliation record: %+v %v", rec, err)
	}
	if _, err := auth2.ReconciliationFor(ctx, "nope"); checkErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("unknown attempt: %v", err)
	}
}

// TestAuthorityCheckInsideWriteTransaction pins the T18 pattern: D calls the
// check inside its own write transaction, so authority and business writes
// commit or roll back together.
func TestAuthorityCheckInsideWriteTransaction(t *testing.T) {
	ctx := context.Background()
	db, r := openResearchRound(t, testResearchScope(), store.RoundAllowance{Requests: 2, Tools: 2})

	committed := false
	err := db.ResearchWrite(ctx, func(tx store.ResearchDB) error {
		remaining, err := store.CheckRoundAuthorityTx(ctx, tx, r.ID, r.Generation,
			researchcontract.PermissionResearchDispatch, testOwner, time.Now())
		if err != nil {
			return err
		}
		if remaining.Requests != 2 || remaining.Tools != 2 {
			t.Fatalf("remaining in txn: %+v", remaining)
		}
		committed = true
		return store.AppendRunEvent(ctx, tx, researchcontract.Event{
			ID: "evt-1", RunID: r.ID, Kind: researchcontract.EventClaim,
			ObservedAt: time.Now(), RecordedAt: time.Now(),
		})
	})
	if err != nil || !committed {
		t.Fatalf("check-then-write txn: %v", err)
	}

	rolledBack := false
	err = db.ResearchWrite(ctx, func(tx store.ResearchDB) error {
		if err := store.AppendRunEvent(ctx, tx, researchcontract.Event{
			ID: "evt-2", RunID: r.ID, Kind: researchcontract.EventClaim,
			ObservedAt: time.Now(), RecordedAt: time.Now(),
		}); err != nil {
			return err
		}
		rolledBack = true
		_, checkErr := store.CheckRoundAuthorityTx(ctx, tx, r.ID, r.Generation+99,
			researchcontract.PermissionResearchDispatch, testOwner, time.Now())
		return checkErr // stale generation aborts the whole transaction
	})
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) || cerr.Code != researchcontract.OutcomeStale || !rolledBack {
		t.Fatalf("failing check must abort txn: %v", err)
	}

	var events []researchcontract.Event
	if err := db.Read(ctx, func(rd store.Reader) error {
		var err error
		events, _, err = store.ListRunEvents(ctx, rd, r.ID, "", 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "evt-1" {
		t.Fatalf("txn journal: %+v", events)
	}
}
