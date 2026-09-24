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
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// fakeExecutor is the T23-bind research executor double: scripted outcomes,
// recorded inputs, high-water concurrency tracking.
type fakeExecutor struct {
	mu        sync.Mutex
	run       func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error)
	calls     []researchcontract.ExecuteInput
	inFlight  int
	highWater int
}

func (f *fakeExecutor) Execute(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.inFlight++
	if f.inFlight > f.highWater {
		f.highWater = f.inFlight
	}
	run := f.run
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if run == nil {
		return okExecuteOutput(in), nil
	}
	return run(ctx, in)
}

func okExecuteOutput(in researchcontract.ExecuteInput) researchcontract.ExecuteOutput {
	key := in.IdempotencyKey
	return researchcontract.ExecuteOutput{
		Outcome:       researchcontract.OutcomeOK,
		ObservationID: "obs-" + key,
		CaptureID:     "cap-" + key,
		Receipt: researchcontract.ExecutionReceipt{
			ID: "rcpt-" + key, Operation: researchcontract.OperationFetch,
			Status: "ok", CaptureID: "cap-" + key, Attempts: 1,
			BytesIn: 100, BytesOut: 800,
			StartedAt: time.Now(), EndedAt: time.Now(),
		},
		Usage: researchcontract.ExecuteUsage{Requests: 1, Bytes: 900},
	}
}

func (f *fakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeTurns is the T17-bind turn runner double.
type fakeTurns struct {
	mu    sync.Mutex
	run   func(ctx context.Context, agent store.Actor, in RunnerTurnInput) (RunnerTurnOutput, error)
	calls []RunnerTurnInput
}

func (f *fakeTurns) RunTurn(ctx context.Context, agent store.Actor, in RunnerTurnInput) (RunnerTurnOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	run := f.run
	f.mu.Unlock()
	if run == nil {
		thread := in.ThreadID
		if thread == "" {
			thread = "thread-" + in.RequestKey
		}
		return RunnerTurnOutput{ThreadID: thread, TurnID: "turn-" + in.RequestKey,
			Status: "completed", NextWork: []string{"follow-" + in.RequestKey}}, nil
	}
	return run(ctx, agent, in)
}

// fakeConversation scripts SteerAttempt outcomes.
type fakeConversation struct {
	mu  sync.Mutex
	run func(ctx context.Context, roundID, attemptID, text string) (string, error)
}

func (f *fakeConversation) SteerAttempt(ctx context.Context, roundID, attemptID, text string) (string, error) {
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return "turn-live", nil
	}
	return run(ctx, roundID, attemptID, text)
}

// fakeObserver scripts reconciliation reads.
type fakeObserver struct {
	mu  sync.Mutex
	run func(ctx context.Context, attemptID string) (Observation, error)
}

func (f *fakeObserver) ObserveDispatch(ctx context.Context, attemptID string) (Observation, error) {
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return Observation{State: store.AttemptObservedSuccess,
			Evidence: json.RawMessage(`{"observed":true}`)}, nil
	}
	return run(ctx, attemptID)
}

type supervisorHarness struct {
	db      *store.Store
	dir     string
	auth    *Authority
	journal *store.RunEventJournal
	exec    *fakeExecutor
	turns   *fakeTurns
	conv    *fakeConversation
	obs     *fakeObserver
	sup     *Supervisor
}

func newSupervisorHarness(t *testing.T, cfg SupervisorConfig) *supervisorHarness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	auth, err := NewAuthority(db, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.NewRunEventJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	h := &supervisorHarness{db: db, dir: dir, auth: auth, journal: journal,
		exec: &fakeExecutor{}, turns: &fakeTurns{}, conv: &fakeConversation{}, obs: &fakeObserver{}}
	sup, err := NewSupervisor(SupervisorDeps{
		DB: db, Authority: auth, Executor: h.exec, Journal: journal,
		Turns: h.turns, Conversation: h.conv, Observer: h.obs, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.sup = sup
	t.Cleanup(sup.Close)
	return h
}

func (h *supervisorHarness) commission(t *testing.T, brief, key string) CommissionOutput {
	t.Helper()
	out, err := h.sup.Commission(context.Background(), CommissionInput{
		Actor: testOwner, BriefText: brief, AgentID: testAgent.ID,
		RubricVersion: "rubric-v1", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created {
		t.Fatal("expected a fresh commission")
	}
	return out
}

func testDescriptor(query string) researchcontract.RequestDescriptor {
	return researchcontract.RequestDescriptor{
		Operation: researchcontract.OperationFetch, Backend: "test-backend", URLOrQuery: query,
	}
}

func contractErr(t *testing.T, err error) *researchcontract.Error {
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

func journalKinds(t *testing.T, h *supervisorHarness, runID string) []string {
	t.Helper()
	var kinds []string
	cursor := ""
	for {
		events, next, err := h.journal.List(context.Background(), runID, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			kinds = append(kinds, e.Kind)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return kinds
}

func countKind(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}

func TestNewSupervisorValidation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth, err := NewAuthority(db, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.NewRunEventJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	full := SupervisorDeps{DB: db, Authority: auth, Journal: journal}
	if _, err := NewSupervisor(full); err != nil {
		t.Fatalf("minimal deps: %v", err)
	}
	for name, mutate := range map[string]func(*SupervisorDeps){
		"nil store":     func(d *SupervisorDeps) { d.DB = nil },
		"nil authority": func(d *SupervisorDeps) { d.Authority = nil },
		"nil journal":   func(d *SupervisorDeps) { d.Journal = nil },
		"bad maxConc":   func(d *SupervisorDeps) { d.Config.MaxConcurrent = 9 },
	} {
		deps := full
		mutate(&deps)
		if _, err := NewSupervisor(deps); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSupervisorCommissionDefaults(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})

	out, err := h.sup.Commission(ctx, CommissionInput{
		Actor: testOwner, BriefText: "Find sourced engineering roles in Amsterdam.",
		AgentID: testAgent.ID, RubricVersion: "rubric-v1", IdempotencyKey: "run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.RunID == "" || out.Generation != 1 || out.State != "running" {
		t.Fatalf("commission output: %+v", out)
	}
	// Canary defaults: 15min, 60 actions, 12 Jev, 8 turns, 2 concurrent.
	if out.Limits.Requests != 72 || out.Limits.Items != 60 || out.Limits.Tools != 68 || out.Limits.Turns != 8 {
		t.Fatalf("default limits: %+v", out.Limits)
	}
	if time.Until(out.Deadline) < 14*time.Minute || out.MaxConcurrent != 2 {
		t.Fatalf("default deadline/concurrency: %+v", out)
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range store.ResearchOperations() {
		found := false
		for _, scoped := range round.Scope.Operations {
			found = found || scoped == op
		}
		if !found {
			t.Fatalf("scope misses %s: %v", op, round.Scope.Operations)
		}
	}
	if len(round.Scope.Delegates) != 1 || round.Scope.Delegates[0] != testAgent.ID {
		t.Fatalf("delegates: %v", round.Scope.Delegates)
	}

	// Durable commission record + initial checkpoint (RemainingAllowance is
	// the UsageLedger JSON per the T12 convention).
	rec, err := h.sup.commissionRecord(ctx, out.RunID)
	if err != nil || rec.Brief != "Find sourced engineering roles in Amsterdam." ||
		rec.RubricVersion != "rubric-v1" || rec.AgentID != testAgent.ID || rec.MaxConcurrent != 2 {
		t.Fatalf("commission record: %+v %v", rec, err)
	}
	cp, err := h.sup.Checkpoint(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.ProfileVersion < 1 || cp.RubricVersion != "rubric-v1" || cp.Generation != 1 {
		t.Fatalf("initial checkpoint: %+v", cp)
	}
	var ledger researchcontract.UsageLedger
	if err := json.Unmarshal(cp.RemainingAllowance, &ledger); err != nil ||
		ledger.Enforced.Requests != 72 || ledger.Unknown {
		t.Fatalf("remaining allowance ledger: %+v %v", ledger, err)
	}

	// Exact replay: same run, no duplicated init state.
	replay, err := h.sup.Commission(ctx, CommissionInput{
		Actor: testOwner, BriefText: "Find sourced engineering roles in Amsterdam.",
		AgentID: testAgent.ID, RubricVersion: "rubric-v1", IdempotencyKey: "run-1",
	})
	if err != nil || replay.Created || replay.RunID != out.RunID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if kinds := journalKinds(t, h, out.RunID); countKind(kinds, SuperviseEventCommissioned) != 1 {
		t.Fatalf("commissioned journaled %d times: %v", countKind(kinds, SuperviseEventCommissioned), kinds)
	}

	// Same key + different brief conflicts (round digest covers intent).
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Commission(ctx, CommissionInput{
			Actor: testOwner, BriefText: "Something else entirely.",
			AgentID: testAgent.ID, RubricVersion: "rubric-v1", IdempotencyKey: "run-1",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeConflict {
		t.Fatalf("conflicting replay: %+v", cerr)
	}

	// Second commission hits the single active slot.
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Commission(ctx, CommissionInput{
			Actor: testOwner, BriefText: "Another run.", AgentID: testAgent.ID,
			RubricVersion: "rubric-v1", IdempotencyKey: "run-2",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeConflict {
		t.Fatalf("second active run: %+v", cerr)
	}
}

func TestSupervisorCommissionValidation(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	base := CommissionInput{
		Actor: testOwner, BriefText: "brief", AgentID: testAgent.ID,
		RubricVersion: "rubric-v1", IdempotencyKey: "valid-1",
	}
	cases := map[string]func(*CommissionInput){
		"non-owner":    func(in *CommissionInput) { in.Actor = testAgent },
		"long brief":   func(in *CommissionInput) { in.BriefText = strings.Repeat("b", maxBriefText+1) },
		"empty agent":  func(in *CommissionInput) { in.AgentID = "" },
		"empty rubric": func(in *CommissionInput) { in.RubricVersion = "" },
		"empty key":    func(in *CommissionInput) { in.IdempotencyKey = "" },
		"spaced key":   func(in *CommissionInput) { in.IdempotencyKey = " key " },
		"bad allowance": func(in *CommissionInput) {
			in.Allowance = &AllowanceInput{TimeMs: 1000, MaxActions: 0, MaxTurns: 1, MaxConcurrent: 1}
		},
		"bad concurrency": func(in *CommissionInput) {
			in.Allowance = &AllowanceInput{TimeMs: 1000, MaxActions: 1, MaxTurns: 1, MaxConcurrent: 9}
		},
	}
	for name, mutate := range cases {
		in := base
		in.IdempotencyKey = "valid-" + strings.ReplaceAll(name, " ", "-")
		mutate(&in)
		if cerr := contractErr(t, func() error {
			_, err := h.sup.Commission(ctx, in)
			return err
		}()); cerr.Code != researchcontract.OutcomeInvalid && cerr.Code != researchcontract.OutcomeForbidden {
			t.Fatalf("%s: %+v", name, cerr)
		}
	}
	if _, err := h.db.ActiveRound(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invalid commissions created a run: %v", err)
	}
}

// TestSupervisorCommissionScopeGuardsDrift pins the T12 permission grants on a
// commissioned run: if the authority grant set moves without the commission
// scope, this fails and names the drift.
func TestSupervisorCommissionScopeGuardsDrift(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "scope check", "scope-1")

	agentAuth, err := NewAuthority(h.db, testAgent)
	if err != nil {
		t.Fatal(err)
	}
	for _, perm := range []researchcontract.Permission{
		researchcontract.PermissionResearchDispatch,
		researchcontract.PermissionRecordWrite,
		researchcontract.PermissionJevRequest,
	} {
		if err := agentAuth.Check(ctx, researchcontract.CheckInput{RunID: out.RunID,
			Generation: out.Generation, Permission: perm}); err != nil {
			t.Fatalf("agent %s on commissioned run: %v", perm, err)
		}
	}
	if err := h.auth.Check(ctx, researchcontract.CheckInput{RunID: out.RunID,
		Generation: out.Generation, Permission: researchcontract.PermissionRunControl}); err != nil {
		t.Fatalf("owner run.control: %v", err)
	}
}

func TestSupervisorDispatchFlow(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "dispatch flow", "dispatch-1")

	got, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/roles"), IdempotencyKey: "op-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != researchcontract.OutcomeOK || got.ObservationID != "obs-op-1" ||
		got.CaptureID != "cap-op-1" || got.AttemptID == "" || got.Replayed {
		t.Fatalf("dispatch output: %+v", got)
	}
	if h.exec.calls[0].Generation != out.Generation || h.exec.calls[0].RunID != out.RunID {
		t.Fatalf("executor scoping: %+v", h.exec.calls[0])
	}

	attempt, err := h.db.RoundAttempt(ctx, got.AttemptID)
	if err != nil || attempt.State != store.AttemptSucceeded {
		t.Fatalf("attempt state: %+v %v", attempt, err)
	}
	results, err := h.db.RoundResults(ctx, out.RunID)
	if err != nil || len(results) != 1 {
		t.Fatalf("results: %d %v", len(results), err)
	}
	kinds := journalKinds(t, h, out.RunID)
	if countKind(kinds, SuperviseEventDispatched) != 1 ||
		countKind(kinds, SuperviseEventObserved) != 1 {
		t.Fatalf("dispatch journal: %v", kinds)
	}
	cp, err := h.sup.Checkpoint(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.EvidenceIDs) != 2 || cp.EvidenceIDs[0] != "obs-op-1" || cp.EvidenceIDs[1] != "cap-op-1" {
		t.Fatalf("checkpoint evidence: %+v", cp.EvidenceIDs)
	}

	// Exact replay serves the stored result without re-executing.
	replay, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/roles"), IdempotencyKey: "op-1",
	})
	if err != nil || !replay.Replayed || replay.AttemptID != got.AttemptID ||
		replay.ObservationID != "obs-op-1" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if h.exec.callCount() != 1 {
		t.Fatalf("replay re-executed: %d calls", h.exec.callCount())
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.Used.Requests != 1 || round.Used.Tools != 1 {
		t.Fatalf("replay double-charged: %+v %v", round.Used, err)
	}

	// Same key + different payload conflicts without charging.
	roundBefore := round.Used
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/other"), IdempotencyKey: "op-1",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeConflict {
		t.Fatalf("changed payload: %+v", cerr)
	}
	round, _ = h.db.Round(ctx, out.RunID)
	if round.Used != roundBefore {
		t.Fatalf("conflict charged: %+v", round.Used)
	}

	// Secret-bearing descriptors are rejected before any charge.
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteAPI,
			Request: researchcontract.RequestDescriptor{
				Operation: researchcontract.OperationAPI, Backend: "b",
				URLOrQuery: "https://example.com/api",
				Params:     []researchcontract.Param{{Name: "api_key", Value: "s3cr3t"}},
			},
			IdempotencyKey: "op-secret",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "request" {
		t.Fatalf("secret descriptor: %+v", cerr)
	}
	round, _ = h.db.Round(ctx, out.RunID)
	if round.Used != roundBefore {
		t.Fatalf("invalid descriptor charged: %+v", round.Used)
	}
}

func TestSupervisorMultiTurnContinuation(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "Find two engineering roles.", "turns-1")

	// A steer queued before the first turn must reach the turn brief.
	steer, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Prefer roles with Rust.", IdempotencyKey: "steer-1",
	})
	if err != nil || steer.State != SteerQueued || steer.Revision != 1 {
		t.Fatalf("pre-turn steer: %+v %v", steer, err)
	}

	first, err := h.sup.ContinueRun(ctx, testAgent, TurnInput{RunID: out.RunID, RequestKey: "turn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "completed" || first.ThreadID == "" || first.TurnID == "" {
		t.Fatalf("first turn: %+v", first)
	}
	if len(h.turns.calls) != 1 || h.turns.calls[0].ThreadID != "" {
		t.Fatalf("first turn must open a fresh thread: %+v", h.turns.calls)
	}
	if !strings.Contains(first.BriefUsed, "Find two engineering roles.") ||
		!strings.Contains(first.BriefUsed, "Prefer roles with Rust.") {
		t.Fatalf("first brief misses durable context: %q", first.BriefUsed)
	}

	second, err := h.sup.ContinueRun(ctx, testAgent, TurnInput{RunID: out.RunID, RequestKey: "turn-2"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ThreadID != first.ThreadID {
		t.Fatalf("second turn did not resume the stored thread: %+v", second)
	}
	if len(h.turns.calls) != 2 || h.turns.calls[1].ThreadID != first.ThreadID {
		t.Fatalf("runner did not see the stored thread: %+v", h.turns.calls)
	}
	// The model's NextWork proposal from turn 1 reaches turn 2 via the
	// checkpoint, owned by the supervisor.
	if !strings.Contains(second.BriefUsed, "follow-turn-1") {
		t.Fatalf("second brief misses turn-1 next work: %q", second.BriefUsed)
	}

	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.Used.Turns != 2 || round.Used.Tools != 2 {
		t.Fatalf("turn charging: %+v %v", round.Used, err)
	}
	cp, err := h.sup.Checkpoint(ctx, out.RunID)
	if err != nil || len(cp.NextWork) != 1 || cp.NextWork[0] != "follow-turn-2" {
		t.Fatalf("checkpoint next work: %+v %v", cp.NextWork, err)
	}
	kinds := journalKinds(t, h, out.RunID)
	if countKind(kinds, "run.turn_observed") != 2 || countKind(kinds, SuperviseEventCheckpointed) != 2 {
		t.Fatalf("turn journal: %v", kinds)
	}

	// Exact turn replay serves the stored result.
	replay, err := h.sup.ContinueRun(ctx, testAgent, TurnInput{RunID: out.RunID, RequestKey: "turn-1"})
	if err != nil || !replay.Replayed || replay.ThreadID != first.ThreadID {
		t.Fatalf("turn replay: %+v %v", replay, err)
	}
	if len(h.turns.calls) != 2 {
		t.Fatalf("turn replay re-ran: %d calls", len(h.turns.calls))
	}
}

func TestSupervisorCrashRecoveryResume(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "crash recovery", "crash-1")

	settled, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteSearch,
		Request: testDescriptor("engineer roles"), IdempotencyKey: "op-settled",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A second dispatch is in flight when the process dies: block the
	// executor, then close the store without settling anything.
	entered := make(chan struct{})
	var closeOnce sync.Once
	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		closeOnce.Do(func() { close(entered) })
		<-ctx.Done()
		return researchcontract.ExecuteOutput{}, ctx.Err()
	}
	dispatchErr := make(chan error, 1)
	go func() {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/crash"), IdempotencyKey: "op-doomed",
		})
		dispatchErr <- err
	}()
	<-entered
	h.db.Close()  // crash: no settle, no in-memory state survives
	h.sup.Close() // test-only: drop the orphaned execution context
	select {
	case <-dispatchErr:
	case <-time.After(5 * time.Second):
		t.Fatal("orphaned dispatch never settled")
	}

	// A fresh supervisor on the reopened store: recovery paused the run,
	// fenced the generation and marked the in-flight attempt uncertain.
	db2, err := store.Open(ctx, h.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	auth2, err := NewAuthority(db2, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	journal2, err := store.NewRunEventJournal(db2)
	if err != nil {
		t.Fatal(err)
	}
	exec2, turns2, conv2, obs2 := &fakeExecutor{}, &fakeTurns{}, &fakeConversation{}, &fakeObserver{}
	sup2, err := NewSupervisor(SupervisorDeps{
		DB: db2, Authority: auth2, Executor: exec2, Journal: journal2,
		Turns: turns2, Conversation: conv2, Observer: obs2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sup2.Close()

	round, err := db2.Round(ctx, out.RunID)
	if err != nil || round.State != store.RoundPaused || round.Generation != out.Generation+1 {
		t.Fatalf("recovered round: %+v %v", round, err)
	}
	limitsBefore := round.Limits
	usedBefore := round.Used

	resume, err := sup2.Resume(ctx, testOwner, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if resume.Generation != out.Generation+2 || resume.Reconciled != 1 || len(resume.Unknown) != 0 {
		t.Fatalf("resume output: %+v", resume)
	}
	round, err = db2.Round(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Remaining allowance, never a fresh budget: limits identical, used
	// preserved plus the one reconciliation check charge.
	if round.Limits != limitsBefore {
		t.Fatalf("resume touched limits: %+v vs %+v", round.Limits, limitsBefore)
	}
	if round.Used.Requests != usedBefore.Requests+1 || round.Used.Tools != usedBefore.Tools+1 {
		t.Fatalf("used after resume: %+v (was %+v)", round.Used, usedBefore)
	}
	if resume.Remaining.Requests != limitsBefore.Requests-round.Used.Requests {
		t.Fatalf("remaining misreported: %+v", resume.Remaining)
	}

	// The checkpoint survived with its evidence and current generation.
	cp, err := sup2.Checkpoint(ctx, out.RunID)
	if err != nil || cp.Generation != resume.Generation || len(cp.EvidenceIDs) != 2 ||
		len(cp.UnresolvedAttempts) != 0 {
		t.Fatalf("restored checkpoint: %+v %v", cp, err)
	}
	// The settled attempt kept its result; the doomed one reconciled.
	if _, err := db2.RoundAttempt(ctx, settled.AttemptID); err != nil {
		t.Fatal(err)
	}
	kinds := journalKinds(t, &supervisorHarness{journal: journal2}, out.RunID)
	if countKind(kinds, SuperviseEventCommissioned) != 1 || countKind(kinds, "run.resumed") != 1 {
		t.Fatalf("journal after recovery: %v", kinds)
	}

	// Work continues on the remainder.
	after, err := sup2.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/after"), IdempotencyKey: "op-after",
	})
	if err != nil || after.Replayed {
		t.Fatalf("post-resume dispatch: %+v %v", after, err)
	}
	if exec2.callCount() != 1 {
		t.Fatalf("post-resume executor calls: %d", exec2.callCount())
	}
}

func TestSupervisorCheckpointRebuildAfterLoss(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "rebuild", "rebuild-1")
	if _, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/a"), IdempotencyKey: "op-a",
	}); err != nil {
		t.Fatal(err)
	}
	// Lose the checkpoint row (crash between journal and checkpoint).
	if err := h.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		_, err := db.ExecContext(ctx, `DELETE FROM run_checkpoints WHERE round_id=?`, out.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sup.Checkpoint(ctx, out.RunID); contractErr(t, err).Code != researchcontract.OutcomeNotFound {
		t.Fatalf("missing checkpoint should read not_found")
	}
	// The next checkpoint refresh rebuilds from the journal + ledger.
	if _, err := h.sup.Stop(ctx, testOwner, out.RunID, "test"); err != nil {
		t.Fatal(err)
	}
	cp, err := h.sup.Checkpoint(ctx, out.RunID)
	if err != nil || len(cp.EvidenceIDs) != 2 || cp.RubricVersion != "rubric-v1" {
		t.Fatalf("rebuilt checkpoint: %+v %v", cp, err)
	}
	if _, err := h.sup.Resume(ctx, testOwner, out.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorStopDuringDispatchFences(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "stop fence", "stop-1")

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return okExecuteOutput(in), nil
		case <-ctx.Done():
			return researchcontract.ExecuteOutput{}, ctx.Err()
		}
	}
	dispatchErr := make(chan error, 1)
	go func() {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/slow"), IdempotencyKey: "op-slow",
		})
		dispatchErr <- err
	}()
	<-entered

	stopped, err := h.sup.Stop(ctx, testOwner, out.RunID, "owner_stopped")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Generation != out.Generation+1 || stopped.Uncertain != 1 {
		t.Fatalf("stop output: %+v", stopped)
	}
	// The fence (generation rotation) committed before cancellation: the
	// executor saw its context canceled.
	select {
	case err := <-dispatchErr:
		if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeUncertain {
			t.Fatalf("canceled dispatch: %+v", cerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not abort on stop")
	}

	// A completion that arrives past the fence keeps as evidence only. Drive
	// a second attempt through the same race, this time letting the executor
	// succeed after the fence.
	if _, err := h.sup.Resume(ctx, testOwner, out.RunID); err != nil {
		t.Fatal(err)
	}
	entered2 := make(chan struct{})
	var once2 sync.Once
	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		once2.Do(func() { close(entered2) })
		<-release // ignore cancellation; the remote call completed anyway
		return okExecuteOutput(in), nil
	}
	dispatchErr2 := make(chan error, 1)
	go func() {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/late"), IdempotencyKey: "op-late",
		})
		dispatchErr2 <- err
	}()
	<-entered2
	if _, err := h.sup.Stop(ctx, testOwner, out.RunID, "owner_stopped"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-dispatchErr2:
		if cerr := contractErr(t, err); cerr.Code != researchcontract.OutcomeStopped {
			t.Fatalf("late dispatch: %+v", cerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late dispatch never settled")
	}
	attempt, err := h.db.RoundAttemptForRequest(ctx, out.RunID, "op-late")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != store.AttemptUncertain || len(attempt.LateResult) == 0 || len(attempt.Result) != 0 {
		t.Fatalf("late attempt: state=%s result=%d late=%d",
			attempt.State, len(attempt.Result), len(attempt.LateResult))
	}
	results, err := h.db.RoundResults(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range results {
		var result dispatchResult
		if err := json.Unmarshal(raw, &result); err == nil && result.CaptureID == "cap-op-late" {
			t.Fatal("late completion wrote a current result")
		}
	}
	kinds := journalKinds(t, h, out.RunID)
	if countKind(kinds, researchcontract.EventLateObservation) != 1 {
		t.Fatalf("late journal: %v", kinds)
	}
	ledger, err := h.sup.Usage(ctx, out.RunID)
	if err != nil || !ledger.Unknown {
		t.Fatalf("unknown must latch while uncertain work stands: %+v %v", ledger, err)
	}
}

func TestSupervisorSteerAckAppliedFallback(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "steering", "steer-1")

	// No live turn: the message is durable and queued for the next turn.
	first, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Focus on backend roles.", IdempotencyKey: "steer-1",
	})
	if err != nil || first.State != SteerQueued || first.Revision != 1 || first.Applied || first.Live {
		t.Fatalf("queued steer: %+v %v", first, err)
	}
	received, err := h.journal.RunEvent(ctx, "steer."+out.RunID+".steer-1")
	if err != nil || received.Kind != SuperviseEventSteerReceived {
		t.Fatalf("durable steer message: %+v %v", received, err)
	}
	var msg struct {
		Revision int64  `json:"revision"`
		Body     string `json:"body"`
	}
	if err := json.Unmarshal(received.Payload, &msg); err != nil || msg.Revision != 1 || msg.Body != "Focus on backend roles." {
		t.Fatalf("steer payload: %+v %v", msg, err)
	}

	// Exact replay returns the original revision without re-applying.
	replay, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Focus on backend roles.", IdempotencyKey: "steer-1",
	})
	if err != nil || replay.State != SteerDuplicate || replay.Revision != 1 || replay.Applied {
		t.Fatalf("steer replay: %+v %v", replay, err)
	}

	// A live turn with a wired conversation gets the verified live path.
	turnStarted := make(chan string, 1)
	h.turns.run = func(ctx context.Context, agent store.Actor, in RunnerTurnInput) (RunnerTurnOutput, error) {
		turnStarted <- in.AttemptID
		<-ctx.Done()
		return RunnerTurnOutput{}, ctx.Err()
	}
	turnErr := make(chan error, 1)
	go func() {
		_, err := h.sup.ContinueRun(ctx, testAgent, TurnInput{RunID: out.RunID, RequestKey: "turn-live"})
		turnErr <- err
	}()
	var liveAttempt string
	select {
	case liveAttempt = <-turnStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("turn never started")
	}
	_ = liveAttempt
	live, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Also consider platform roles.", IdempotencyKey: "steer-2",
	})
	if err != nil || live.State != SteerApplied || !live.Live || live.Revision != 2 {
		t.Fatalf("live steer: %+v %v", live, err)
	}
	// The replay of a live-applied steer reports the application.
	replayLive, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Also consider platform roles.", IdempotencyKey: "steer-2",
	})
	if err != nil || replayLive.State != SteerDuplicate || !replayLive.Applied {
		t.Fatalf("live steer replay: %+v %v", replayLive, err)
	}

	// A fenced conversation (terminal turn) falls back to the queue without
	// losing the message.
	h.conv.run = func(ctx context.Context, roundID, attemptID, text string) (string, error) {
		return "", store.ErrFenced
	}
	fallback, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Ignore contracting.", IdempotencyKey: "steer-3",
	})
	if err != nil || fallback.State != SteerQueued || fallback.Revision != 3 {
		t.Fatalf("fallback steer: %+v %v", fallback, err)
	}
	applied, err := h.journal.RunEvent(ctx, "steer."+out.RunID+".steer-3.applied")
	if err != nil {
		t.Fatal(err)
	}
	var ack struct {
		Applied string `json:"applied"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(applied.Payload, &ack); err != nil || ack.Applied != "next_turn" || ack.Reason != "turn_terminal" {
		t.Fatalf("fallback ack: %+v %v", ack, err)
	}

	// Stale generation: rejected with the check's code/field/detail, journaled.
	h.conv.run = nil
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Steer(ctx, SteerInput{
			Actor: testOwner, RunID: out.RunID, Generation: out.Generation + 9,
			Body: "Stale view.", IdempotencyKey: "steer-stale",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeStale || cerr.Field != "generation" {
		t.Fatalf("stale steer: %+v", cerr)
	}
	rejected, err := h.journal.RunEvent(ctx, "steer-rejected."+out.RunID+".steer-stale")
	if err != nil {
		t.Fatalf("rejection not journaled: %v", err)
	}
	var rej struct {
		Code   string `json:"code"`
		Field  string `json:"field"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rejected.Payload, &rej); err != nil || rej.Code != "stale" || rej.Field != "generation" || rej.Detail == "" {
		t.Fatalf("rejection payload: %+v %v", rej, err)
	}

	// Non-owner steers are forbidden by run.control.
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Steer(ctx, SteerInput{
			Actor: testAgent, RunID: out.RunID, Generation: out.Generation,
			Body: "Agent steer.", IdempotencyKey: "steer-agent",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeForbidden {
		t.Fatalf("agent steer: %+v", cerr)
	}

	if _, err := h.sup.Stop(ctx, testOwner, out.RunID, "test"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-turnErr:
	case <-time.After(5 * time.Second):
		t.Fatal("live turn never settled after stop")
	}
}

func TestSupervisorSteerConcurrentRevisions(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "concurrent steers", "steer-race-1")

	const steers = 8
	var wg sync.WaitGroup
	revisions := make(chan int64, steers)
	errs := make(chan error, steers)
	for i := 0; i < steers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := h.sup.Steer(ctx, SteerInput{
				Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
				Body:           "steer body",
				IdempotencyKey: "race-" + string(rune('a'+i)),
			})
			if err != nil {
				errs <- err
				return
			}
			revisions <- got.Revision
		}(i)
	}
	wg.Wait()
	close(revisions)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent steer: %v", err)
	}
	seen := map[int64]bool{}
	for rev := range revisions {
		if seen[rev] {
			t.Fatalf("duplicate revision %d", rev)
		}
		seen[rev] = true
	}
	if len(seen) != steers {
		t.Fatalf("revisions: %d distinct of %d", len(seen), steers)
	}
	for rev := int64(1); rev <= steers; rev++ {
		if !seen[rev] {
			t.Fatalf("revision %d missing: %v", rev, seen)
		}
	}
}

func TestSupervisorCorrectionKeepsSavedRecords(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "correction", "correct-1")

	saved, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/saved"), IdempotencyKey: "op-saved",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A pending hold that the correction must fence and reap.
	pending, err := h.auth.Reserve(ctx, out.RunID, store.RoundResearchFetch, "op-pending", "payload")
	if err != nil {
		t.Fatal(err)
	}
	roundBefore, err := h.db.Round(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}

	corrected, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Correction: the owner moved to Berlin.", IdempotencyKey: "steer-correct",
		Correction: true,
	})
	if err != nil || corrected.State != SteerApplied || corrected.Revision != 1 {
		t.Fatalf("correction steer: %+v %v", corrected, err)
	}

	round, err := h.db.Round(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if round.State != store.RoundRunning || round.Generation != roundBefore.Generation+2 {
		t.Fatalf("authority did not rotate through stop+resume: %+v", round)
	}
	// The saved result and its audit row survive; the pending hold is gone
	// and refunded.
	results, err := h.db.RoundResults(ctx, out.RunID)
	if err != nil || len(results) != 1 {
		t.Fatalf("saved results after correction: %d %v", len(results), err)
	}
	kept, err := h.db.RoundAttempt(ctx, saved.AttemptID)
	if err != nil || kept.State != store.AttemptSucceeded {
		t.Fatalf("saved attempt: %+v %v", kept, err)
	}
	reaped, err := h.db.RoundAttempt(ctx, pending.AttemptID)
	if err != nil || reaped.State != store.AttemptCancelled {
		t.Fatalf("pending hold: %+v %v", reaped, err)
	}
	if round.Used.Requests != 1 || round.Used.Tools != 1 {
		t.Fatalf("pending hold not refunded: %+v", round.Used)
	}
	if round.Limits != roundBefore.Limits {
		t.Fatalf("correction touched limits: %+v", round.Limits)
	}
	kinds := journalKinds(t, h, out.RunID)
	if countKind(kinds, SuperviseEventCorrection) != 1 || countKind(kinds, SuperviseEventStopped) != 1 {
		t.Fatalf("correction journal: %v", kinds)
	}
	cp, err := h.sup.Checkpoint(ctx, out.RunID)
	if err != nil || cp.Generation != round.Generation {
		t.Fatalf("checkpoint after correction: %+v %v", cp, err)
	}
	// The next turn carries the correction from the durable message.
	turn, err := h.sup.ContinueRun(ctx, testAgent, TurnInput{RunID: out.RunID, RequestKey: "turn-after-correct"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turn.BriefUsed, "moved to Berlin") {
		t.Fatalf("turn brief misses correction: %q", turn.BriefUsed)
	}
}

func TestSupervisorCorrectionWithUnknownStaysPaused(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "correction unknown", "correct-2")

	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		return researchcontract.ExecuteOutput{}, errors.New("transport lost")
	}
	if _, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/doomed"), IdempotencyKey: "op-doomed",
	}); contractErr(t, err).Code != researchcontract.OutcomeUncertain {
		t.Fatalf("doomed dispatch should settle uncertain")
	}
	// The reconciler cannot verify the remote outcome either.
	h.obs.run = func(ctx context.Context, attemptID string) (Observation, error) {
		return Observation{State: store.AttemptUncertain,
			Evidence: json.RawMessage(`{"code":"check_failed"}`)}, nil
	}
	corrected, err := h.sup.Steer(ctx, SteerInput{
		Actor: testOwner, RunID: out.RunID, Generation: out.Generation,
		Body: "Correction under uncertainty.", IdempotencyKey: "steer-correct",
		Correction: true,
	})
	if contractErr(t, err).Code != researchcontract.OutcomeUncertain || corrected.State != SteerPaused {
		t.Fatalf("correction under uncertainty: %+v %v", corrected, err)
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.State != store.RoundPaused {
		t.Fatalf("run must stay paused: %+v %v", round, err)
	}
	// The message is durable; the owner resumes once reconciliation can
	// verify the work.
	received, err := h.journal.RunEvent(ctx, "steer."+out.RunID+".steer-correct")
	if err != nil {
		t.Fatalf("correction message lost: %v", err)
	}
	_ = received
	h.obs.run = nil // verification succeeds now
	resume, err := h.sup.Resume(ctx, testOwner, out.RunID)
	if err != nil || resume.Reconciled != 1 {
		t.Fatalf("late resume: %+v %v", resume, err)
	}
}

func TestSupervisorUnknownSpendLedger(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "unknown spend", "unknown-1")

	fresh, err := h.sup.Usage(ctx, out.RunID)
	if err != nil || fresh.Unknown || fresh.Reserved.Requests != 0 {
		t.Fatalf("fresh ledger: %+v %v", fresh, err)
	}
	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		return researchcontract.ExecuteOutput{}, errors.New("transport lost mid-call")
	}
	if _, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/maybe"), IdempotencyKey: "op-maybe",
	}); contractErr(t, err).Code != researchcontract.OutcomeUncertain {
		t.Fatalf("unverified execution should settle uncertain")
	}
	if _, err := h.sup.Stop(ctx, testOwner, out.RunID, "test"); err != nil {
		t.Fatal(err)
	}
	ledger, err := h.sup.Usage(ctx, out.RunID)
	if err != nil || !ledger.Unknown || ledger.Reserved.Requests != 1 || ledger.Observed.Requests != 0 {
		t.Fatalf("uncertain ledger: %+v %v", ledger, err)
	}
	kinds := journalKinds(t, h, out.RunID)
	if countKind(kinds, "run.uncertain") != 1 {
		t.Fatalf("uncertain journal: %v", kinds)
	}
	// Unverifiable reconciliation keeps the run paused with unknown spend
	// still latched; the attempt settles reconciled-uncertain for C's
	// expired-lease takeover.
	h.obs.run = func(ctx context.Context, attemptID string) (Observation, error) {
		return Observation{State: store.AttemptUncertain,
			Evidence: json.RawMessage(`{"code":"check_failed"}`)}, nil
	}
	if _, err := h.sup.Resume(ctx, testOwner, out.RunID); contractErr(t, err).Code != researchcontract.OutcomeUncertain {
		t.Fatalf("unverifiable resume should stay paused")
	}
	attempt, err := h.db.RoundAttemptForRequest(ctx, out.RunID, "op-maybe")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := h.auth.ReconciliationFor(ctx, attempt.ID)
	if err != nil || rec.State != store.ReconciledUncertain {
		t.Fatalf("reconciliation: %+v %v", rec, err)
	}
	ledger, err = h.sup.Usage(ctx, out.RunID)
	if err != nil || !ledger.Unknown {
		t.Fatalf("unknown must stay latched: %+v %v", ledger, err)
	}
	// A later verified observation resolves the attempt and resumes the
	// run, but the unknown latch stays: the run did spend into uncertainty
	// (T06 §5: unknown latches, never silently zero).
	h.obs.run = nil
	resume, err := h.sup.Resume(ctx, testOwner, out.RunID)
	if err != nil || resume.Reconciled != 1 {
		t.Fatalf("verified resume: %+v %v", resume, err)
	}
	ledger, err = h.sup.Usage(ctx, out.RunID)
	if err != nil || !ledger.Unknown || ledger.Observed.Requests == 0 {
		t.Fatalf("resolved ledger: %+v %v", ledger, err)
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.State != store.RoundRunning {
		t.Fatalf("run state after verified resume: %+v %v", round, err)
	}
}

func TestSupervisorDispatchConcurrencyBound(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "concurrency", "conc-1")

	// Six dispatches against the default bound of 2: the high-water mark
	// must never exceed it, and every checkpoint line must land (no lost
	// evidence under the checkpoint merge).
	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		select {
		case <-time.After(20 * time.Millisecond):
			return okExecuteOutput(in), nil
		case <-ctx.Done():
			return researchcontract.ExecuteOutput{}, ctx.Err()
		}
	}
	const dispatches = 6
	var wg sync.WaitGroup
	errs := make(chan error, dispatches)
	for i := 0; i < dispatches; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.sup.Dispatch(ctx, DispatchInput{
				RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
				Request:        testDescriptor("https://example.com/c" + string(rune('a'+i))),
				IdempotencyKey: "conc-" + string(rune('a'+i)),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent dispatch: %v", err)
	}
	h.exec.mu.Lock()
	highWater := h.exec.highWater
	h.exec.mu.Unlock()
	if highWater > 2 || highWater < 1 {
		t.Fatalf("concurrency high-water: %d (bound 2)", highWater)
	}
	cp, err := h.sup.Checkpoint(ctx, out.RunID)
	if err != nil || len(cp.EvidenceIDs) != 2*dispatches {
		t.Fatalf("checkpoint evidence under concurrency: %d %v", len(cp.EvidenceIDs), err)
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.Used.Requests != dispatches {
		t.Fatalf("charging under concurrency: %+v %v", round.Used, err)
	}
}

func TestSupervisorPerOpTimeoutSettlesUncertain(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{PerOpTimeout: 30 * time.Millisecond})
	out := h.commission(t, "timeout", "timeout-1")

	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		<-ctx.Done()
		return researchcontract.ExecuteOutput{}, ctx.Err()
	}
	if _, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/hang"), IdempotencyKey: "op-hang",
	}); contractErr(t, err).Code != researchcontract.OutcomeUncertain {
		t.Fatalf("timed-out dispatch should settle uncertain")
	}
	attempt, err := h.db.RoundAttemptForRequest(ctx, out.RunID, "op-hang")
	if err != nil || attempt.State != store.AttemptDispatched {
		t.Fatalf("timed-out attempt state: %+v %v", attempt, err)
	}
	// The caller bound wins when tighter than the default.
	h2 := newSupervisorHarness(t, SupervisorConfig{PerOpTimeout: time.Minute})
	out2 := h2.commission(t, "timeout bound", "timeout-2")
	h2.exec.run = h.exec.run
	start := time.Now()
	if _, err := h2.sup.Dispatch(ctx, DispatchInput{
		RunID: out2.RunID, Kind: researchcontract.ExecuteFetch,
		Request:        testDescriptor("https://example.com/hang"),
		Bounds:         researchcontract.Bounds{DeadlineMs: 30},
		IdempotencyKey: "op-hang",
	}); contractErr(t, err).Code != researchcontract.OutcomeUncertain {
		t.Fatalf("caller-bounded dispatch should settle uncertain")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("caller bound ignored: %v", elapsed)
	}
}

func TestSupervisorDeadlineFence(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	committed, err := h.sup.Commission(ctx, CommissionInput{
		Actor: testOwner, BriefText: "short run", AgentID: testAgent.ID,
		RubricVersion: "rubric-v1", IdempotencyKey: "deadline-1",
		Allowance: &AllowanceInput{TimeMs: 150, MaxActions: 4, MaxJev: 1, MaxTurns: 1, MaxConcurrent: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	roundBefore, err := h.db.Round(ctx, committed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: committed.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/x"), IdempotencyKey: "op-x",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeInvalid || cerr.Field != "deadline" {
		t.Fatalf("past-deadline dispatch: %+v", cerr)
	}
	round, err := h.db.Round(ctx, committed.RunID)
	if err != nil || round.Used != roundBefore.Used {
		t.Fatalf("deadline fence charged: %+v", round.Used)
	}
}

func TestSupervisorResumeSpendsRemaining(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out, err := h.sup.Commission(ctx, CommissionInput{
		Actor: testOwner, BriefText: "remaining", AgentID: testAgent.ID,
		RubricVersion: "rubric-v1", IdempotencyKey: "remain-1",
		Allowance: &AllowanceInput{TimeMs: 3600000, MaxActions: 3, MaxJev: 0, MaxTurns: 1, MaxConcurrent: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/1"), IdempotencyKey: "op-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sup.Stop(ctx, testOwner, out.RunID, "test"); err != nil {
		t.Fatal(err)
	}
	resume, err := h.sup.Resume(ctx, testOwner, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Limits {3,3,4,1}: one action spent, nothing to reconcile.
	if resume.Remaining.Requests != 2 || resume.Remaining.Tools != 3 || resume.Reconciled != 0 {
		t.Fatalf("resume output: %+v", resume)
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.Limits.Requests != 3 || round.Used.Requests != 1 {
		t.Fatalf("limits/used after resume: %+v %+v", round.Limits, round.Used)
	}
	// The remainder funds exactly two more actions; the fourth exhausts.
	for _, key := range []string{"op-2", "op-3"} {
		if _, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/" + key), IdempotencyKey: key,
		}); err != nil {
			t.Fatalf("dispatch %s on remainder: %v", key, err)
		}
	}
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/op-4"), IdempotencyKey: "op-4",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeBudgetExhausted {
		t.Fatalf("exhausted remainder: %+v", cerr)
	}

	// Resume state machine: running conflicts, terminal rejects.
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Resume(ctx, testOwner, out.RunID)
		return err
	}()); cerr.Code != researchcontract.OutcomeConflict {
		t.Fatalf("resume while running: %+v", cerr)
	}
	if _, err := h.db.FinishRound(ctx, testOwner, out.RunID, store.RoundCompleted,
		"done", "full", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Resume(ctx, testOwner, out.RunID)
		return err
	}()); cerr.Code != researchcontract.OutcomeInvalid {
		t.Fatalf("resume when terminal: %+v", cerr)
	}
}

func TestSupervisorStopReleasesReserved(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "reap", "reap-1")

	for _, key := range []string{"hold-1", "hold-2"} {
		if _, err := h.auth.Reserve(ctx, out.RunID, store.RoundResearchFetch, key, "p"); err != nil {
			t.Fatal(err)
		}
	}
	stopped, err := h.sup.Stop(ctx, testOwner, out.RunID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Released != 2 || stopped.Uncertain != 0 {
		t.Fatalf("stop output: %+v", stopped)
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.Used.Requests != 0 || round.Used.Tools != 0 {
		t.Fatalf("holds not refunded: %+v", round.Used)
	}
	// Re-reserving a reaped key names the fence instead of dispatching.
	if _, err := h.sup.Resume(ctx, testOwner, out.RunID); err != nil {
		t.Fatal(err)
	}
	if cerr := contractErr(t, func() error {
		_, err := h.sup.Dispatch(ctx, DispatchInput{
			RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
			Request: testDescriptor("https://example.com/hold"), IdempotencyKey: "hold-1",
		})
		return err
	}()); cerr.Code != researchcontract.OutcomeConflict && cerr.Code != researchcontract.OutcomeStale {
		t.Fatalf("reaped key re-dispatch: %+v", cerr)
	}
}

func TestSupervisorRunBoundsPublish(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{PerOpTimeout: 90 * time.Second, TurnTimeout: time.Minute})
	out, err := h.sup.Commission(ctx, CommissionInput{
		Actor: testOwner, BriefText: "bounds", AgentID: testAgent.ID,
		RubricVersion: "rubric-v1", IdempotencyKey: "bounds-1",
		Allowance: &AllowanceInput{TimeMs: 3600000, MaxActions: 4, MaxJev: 1, MaxTurns: 2, MaxConcurrent: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := h.sup.RunBoundsFor(ctx, out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if bounds.MaxConcurrent != 1 || bounds.PerOpTimeout != 90*time.Second ||
		bounds.TurnTimeout != time.Minute || bounds.Deadline.IsZero() {
		t.Fatalf("run bounds: %+v", bounds)
	}
	// The commissioned bound of 1 serializes execution.
	h.exec.run = func(ctx context.Context, in researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		select {
		case <-time.After(20 * time.Millisecond):
			return okExecuteOutput(in), nil
		case <-ctx.Done():
			return researchcontract.ExecuteOutput{}, ctx.Err()
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.sup.Dispatch(ctx, DispatchInput{
				RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
				Request:        testDescriptor("https://example.com/b" + string(rune('a'+i))),
				IdempotencyKey: "bound-" + string(rune('a'+i)),
			})
			if err != nil {
				t.Errorf("bounded dispatch: %v", err)
			}
		}(i)
	}
	wg.Wait()
	h.exec.mu.Lock()
	highWater := h.exec.highWater
	h.exec.mu.Unlock()
	if highWater != 1 {
		t.Fatalf("commissioned bound ignored: high-water %d", highWater)
	}
}

// A certified pre-claim refusal (T24 F2/F3) releases the supervisor hold
// with a full refund and surfaces the refusal cause verbatim: the attempt
// lands cancelled with the refusal code, the key settles terminally (stale
// on redispatch, the failures precedent), and the refusal journals as a
// run.dispatch_refused companion to the dispatch event.
func TestSupervisorDispatchReleasesOnPreClaimRefusal(t *testing.T) {
	for _, cause := range []*researchcontract.Error{
		researchcontract.NewError(researchcontract.OutcomeInvalid, "request.method", "bad verb"),
		researchcontract.NewError(researchcontract.OutcomeBudgetExhausted, "allowance", "no room"),
	} {
		t.Run(string(cause.Code), func(t *testing.T) {
			ctx := context.Background()
			h := newSupervisorHarness(t, SupervisorConfig{})
			out := h.commission(t, "pre-claim refusal", "refuse-"+string(cause.Code))
			h.exec.run = func(context.Context, researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
				return researchcontract.ExecuteOutput{}, &researchexecute.PreClaimRefusal{Err: cause}
			}

			_, err := h.sup.Dispatch(ctx, DispatchInput{
				RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
				Request: testDescriptor("https://example.com/refused"), IdempotencyKey: "op-refused",
			})
			if got := contractErr(t, err); got.Code != cause.Code || got.Detail != cause.Detail {
				t.Fatalf("refusal surfaced: %+v, want %+v", got, cause)
			}
			attempt, err := h.db.RoundAttemptForRequest(ctx, out.RunID, "op-refused")
			if err != nil {
				t.Fatal(err)
			}
			if attempt.State != store.AttemptCancelled || attempt.ErrorCode != store.AbandonedPreClaimCode {
				t.Fatalf("refused attempt: state=%s code=%s", attempt.State, attempt.ErrorCode)
			}
			if len(attempt.Result) != 0 {
				t.Fatal("refused attempt stored a result")
			}
			round, err := h.db.Round(ctx, out.RunID)
			if err != nil || round.Used.Requests != 0 || round.Used.Tools != 0 {
				t.Fatalf("refusal held a charge: %+v %v", round.Used, err)
			}
			kinds := journalKinds(t, h, out.RunID)
			if countKind(kinds, SuperviseEventDispatched) != 1 ||
				countKind(kinds, SuperviseEventDispatchRefused) != 1 {
				t.Fatalf("refusal journal: %v", kinds)
			}
			_, err = h.sup.Dispatch(ctx, DispatchInput{
				RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
				Request: testDescriptor("https://example.com/refused"), IdempotencyKey: "op-refused",
			})
			if got := contractErr(t, err); got.Code != researchcontract.OutcomeStale {
				t.Fatalf("refused-key redispatch: %+v", got)
			}
			if round, _ := h.db.Round(ctx, out.RunID); round.Used.Requests != 0 {
				t.Fatalf("refused-key redispatch charged: %+v", round.Used)
			}
		})
	}
}

// Only the typed pre-claim signal releases: a plain executor error of any
// code keeps the stop/reconcile path (the attempt stays dispatched with
// its charge held) so post-dispatch failures can never refund by mistake.
func TestSupervisorDispatchPlainExecutorErrorStaysUncertain(t *testing.T) {
	ctx := context.Background()
	h := newSupervisorHarness(t, SupervisorConfig{})
	out := h.commission(t, "plain executor error", "plain-err-1")
	h.exec.run = func(context.Context, researchcontract.ExecuteInput) (researchcontract.ExecuteOutput, error) {
		return researchcontract.ExecuteOutput{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "claim", "post-claim store failure")
	}

	_, err := h.sup.Dispatch(ctx, DispatchInput{
		RunID: out.RunID, Kind: researchcontract.ExecuteFetch,
		Request: testDescriptor("https://example.com/plain"), IdempotencyKey: "op-plain",
	})
	if got := contractErr(t, err); got.Code != researchcontract.OutcomeUncertain {
		t.Fatalf("plain executor error: %+v", got)
	}
	attempt, err := h.db.RoundAttemptForRequest(ctx, out.RunID, "op-plain")
	if err != nil || attempt.State != store.AttemptDispatched {
		t.Fatalf("attempt state: %+v %v", attempt, err)
	}
	round, err := h.db.Round(ctx, out.RunID)
	if err != nil || round.Used.Requests != 1 {
		t.Fatalf("charge released without a refusal signal: %+v %v", round.Used, err)
	}
	kinds := journalKinds(t, h, out.RunID)
	if countKind(kinds, SuperviseEventDispatchRefused) != 0 {
		t.Fatalf("plain error journaled a refusal: %v", kinds)
	}
}
