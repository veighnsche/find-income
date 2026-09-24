// Research run supervision: commissioning, bounded dispatch, multi-turn
// continuation, steering acknowledgment, stop/resume and crash recovery.
//
// Owner: lane B (runtime), T13. The supervisor orchestrates the T12 authority,
// the T07 run_events/run_checkpoints tables (via the durable journal in
// store/round_supervision.go) and the agreed researchcontract interfaces.
// Execution backends stay behind doubles here: the real research executor
// binds at T23, the Codex turn runner at T17. No live turns, spend, browser
// or binary launches happen in this file.
//
// Authority rules (T06 §5, plan §8): Stop fences first (control-generation
// rotation in the store transaction) and only then cancels owned execution;
// no new request or business commit may begin past that boundary, and late
// results land as evidence only. Resume reconciles in-flight work, rotates
// credentials and generation, restores the checkpoint and spends the
// REMAINING allowance; it never grants a fresh budget. Saved audited records
// survive every fence: only pending (reserved/dispatched) writes are fenced.
package rounds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchexecute"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// T13 run/turn/steering event kinds (T06 §5 permits B to add these). They join
// the T08 kinds (run.turn_observed, run.item_*, run.steered, run.steer_rejected,
// run.interrupted, run.resumed, run.uncertain, run.unknown_events) and the T06
// research kinds in one durable journal; together they are the draft T10
// activity-feed shape.
const (
	// SuperviseEventCommissioned records the durable commission: full brief,
	// rubric version, agent, allowance and concurrency. Crash recovery heals
	// a missing checkpoint from this event.
	SuperviseEventCommissioned = "run.commissioned"
	// SuperviseEventDispatched marks one committed dispatch (research op or
	// model turn) before execution starts.
	SuperviseEventDispatched = "run.dispatched"
	// SuperviseEventObserved records one settled dispatch outcome. Executor-
	// claimed observation/capture ids ride the payload, never the FK-linked
	// envelope columns: the supervisor asserts no linkage it does not own
	// (C journals envelope-linked observation events at T16).
	SuperviseEventObserved = "run.observed"
	// SuperviseEventCheckpointed marks an explicit supervisor checkpoint save
	// (turn settle, stop, resume, correction).
	SuperviseEventCheckpointed = "run.checkpointed"
	// SuperviseEventStopped records a completed owner stop with generation,
	// reaped holds and the uncertain set.
	SuperviseEventStopped = "run.stopped"
	// SuperviseEventSteerReceived is the durable steering message with its
	// revision; acknowledgment state follows as run.steered.
	SuperviseEventSteerReceived = "run.steer_received"
	// SuperviseEventCorrection records an applied owner correction that
	// rotated authority.
	SuperviseEventCorrection = "run.correction_applied"
	// SuperviseEventRecovered marks a continuation turn that rebuilt context
	// from the checkpoint because the stored conversation was unavailable.
	SuperviseEventRecovered = "run.recovered"
	// SuperviseEventDispatchFenced records a reservation that met the fence
	// between reserve and dispatch (Stop won the race).
	SuperviseEventDispatchFenced = "run.dispatch_fenced"
	// SuperviseEventDispatchRefused records a dispatch the executor refused
	// pre-claim (T24 F2/F3): validation, authority check, reservation or
	// claim refused before any external work, so the supervisor released its
	// hold and refunded the charge. The refusal cause is the event outcome.
	SuperviseEventDispatchRefused = "run.dispatch_refused"
	// SuperviseEventSaved records one committed records_save batch: the
	// saved record ids join the checkpoint so run views and reports derive
	// saved counts from durable state. Event ids are deterministic per
	// batch key, so an exact replay journals once.
	SuperviseEventSaved = "run.saved"
)

// Reused T08 kind literals. rounds cannot import codexservice (it imports
// rounds), so the shared literals are repeated here. Single-writer rule:
// codexservice-driven turns journal via ItemCorrelator/turnControl while
// supervisor-driven continuation journals here; turn_observed ids are
// deterministic (turn.<thread>.<turn>.<status>), so a double journal
// collapses to one row.
const (
	superviseEventTurnObserved  = "run.turn_observed"
	superviseEventSteered       = "run.steered"
	superviseEventSteerRejected = "run.steer_rejected"
	superviseEventResumed       = "run.resumed"
	superviseEventUncertain     = "run.uncertain"
)

// Initial canary commission defaults (plan §8: operational settings to
// validate and adjust, not quotas). Limits derivation: every research action
// and every Jev request charges one request; every action charges one item
// slot (at most one saved item per action) and one tool; every model turn
// charges one tool and one turn.
const (
	DefaultRunMinutes    = 15
	DefaultMaxActions    = 60
	DefaultMaxJev        = 12
	DefaultMaxTurns      = 8
	DefaultMaxConcurrent = 2
)

// Calibrated execution bounds (T22, from T02 proof measurements). The live
// proof's slowest operation was a 359ms browser render inside a 1.6s run;
// fixture search/fetch/api calls settled in ≤20ms and the largest live
// body was 559 bytes (example.com). The per-operation deadline is twice
// the executor's internal 30s op default, so the executor's own deadline
// fires first with partial accounting while the supervisor ceiling covers
// reservation/claim/settle overhead and slow public origins. The byte and
// request ceilings match the executor defaults: one response body up to
// 1 MiB (~1800x the live measurement, covering large rendered DOMs) and
// 32 requests per operation including browser subresources (the T16
// three-subresource render is the largest proven shape). Per-run ceilings
// derive from the 60-action canary allowance (1920 subrequests, 60 MiB
// observed worst case) and stay observed via journaled ExecuteUsage —
// hard per-run byte enforcement has no authority field (plan §8: hard
// only where the mechanism can enforce).
const (
	DefaultPerOpTimeout = 60 * time.Second
	DefaultTurnTimeout  = 10 * time.Minute

	// MaxDispatchBodyBytes caps one response body per request.
	MaxDispatchBodyBytes = int64(1 << 20)
	// MaxDispatchRequests caps one operation's requests including
	// browser subresources.
	MaxDispatchRequests = 32
)

// clampDispatchBounds enforces the calibrated ceilings on caller-supplied
// bounds. Zero values pass through (the executor substitutes its
// defaults); negatives pass through for the executor to reject.
func clampDispatchBounds(b researchcontract.Bounds, perOp time.Duration) researchcontract.Bounds {
	if b.MaxBytes > MaxDispatchBodyBytes {
		b.MaxBytes = MaxDispatchBodyBytes
	}
	if b.MaxRequests > MaxDispatchRequests {
		b.MaxRequests = MaxDispatchRequests
	}
	if ceil := perOp.Milliseconds(); ceil > 0 && b.DeadlineMs > ceil {
		b.DeadlineMs = ceil
	}
	return b
}

const (
	maxBriefText     = 20000
	maxSteerBody     = 20000
	maxIdempotency   = 128
	maxCorrections   = 512
	maxAgentID       = 128
	maxRubricVersion = 128
	maxTurnBrief     = 12000
	maxTurnEvidence  = 32000
)

// recordWriteScopeOps mirrors T12's record.write grant set so commissioned
// runs accept D's future saves; TestSupervisorCommissionScopeGuardsDrift fails
// if the grant set moves without this list.
var recordWriteScopeOps = []string{
	store.RoundCreateCompany, store.RoundCreateOpportunity, store.RoundSaveSourceOpportunity,
	store.RoundCorrectEvidence, store.RoundCorrectOpportunity,
}

// TurnRunner executes one model turn against an already reserved and
// dispatched codex.turn attempt. It is pure runtime: no store access, no
// reservation, no observation writes. T17 implements it over codexservice
// (resume the stored thread when ThreadID is set, else open a fresh thread,
// run the turn with the supervisor-built brief, return the verified outcome);
// T13 tests drive doubles.
type TurnRunner interface {
	RunTurn(ctx context.Context, agent store.Actor, in RunnerTurnInput) (RunnerTurnOutput, error)
}

// RunnerTurnInput scopes one continuation turn. ThreadID is "" for the first
// turn; later turns carry the latest stored thread. Capability is the
// one-turn tool capability the supervisor issues after dispatch; the runner
// embeds it in the turn prompt so model tool calls bind the active attempt.
type RunnerTurnInput struct {
	RunID      string
	RequestKey string
	AttemptID  string
	ThreadID   string
	Brief      string
	Evidence   string
	Generation int64
	Capability string
}

// RunnerTurnOutput reports the verified turn outcome. Status is one of
// completed, failed, interrupted, unknown. NextWork is the model's proposed
// next work; the supervisor owns persisting it.
type RunnerTurnOutput struct {
	ThreadID string
	TurnID   string
	Status   string
	Evidence json.RawMessage
	NextWork []string
}

// ConversationControl steers a live turn. codexservice.Service satisfies it
// implicitly (identical signature to SteerAttempt), so T23 wires the real
// conversation path without an adapter. A nil control selects the honest
// fallback: the message stays durable and queued for the next turn.
type ConversationControl interface {
	SteerAttempt(ctx context.Context, roundID, attemptID, text string) (string, error)
}

// CredentialRotator advances an advisory credential-version reference.
// *Authority implements it; fakes may omit it.
type CredentialRotator interface {
	RotateCredentials(runID string) int64
}

// SupervisorConfig bounds execution. Zero values select defaults.
type SupervisorConfig struct {
	MaxConcurrent int
	PerOpTimeout  time.Duration
	TurnTimeout   time.Duration
}

// SupervisorDeps wires one supervisor. DB, Authority and Journal are required;
// Executor, Turns, Conversation and Observer degrade honestly when nil (see
// each method).
type SupervisorDeps struct {
	DB           *store.Store
	Authority    researchcontract.Authority
	Executor     researchcontract.Executor
	Journal      researchcontract.EventSink
	Turns        TurnRunner
	Conversation ConversationControl
	Observer     Reconciler
	Config       SupervisorConfig
}

// Supervisor orchestrates research runs. It is safe for concurrent use.
type Supervisor struct {
	db         *store.Store
	auth       researchcontract.Authority
	exec       researchcontract.Executor
	journal    researchcontract.EventSink
	turns      TurnRunner
	converse   ConversationControl
	observer   Reconciler
	maxConc    int
	perOp      time.Duration
	turnBudget time.Duration
	control    *Service

	// depMu guards the late-bound service deps (turns/converse/observer)
	// and the control reconciler flip in SetObserver. Construction sets
	// the deps; T23 late binding uses the Set* setters before serving.
	depMu sync.RWMutex

	mu   sync.Mutex
	runs map[string]*runTracker

	ckptMu sync.Mutex

	// maxConcs caches the commissioned per-run concurrency bound. Guarded
	// by mu, like runs.
	maxConcs map[string]int
}

// runTracker owns one run's execution lifetime: cancellation fan-out, drain
// accounting and the concurrency semaphore.
type runTracker struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	sem    chan struct{}
}

// NewSupervisor builds the T17/T23 supervision entrypoint.
func NewSupervisor(deps SupervisorDeps) (*Supervisor, error) {
	if deps.DB == nil {
		return nil, errors.New("rounds: supervisor store required")
	}
	if deps.Authority == nil {
		return nil, errors.New("rounds: supervisor authority required")
	}
	if deps.Journal == nil {
		return nil, errors.New("rounds: supervisor journal required")
	}
	maxConc := deps.Config.MaxConcurrent
	if maxConc == 0 {
		maxConc = DefaultMaxConcurrent
	}
	if maxConc < 1 || maxConc > 4 {
		return nil, errors.New("rounds: supervisor maxConcurrent must be 1..4")
	}
	perOp := deps.Config.PerOpTimeout
	if perOp <= 0 {
		perOp = DefaultPerOpTimeout
	}
	turnBudget := deps.Config.TurnTimeout
	if turnBudget <= 0 {
		turnBudget = DefaultTurnTimeout
	}
	s := &Supervisor{
		db: deps.DB, auth: deps.Authority, exec: deps.Executor, journal: deps.Journal,
		turns: deps.Turns, converse: deps.Conversation, observer: deps.Observer,
		maxConc: maxConc, perOp: perOp, turnBudget: turnBudget,
		runs: map[string]*runTracker{}, maxConcs: map[string]int{},
	}
	// Without an observer there is no remote check to run: leave the
	// reconciler unwired so resume reports uncertain without charging
	// allowance for checks that cannot observe.
	s.control = &Service{Store: deps.DB, Readiness: s, Canceller: s, Worker: s}
	if deps.Observer != nil {
		s.control.Reconciler = s
	}
	return s, nil
}

// Late binding for the lazily built service deps (T23): the codexservice
// Service only exists after first use, so a startup-constructed supervisor
// binds the turn runner, conversation control and dispatch observer once
// the service is available (codexservice.Lazy.SetResearchWiring). Binding
// is single-assignment: a nil dep or a second bind returns an error and
// keeps the existing binding, so construction-time deps always win over a
// late call. Call setters before serving load; dep reads during execution
// take the read lock, but the control reconciler flip is startup wiring.

// SetTurnRunner binds the commissioned-turn runner once.
func (s *Supervisor) SetTurnRunner(t TurnRunner) error {
	s.depMu.Lock()
	defer s.depMu.Unlock()
	if t == nil {
		return errors.New("rounds: turn runner required")
	}
	if s.turns != nil {
		return errors.New("rounds: turn runner already bound")
	}
	s.turns = t
	return nil
}

// SetConversation binds live-turn steering once.
func (s *Supervisor) SetConversation(c ConversationControl) error {
	s.depMu.Lock()
	defer s.depMu.Unlock()
	if c == nil {
		return errors.New("rounds: conversation control required")
	}
	if s.converse != nil {
		return errors.New("rounds: conversation control already bound")
	}
	s.converse = c
	return nil
}

// SetObserver binds the dispatch observer once and enables remote
// reconciliation on the shared control service (mirroring the
// construction-time wiring).
func (s *Supervisor) SetObserver(o Reconciler) error {
	s.depMu.Lock()
	defer s.depMu.Unlock()
	if o == nil {
		return errors.New("rounds: dispatch observer required")
	}
	if s.observer != nil {
		return errors.New("rounds: dispatch observer already bound")
	}
	s.observer = o
	s.control.Reconciler = s
	return nil
}

func (s *Supervisor) turnRunner() TurnRunner {
	s.depMu.RLock()
	defer s.depMu.RUnlock()
	return s.turns
}

func (s *Supervisor) conversation() ConversationControl {
	s.depMu.RLock()
	defer s.depMu.RUnlock()
	return s.converse
}

func (s *Supervisor) dispatchObserver() Reconciler {
	s.depMu.RLock()
	defer s.depMu.RUnlock()
	return s.observer
}

// Close cancels all owned execution. In-flight dispatches settle uncertain.
func (s *Supervisor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.runs {
		t.cancel()
	}
}

// CheckRound implements Readiness: a research run without an execution
// backend cannot continue.
func (s *Supervisor) CheckRound(ctx context.Context, outcome string) error {
	if s.exec == nil {
		return ErrNotReady
	}
	return nil
}

// LaunchRound implements Worker: establish run ownership after commit. T17
// starts the agent loop here; T13 records ownership so Stop cancels and
// Resume drains.
func (s *Supervisor) LaunchRound(ctx context.Context, r store.Round) error {
	s.resolveMaxConc(ctx, r.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureTrackerLocked(r.ID)
	return nil
}

// CancelRound implements Worker: revoke owned execution after the fence.
func (s *Supervisor) CancelRound(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.runs[runID]; ok {
		t.cancel()
	}
}

// WaitRoundStopped implements WorkerDrainer: wait for owned execution to
// settle or the caller to give up.
func (s *Supervisor) WaitRoundStopped(ctx context.Context, runID string) error {
	s.mu.Lock()
	t, ok := s.runs[runID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// CancelDispatch implements Canceller: drop one owned attempt context. The
// generation fence is the real protection; this signal is advisory.
func (s *Supervisor) CancelDispatch(ctx context.Context, attemptID string) error {
	return nil
}

// ObserveDispatch implements Reconciler by delegating to the injected
// observer (codexservice at T23 satisfies Reconciler directly).
func (s *Supervisor) ObserveDispatch(ctx context.Context, attemptID string) (Observation, error) {
	observer := s.dispatchObserver()
	if observer == nil {
		return Observation{}, errors.New("rounds: no dispatch observer wired")
	}
	return observer.ObserveDispatch(ctx, attemptID)
}

func (s *Supervisor) ensureTrackerLocked(runID string) *runTracker {
	if t, ok := s.runs[runID]; ok && t.ctx.Err() == nil {
		return t
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &runTracker{ctx: ctx, cancel: cancel, sem: make(chan struct{}, s.runMaxConcLocked(runID))}
	s.runs[runID] = t
	return t
}

// tracker returns the live tracker for a run, recreating it after a cancel.
func (s *Supervisor) tracker(runID string) *runTracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureTrackerLocked(runID)
}

// runMaxConcLocked resolves the per-run concurrency bound: the commissioned
// value wins, else the supervisor default.
func (s *Supervisor) runMaxConcLocked(runID string) int {
	if n, ok := s.maxConcs[runID]; ok && n >= 1 && n <= 4 {
		return n
	}
	return s.maxConc
}

// resolveMaxConc loads the commissioned concurrency bound once per run. The
// journal read runs outside mu; only the cache touch holds it.
func (s *Supervisor) resolveMaxConc(ctx context.Context, runID string) int {
	s.mu.Lock()
	if n, ok := s.maxConcs[runID]; ok {
		s.mu.Unlock()
		return n
	}
	s.mu.Unlock()
	n := s.maxConc
	if c, err := s.commissionRecord(ctx, runID); err == nil && c.MaxConcurrent >= 1 && c.MaxConcurrent <= 4 {
		n = c.MaxConcurrent
	}
	s.mu.Lock()
	s.maxConcs[runID] = n
	s.mu.Unlock()
	return n
}

// cacheMaxConc records a known per-run bound (commission time).
func (s *Supervisor) cacheMaxConc(runID string, n int) {
	if n < 1 || n > 4 {
		return
	}
	s.mu.Lock()
	s.maxConcs[runID] = n
	s.mu.Unlock()
}

func requireOwner(actor store.Actor) error {
	if actor.Kind != "administrator" || actor.ID == "" {
		return researchcontract.NewError(researchcontract.OutcomeForbidden,
			string(researchcontract.AuthorityPermission), "run control requires the owner administrator")
	}
	return nil
}

func validKey(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value
}

// mapStoreError translates storage failures at the supervision boundary into
// contract errors. Callers needing stale-vs-stopped precision reload the
// round and map from state instead.
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	var cerr *researchcontract.Error
	if errors.As(err, &cerr) {
		return err
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return researchcontract.NewError(researchcontract.OutcomeNotFound, "run", "unknown run")
	case errors.Is(err, store.ErrInvalid):
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "run", "invalid supervision input: "+err.Error())
	case errors.Is(err, store.ErrAllowance):
		return researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
			string(researchcontract.AuthorityAllowance), "allowance exhausted: "+err.Error())
	case errors.Is(err, store.ErrUncertain):
		return researchcontract.NewError(researchcontract.OutcomeUncertain, "attempt",
			"unreconciled work is outstanding; reconcile before continuing")
	case errors.Is(err, store.ErrExpired):
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityDeadline), "run deadline passed")
	case errors.Is(err, store.ErrConflict):
		return researchcontract.NewError(researchcontract.OutcomeRevisionConflict, "run",
			"brief or revision moved under this request: "+err.Error())
	case errors.Is(err, store.ErrActiveRound), errors.Is(err, store.ErrRoundIdempotencyConflict):
		return researchcontract.NewError(researchcontract.OutcomeConflict, "run", err.Error())
	case errors.Is(err, store.ErrFenced):
		return researchcontract.NewError(researchcontract.OutcomeStopped, "run",
			"authority fenced by stop or rotation; reload run state before retrying")
	}
	return err
}

// AllowanceInput mirrors the T10 allowance shape without generated types:
// time budget plus logical-action, Jev and turn caps and a concurrency bound.
type AllowanceInput struct {
	TimeMs        int64
	MaxActions    int64
	MaxJev        int64
	MaxTurns      int64
	MaxConcurrent int64
}

func defaultAllowance() AllowanceInput {
	return AllowanceInput{
		TimeMs:     int64(DefaultRunMinutes) * 60 * 1000,
		MaxActions: DefaultMaxActions, MaxJev: DefaultMaxJev,
		MaxTurns: DefaultMaxTurns, MaxConcurrent: DefaultMaxConcurrent,
	}
}

// CommissionInput commissions one research run. RubricVersion is explicit:
// the checkpoint and the Jev reuse key need it, and no central rubric source
// exists yet (T17/A follow-up), so the supervisor invents no default.
// RubricSource records where the caller resolved the rubric version from.
type CommissionInput struct {
	Actor          store.Actor
	BriefText      string
	AgentID        string
	RubricVersion  string
	RubricSource   string
	ProfileVersion int64 // 0 = current owner profile
	Allowance      *AllowanceInput
	IdempotencyKey string
	CorrectionsRef string
}

// CommissionOutput describes the commissioned run. Created=false is an exact
// replay of the same key: the existing run, unchanged.
type CommissionOutput struct {
	RunID          string
	Generation     int64
	State          string
	Created        bool
	ProfileVersion int64
	RubricVersion  string
	Limits         researchcontract.Allowance
	Deadline       time.Time
	MaxConcurrent  int
}

// commissionRecord is the durable commission behind run.commissioned: the
// crash-recovery source for checkpoint rebuilds.
type commissionRecord struct {
	Brief         string         `json:"brief"`
	RubricVersion string         `json:"rubricVersion"`
	RubricSource  string         `json:"rubricSource,omitempty"`
	AgentID       string         `json:"agentId"`
	Allowance     AllowanceInput `json:"allowance"`
	MaxConcurrent int            `json:"maxConcurrent"`
	Corrections   string         `json:"correctionsRef,omitempty"`
}

func commissionedEventID(runID string) string { return "run." + runID + ".commissioned" }

func (s *Supervisor) commissionRecord(ctx context.Context, runID string) (commissionRecord, error) {
	var c commissionRecord
	e, err := journalGet(ctx, s.journal, commissionedEventID(runID))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(e.Payload, &c); err != nil {
		return commissionRecord{}, err
	}
	return c, nil
}

// journalGet reads one event through the sink interface. The durable journal
// serves point reads; other sinks return a lookup miss.
func journalGet(ctx context.Context, sink researchcontract.EventSink, eventID string) (researchcontract.Event, error) {
	type getter interface {
		RunEvent(ctx context.Context, eventID string) (researchcontract.Event, error)
	}
	if g, ok := sink.(getter); ok {
		return g.RunEvent(ctx, eventID)
	}
	return researchcontract.Event{}, store.ErrNotFound
}

func (s *Supervisor) journalAppend(ctx context.Context, e researchcontract.Event) error {
	if e.ObservedAt.IsZero() {
		e.ObservedAt = time.Now()
	}
	if e.RecordedAt.IsZero() {
		e.RecordedAt = e.ObservedAt
	}
	return s.journal.Append(ctx, e)
}

func mustPayload(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// Commission starts one research run: scope, allowance, deadline, durable
// commission record and initial checkpoint. Replaying the same key returns
// the existing run and heals any missing init state (commission record or
// checkpoint) left by a crash; it never duplicates work.
func (s *Supervisor) Commission(ctx context.Context, in CommissionInput) (CommissionOutput, error) {
	if err := requireOwner(in.Actor); err != nil {
		return CommissionOutput{}, err
	}
	if len(in.BriefText) > maxBriefText {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "briefText",
			fmt.Sprintf("brief exceeds %d chars", maxBriefText))
	}
	if !validKey(in.AgentID, maxAgentID) {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "agentId",
			"delegated agent id required (1..128 chars, no surrounding space)")
	}
	if strings.TrimSpace(in.RubricVersion) == "" || len(in.RubricVersion) > maxRubricVersion ||
		strings.TrimSpace(in.RubricVersion) != in.RubricVersion {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "rubricVersion",
			"rubric version required (1..128 chars, no surrounding space)")
	}
	if !validKey(in.IdempotencyKey, maxIdempotency) {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "idempotencyKey",
			"idempotency key required (1..128 chars, no surrounding space)")
	}
	if len(in.CorrectionsRef) > maxCorrections {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "correctionsRef",
			fmt.Sprintf("corrections reference exceeds %d chars", maxCorrections))
	}
	allow := defaultAllowance()
	if in.Allowance != nil {
		allow = *in.Allowance
	}
	if allow.TimeMs < 1 || allow.MaxActions < 1 || allow.MaxJev < 0 || allow.MaxTurns < 1 ||
		allow.MaxConcurrent < 1 || allow.MaxConcurrent > 4 {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "allowance",
			"allowance needs timeMs>=1, maxActions>=1, maxJev>=0, maxTurns>=1, maxConcurrent 1..4")
	}
	profile := in.ProfileVersion
	if profile == 0 {
		p, err := s.db.CurrentPreferences(ctx)
		if err != nil {
			return CommissionOutput{}, mapStoreError(fmt.Errorf("read owner profile: %w", err))
		}
		profile = p.Version
	}
	if profile < 1 {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "profileVersion",
			"profile version must be >= 1")
	}
	deadline := time.Now().Add(time.Duration(allow.TimeMs) * time.Millisecond)
	if !deadline.After(time.Now()) || deadline.After(time.Now().Add(24*time.Hour)) {
		return CommissionOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "allowance",
			"deadline must be in the future and within 24h")
	}
	limits := store.RoundAllowance{
		Requests: allow.MaxActions + allow.MaxJev,
		Items:    allow.MaxActions,
		Tools:    allow.MaxActions + allow.MaxTurns,
		Turns:    allow.MaxTurns,
	}
	ops := append(append(append([]string{}, store.ResearchOperations()...),
		store.RoundCodexTurn, store.RoundJevRequest, store.RoundJevAssess), recordWriteScopeOps...)
	scope := store.RoundScope{
		Operations: ops,
		Resources:  []string{store.ResearchAuthorityResource},
		Delegates:  []string{in.AgentID},
	}
	intent := "Research run"
	if trimmed := strings.TrimSpace(in.BriefText); trimmed != "" {
		intent = "Research run: " + trimmed
		if len(intent) > 2000 {
			intent = intent[:1990] + "[…]"
		}
	}
	rec := commissionRecord{
		Brief: in.BriefText, RubricVersion: in.RubricVersion, RubricSource: in.RubricSource,
		AgentID: in.AgentID, Allowance: allow, MaxConcurrent: int(allow.MaxConcurrent),
		Corrections: in.CorrectionsRef,
	}
	round, created, err := s.db.StartRound(ctx, in.Actor, store.StartRoundInput{
		RequestKey: in.IdempotencyKey, Intent: intent, Outcome: "research_run",
		ProfileVersion: profile, Scope: scope, Limits: limits, Deadline: deadline,
	})
	if err != nil {
		// The store digest covers the absolute deadline, which drifts with
		// the clock between the original commission and a replay. A replay
		// that matches on every commission field except the deadline is the
		// same commission: the original deadline stands (first write wins).
		if errors.Is(err, store.ErrRoundIdempotencyConflict) {
			if existing, rerr := s.db.RoundByRequest(ctx, in.Actor, in.IdempotencyKey); rerr == nil &&
				sameCommission(existing, intent, profile, scope, limits) {
				return s.healCommission(ctx, existing, rec)
			}
		}
		return CommissionOutput{}, mapStoreError(err)
	}
	if !created {
		return s.healCommission(ctx, round, rec)
	}
	round, err = s.db.ActivateRound(ctx, in.Actor, round.ID)
	if err != nil {
		return CommissionOutput{}, mapStoreError(err)
	}
	out := CommissionOutput{
		RunID: round.ID, Generation: round.Generation, State: string(round.State), Created: true,
		ProfileVersion: profile, RubricVersion: in.RubricVersion,
		Limits:   researchcontract.Allowance{Requests: limits.Requests, Items: limits.Items, Tools: limits.Tools, Turns: limits.Turns},
		Deadline: round.Deadline, MaxConcurrent: int(allow.MaxConcurrent),
	}
	now := time.Now()
	if err := s.journalAppend(ctx, researchcontract.Event{
		ID: commissionedEventID(round.ID), RunID: round.ID, Kind: SuperviseEventCommissioned,
		Outcome: researchcontract.OutcomeOK, Payload: mustPayload(rec),
		ObservedAt: now, RecordedAt: now,
	}); err != nil {
		// The run exists and is active; the commission record write failed.
		// Report explicitly: retrying the same key heals it.
		return out, errors.Join(
			researchcontract.NewError(researchcontract.OutcomeUncertain, "journal",
				"run commissioned but the commission record was not journaled; retry the same idempotency key to heal"),
			err)
	}
	if err := s.saveInitialCheckpoint(ctx, round, profile, in.RubricVersion); err != nil {
		return out, errors.Join(
			researchcontract.NewError(researchcontract.OutcomeUncertain, "checkpoint",
				"run commissioned but the initial checkpoint was not saved; retry the same idempotency key to heal"),
			err)
	}
	s.cacheMaxConc(round.ID, int(allow.MaxConcurrent))
	s.tracker(round.ID)
	return out, nil
}

// sameCommission reports whether a stored round matches a commission replay
// on every digest field except the absolute deadline (which legitimately
// drifts with the clock between the original and the replay).
func sameCommission(round store.Round, intent string, profile int64, scope store.RoundScope, limits store.RoundAllowance) bool {
	if round.Intent != intent || round.Outcome != "research_run" ||
		round.ProfileVersion != profile || round.Limits != limits {
		return false
	}
	want, err := json.Marshal(scope)
	if err != nil {
		return false
	}
	got, err := json.Marshal(round.Scope)
	if err != nil {
		return false
	}
	return string(want) == string(got)
}

// healCommission finishes a replayed commission: activate when still queued,
// re-journal the deterministic commission record and save the checkpoint when
// missing. First write wins everywhere, so replays never duplicate state.
func (s *Supervisor) healCommission(ctx context.Context, round store.Round, rec commissionRecord) (CommissionOutput, error) {
	out := CommissionOutput{
		RunID: round.ID, Generation: round.Generation, State: string(round.State),
		ProfileVersion: round.ProfileVersion,
		Limits: researchcontract.Allowance{Requests: round.Limits.Requests, Items: round.Limits.Items,
			Tools: round.Limits.Tools, Turns: round.Limits.Turns},
		Deadline: round.Deadline,
	}
	if round.State == store.RoundQueued {
		// Owner-only activation: the replay carries the original owner actor.
		activated, err := s.db.ActivateRound(ctx, round.Actor, round.ID)
		if err != nil {
			return out, mapStoreError(err)
		}
		round = activated
		out.Generation, out.State = round.Generation, string(round.State)
	}
	now := time.Now()
	if err := s.journalAppend(ctx, researchcontract.Event{
		ID: commissionedEventID(round.ID), RunID: round.ID, Kind: SuperviseEventCommissioned,
		Outcome: researchcontract.OutcomeOK, Payload: mustPayload(rec),
		ObservedAt: now, RecordedAt: now,
	}); err != nil {
		return out, errors.Join(mapStoreError(err),
			fmt.Errorf("commission replay could not heal the commission record"))
	}
	stored, err := s.commissionRecord(ctx, round.ID)
	if err != nil {
		return out, mapStoreError(err)
	}
	out.RubricVersion, out.MaxConcurrent = stored.RubricVersion, stored.MaxConcurrent
	if _, err := s.loadCheckpoint(ctx, round.ID); errors.Is(err, store.ErrNotFound) {
		if err := s.saveInitialCheckpoint(ctx, round, round.ProfileVersion, stored.RubricVersion); err != nil {
			return out, errors.Join(mapStoreError(err),
				fmt.Errorf("commission replay could not heal the checkpoint"))
		}
	} else if err != nil {
		return out, mapStoreError(err)
	}
	s.cacheMaxConc(round.ID, stored.MaxConcurrent)
	s.tracker(round.ID)
	return out, nil
}

func (s *Supervisor) saveInitialCheckpoint(ctx context.Context, round store.Round, profile int64, rubric string) error {
	ledger, err := s.auth.Usage(ctx, round.ID)
	if err != nil {
		return err
	}
	remaining, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	return s.saveCheckpoint(ctx, round.ID, researchcontract.Checkpoint{
		ProfileVersion: profile, RubricVersion: rubric,
		RemainingAllowance: remaining,
		Generation:         round.Generation, UpdatedAt: time.Now(),
	})
}

// saveCheckpoint persists one checkpoint through a ResearchWrite.
func (s *Supervisor) saveCheckpoint(ctx context.Context, runID string, cp researchcontract.Checkpoint) error {
	return s.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		return store.SaveRunCheckpoint(ctx, db, runID, cp)
	})
}

// loadCheckpoint reads the stored checkpoint, or store.ErrNotFound.
func (s *Supervisor) loadCheckpoint(ctx context.Context, runID string) (researchcontract.Checkpoint, error) {
	var cp researchcontract.Checkpoint
	var loadErr error
	if err := s.db.Read(ctx, func(r store.Reader) error {
		cp, loadErr = store.GetRunCheckpoint(ctx, r, runID)
		return nil
	}); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	return cp, loadErr
}

// Checkpoint returns the stored resumable state. It is a pure read: a missing
// checkpoint is not_found, never a silent rebuild.
func (s *Supervisor) Checkpoint(ctx context.Context, runID string) (researchcontract.Checkpoint, error) {
	cp, err := s.loadCheckpoint(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return researchcontract.Checkpoint{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
			"checkpoint", "no checkpoint stored for run "+runID)
	}
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	return cp, nil
}

// rebuildCheckpoint reconstructs resumable state from durable sources when the
// checkpoint row is missing: versions from the commission record, evidence
// from the journaled observations, unresolved attempts from the ledger, the
// remaining allowance from current usage. Saved-record and next-work lists
// cannot be rebuilt and restart empty — honestly, not silently: the recovery
// is journaled by the caller.
func (s *Supervisor) rebuildCheckpoint(ctx context.Context, round store.Round) (researchcontract.Checkpoint, error) {
	rec, err := s.commissionRecord(ctx, round.ID)
	if err != nil {
		return researchcontract.Checkpoint{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "run",
			"commission incomplete for run "+round.ID+"; re-commission with the same idempotency key to heal")
	}
	evidence := []string{}
	seen := map[string]bool{}
	cursor := ""
	for {
		events, next, err := s.journal.List(ctx, round.ID, cursor, 100)
		if err != nil {
			return researchcontract.Checkpoint{}, err
		}
		for _, e := range events {
			if e.Kind != SuperviseEventObserved {
				continue
			}
			var payload struct {
				ObservationID string `json:"observationId"`
				CaptureID     string `json:"captureId"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				continue
			}
			for _, id := range []string{payload.ObservationID, payload.CaptureID} {
				if id != "" && !seen[id] {
					seen[id] = true
					evidence = append(evidence, id)
				}
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	unresolved, err := s.unresolvedAttempts(ctx, round.ID)
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	ledger, err := s.auth.Usage(ctx, round.ID)
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	remaining, err := json.Marshal(ledger)
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	return researchcontract.Checkpoint{
		ProfileVersion: round.ProfileVersion, RubricVersion: rec.RubricVersion,
		EvidenceIDs: evidence, UnresolvedAttempts: unresolved,
		RemainingAllowance: remaining, Generation: round.Generation, UpdatedAt: time.Now(),
	}, nil
}

func (s *Supervisor) unresolvedAttempts(ctx context.Context, runID string) ([]researchcontract.UnresolvedAttempt, error) {
	// Gather ids first, then reconcile outside the read: Store.Read holds a
	// pooled connection and Authority reads take their own, so nesting them
	// deadlocks the pool.
	var ids []string
	if err := s.db.Read(ctx, func(r store.Reader) error {
		attempts, err := store.UncertainAttemptsTx(ctx, r, runID)
		if err != nil {
			return err
		}
		for _, a := range attempts {
			ids = append(ids, a.ID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]researchcontract.UnresolvedAttempt, 0, len(ids))
	for _, id := range ids {
		reason := "uncertain dispatch; reconcile before continuing"
		if rec, err := s.auth.ReconciliationFor(ctx, id); err == nil {
			reason = "settled: " + rec.State
		}
		out = append(out, researchcontract.UnresolvedAttempt{AttemptID: id, Reason: reason})
	}
	return out, nil
}

// refreshCheckpoint reloads (rebuilding when missing), refreshes the derived
// fields — unresolved attempts, remaining allowance, generation — and saves.
func (s *Supervisor) refreshCheckpoint(ctx context.Context, round store.Round, merge func(*researchcontract.Checkpoint)) (researchcontract.Checkpoint, error) {
	s.ckptMu.Lock()
	defer s.ckptMu.Unlock()
	cp, err := s.loadCheckpoint(ctx, round.ID)
	if errors.Is(err, store.ErrNotFound) {
		cp, err = s.rebuildCheckpoint(ctx, round)
	}
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	unresolved, err := s.unresolvedAttempts(ctx, round.ID)
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	ledger, err := s.auth.Usage(ctx, round.ID)
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	remaining, err := json.Marshal(ledger)
	if err != nil {
		return researchcontract.Checkpoint{}, err
	}
	cp.UnresolvedAttempts = unresolved
	cp.RemainingAllowance = remaining
	cp.Generation = round.Generation
	cp.UpdatedAt = time.Now()
	if merge != nil {
		merge(&cp)
	}
	if err := s.saveCheckpoint(ctx, round.ID, cp); err != nil {
		return researchcontract.Checkpoint{}, err
	}
	return cp, nil
}

// DispatchInput scopes one research execution (T06 §8 research_execute) to
// the active run. Generation 0 means the current generation; a presented
// generation is authority-checked.
type DispatchInput struct {
	RunID          string
	Generation     int64
	Kind           researchcontract.ExecuteKind
	Request        researchcontract.RequestDescriptor
	Bounds         researchcontract.Bounds
	IdempotencyKey string
}

// DispatchOutput is one settled execution. Replayed marks an exact replay
// served from the stored result without re-executing.
type DispatchOutput struct {
	AttemptID     string
	Outcome       researchcontract.Outcome
	ObservationID string
	CaptureID     string
	Receipt       researchcontract.ExecutionReceipt
	Usage         researchcontract.ExecuteUsage
	Replayed      bool
}

// dispatchResult is the durable terminal record behind a settled dispatch.
type dispatchResult struct {
	Outcome       researchcontract.Outcome          `json:"outcome"`
	ObservationID string                            `json:"observationId,omitempty"`
	CaptureID     string                            `json:"captureId,omitempty"`
	Receipt       researchcontract.ExecutionReceipt `json:"receipt"`
	Usage         researchcontract.ExecuteUsage     `json:"usage"`
}

func executeOperation(kind researchcontract.ExecuteKind) (string, bool) {
	switch kind {
	case researchcontract.ExecuteSearch:
		return store.RoundResearchSearch, true
	case researchcontract.ExecuteFetch:
		return store.RoundResearchFetch, true
	case researchcontract.ExecuteBrowse:
		return store.RoundResearchBrowse, true
	case researchcontract.ExecuteAPI:
		return store.RoundResearchAPI, true
	case researchcontract.ExecuteExec:
		return store.RoundResearchExec, true
	}
	return "", false
}

// Dispatch runs one bounded research operation: authority check, stable
// reservation, fenced dispatch, execution, observation and checkpoint. Every
// controlled external call flows through here (T17's research_execute handler
// calls it); retries are new keys, never blind replays.
func (s *Supervisor) Dispatch(ctx context.Context, in DispatchInput) (DispatchOutput, error) {
	if s.exec == nil {
		return DispatchOutput{}, ErrNotReady
	}
	op, ok := executeOperation(in.Kind)
	if !ok {
		return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "kind",
			fmt.Sprintf("unknown execution kind %q", in.Kind))
	}
	if !validKey(in.IdempotencyKey, maxIdempotency) {
		return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "idempotencyKey",
			"idempotency key required (1..128 chars, no surrounding space)")
	}
	in.Bounds = clampDispatchBounds(in.Bounds, s.perOp)
	// The exact-request fingerprint doubles as the reservation payload hash:
	// stable, credential-free, and secret-rejecting (T06 §2).
	fingerprint, err := in.Request.Fingerprint()
	if err != nil {
		return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "request",
			"invalid request descriptor: "+err.Error())
	}
	gen := in.Generation
	if gen == 0 {
		round, err := s.db.Round(ctx, in.RunID)
		if err != nil {
			return DispatchOutput{}, mapStoreError(err)
		}
		gen = round.Generation
	}
	if err := s.auth.Check(ctx, researchcontract.CheckInput{RunID: in.RunID, Generation: gen,
		Permission: researchcontract.PermissionResearchDispatch, Now: time.Now()}); err != nil {
		return DispatchOutput{}, err
	}
	round, err := s.db.Round(ctx, in.RunID)
	if err != nil {
		return DispatchOutput{}, mapStoreError(err)
	}

	s.resolveMaxConc(ctx, in.RunID)
	tracker := s.tracker(in.RunID)
	select {
	case tracker.sem <- struct{}{}:
	case <-ctx.Done():
		return DispatchOutput{}, ctx.Err()
	case <-tracker.ctx.Done():
		return DispatchOutput{}, s.fencedError(ctx, in.RunID)
	}
	defer func() { <-tracker.sem }()

	execCtx, cancel, done := s.execContext(ctx, tracker, round.Deadline, s.perOp, in.Bounds.DeadlineMs)
	defer cancel()
	defer close(done)
	tracker.wg.Add(1)
	defer tracker.wg.Done()

	reservation, err := s.auth.Reserve(ctx, in.RunID, op, in.IdempotencyKey, fingerprint)
	if err != nil {
		return DispatchOutput{}, err
	}
	attempt, err := s.db.RoundAttempt(ctx, reservation.AttemptID)
	if err != nil {
		return DispatchOutput{}, mapStoreError(err)
	}
	// Replay semantics by attempt state: adopted holds proceed, terminal
	// results serve from storage, in-flight keys conflict, fenced holds name
	// the fence. Nothing re-executes blindly.
	switch attempt.State {
	case store.AttemptReserved:
	case store.AttemptSucceeded, store.AttemptFailed,
		store.AttemptObservedSuccess, store.AttemptObservedFailure:
		return s.replayDispatch(attempt)
	case store.AttemptDispatched:
		return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeConflict, "idempotencyKey",
			fmt.Sprintf("key %q is already dispatched under attempt %s; observe it instead of re-dispatching",
				in.IdempotencyKey, attempt.ID))
	case store.AttemptUncertain:
		return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeUncertain, "idempotencyKey",
			fmt.Sprintf("key %q is held by uncertain attempt %s; reconcile it before re-dispatching",
				in.IdempotencyKey, attempt.ID))
	default:
		// Cancelled holds settle terminally without a stored result (the
		// failures precedent): same-key redispatch names the fence honestly
		// with no new charge. A pre-claim refusal was never fenced by a
		// stop, so it says so.
		if attempt.State == store.AttemptCancelled && attempt.ErrorCode == store.AbandonedPreClaimCode {
			return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeStale, "generation",
				fmt.Sprintf("key %q was refused before dispatch; re-issue under a new key (no charge held)",
					in.IdempotencyKey))
		}
		return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeStale, "generation",
			fmt.Sprintf("key %q was fenced by a stop; re-issue under a new key", in.IdempotencyKey))
	}

	now := time.Now()
	if err := s.journalAppend(execCtx, researchcontract.Event{
		ID: "dispatch." + attempt.ID, RunID: in.RunID, AttemptID: attempt.ID,
		Kind: SuperviseEventDispatched, RequestFingerprint: fingerprint,
		Outcome: researchcontract.OutcomeOK,
		Payload: mustPayload(struct {
			Operation string `json:"operation"`
			Key       string `json:"requestKey"`
			Gen       int64  `json:"generation"`
		}{op, in.IdempotencyKey, attempt.Generation}),
		ObservedAt: now, RecordedAt: now,
	}); err != nil {
		// Coverage failure before execution: release the hold so nothing is
		// charged for work that never ran, and say so explicitly.
		_ = s.auth.Release(ctx, reservation.ID)
		return DispatchOutput{}, errors.Join(
			researchcontract.NewError(researchcontract.OutcomeUncertain, "journal",
				"dispatch journaled nothing; the reservation was released, retry under a new key"), err)
	}
	if _, err := s.db.MarkRoundDispatched(ctx, in.RunID, attempt.ID); err != nil {
		_ = s.auth.Release(ctx, reservation.ID)
		s.journalFenced(ctx, in.RunID, attempt.ID, in.IdempotencyKey, attempt.Generation)
		return DispatchOutput{}, s.fencedError(ctx, in.RunID)
	}

	out, execErr := s.exec.Execute(execCtx, researchcontract.ExecuteInput{
		Kind: in.Kind, Request: in.Request, Bounds: in.Bounds,
		IdempotencyKey: in.IdempotencyKey, RunID: in.RunID, Generation: attempt.Generation,
	})
	if execErr != nil {
		// A certified pre-claim refusal (T24 F2/F3) unwinds the hold: the
		// executor did no work, so the charge refunds and the key settles
		// terminally instead of stranding dispatched/uncertain. Every other
		// executor failure keeps the stop/reconcile path.
		if cause, ok := researchexecute.RefusedPreClaim(execErr); ok {
			return DispatchOutput{}, s.settleRefused(ctx, in.RunID, attempt.ID, fingerprint, cause)
		}
		return DispatchOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, fingerprint, execErr)
	}
	if !out.Outcome.Valid() {
		return DispatchOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, fingerprint,
			fmt.Errorf("executor returned invalid outcome %q", out.Outcome))
	}
	if fenced := s.fencedError(ctx, in.RunID); fenced != nil || attempt.Generation != s.currentGeneration(ctx, in.RunID) {
		result := dispatchResult{
			Outcome: out.Outcome, ObservationID: out.ObservationID, CaptureID: out.CaptureID,
			Receipt: out.Receipt, Usage: out.Usage,
		}
		raw, _ := json.Marshal(result)
		return DispatchOutput{}, s.settleLate(ctx, in.RunID, attempt, fingerprint, out.Outcome, raw)
	}
	if out.Outcome == researchcontract.OutcomeUncertain {
		return DispatchOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, fingerprint,
			errors.New("executor reported an uncertain outcome"))
	}
	result := dispatchResult{
		Outcome: out.Outcome, ObservationID: out.ObservationID, CaptureID: out.CaptureID,
		Receipt: out.Receipt, Usage: out.Usage,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return DispatchOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, fingerprint, err)
	}
	finisher := round.Actor
	if finisher.Kind == "" || finisher.ID == "" {
		finisher = store.Actor{Kind: "administrator", ID: "owner"}
	}
	success := out.Outcome == researchcontract.OutcomeOK || out.Outcome == researchcontract.OutcomeReused
	errorCode := ""
	if !success {
		errorCode = string(out.Outcome)
	}
	if _, err := s.db.FinishRoundAttempt(ctx, finisher, in.RunID, attempt.ID, success, raw, errorCode); err != nil {
		if errors.Is(err, store.ErrFenced) {
			return DispatchOutput{}, s.settleLate(ctx, in.RunID, attempt, fingerprint, out.Outcome, raw)
		}
		return DispatchOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, fingerprint,
			fmt.Errorf("finish dispatch: %w", err))
	}
	now = time.Now()
	obsErr := s.journalAppend(ctx, researchcontract.Event{
		ID: "observed." + attempt.ID, RunID: in.RunID, AttemptID: attempt.ID,
		Kind: SuperviseEventObserved, RequestFingerprint: fingerprint, Outcome: out.Outcome,
		Payload: mustPayload(struct {
			ObservationID string `json:"observationId,omitempty"`
			CaptureID     string `json:"captureId,omitempty"`
			ReceiptID     string `json:"receiptId,omitempty"`
			Requests      int    `json:"requests"`
			BytesIn       int64  `json:"bytesIn"`
			BytesOut      int64  `json:"bytesOut"`
			Redirects     int    `json:"redirects"`
			Truncated     bool   `json:"truncated,omitempty"`
		}{out.ObservationID, out.CaptureID, out.Receipt.ID, out.Usage.Requests,
			out.Receipt.BytesIn, out.Receipt.BytesOut, out.Usage.Redirects, out.Receipt.Truncated}),
		ObservedAt: now, RecordedAt: now,
	})
	output := DispatchOutput{AttemptID: attempt.ID, Outcome: out.Outcome,
		ObservationID: out.ObservationID, CaptureID: out.CaptureID,
		Receipt: out.Receipt, Usage: out.Usage}
	if _, err := s.refreshCheckpoint(ctx, *roundFresh(ctx, s.db, in.RunID), func(cp *researchcontract.Checkpoint) {
		cp.EvidenceIDs = appendEvidence(cp.EvidenceIDs, out.ObservationID, out.CaptureID)
	}); err != nil {
		// The outcome is durable (attempt + journal); only the checkpoint
		// lags. Explicit, and recovery rebuilds from the journal.
		return output, errors.Join(err,
			errors.New("dispatch settled but the checkpoint lagged"))
	}
	if obsErr != nil {
		return output, errors.Join(obsErr,
			errors.New("dispatch settled but the observation was not journaled"))
	}
	return output, nil
}

// NoteSavedRecords journals one committed records_save batch and merges its
// record ids into the run checkpoint, so run views, counts and reports
// derive saves from durable state instead of dropping them. The batch key
// makes the journal event idempotent: an exact replay records once. A
// journal/checkpoint lag after the commit is explicit, never silent: the
// records stay saved and the caller reports the lag.
func (s *Supervisor) NoteSavedRecords(ctx context.Context, runID, batchKey string, recordIDs []string) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(batchKey) == "" {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "batch",
			"run id and batch key required")
	}
	ids := make([]string, 0, len(recordIDs))
	for _, id := range recordIDs {
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	now := time.Now()
	if err := s.journalAppend(ctx, researchcontract.Event{
		ID: fmt.Sprintf("saved.%s.%s", runID, batchKey), RunID: runID,
		Kind: SuperviseEventSaved, Outcome: researchcontract.OutcomeOK,
		Payload: mustPayload(struct {
			RecordIDs []string `json:"recordIds"`
			Count     int      `json:"count"`
		}{ids, len(ids)}),
		ObservedAt: now, RecordedAt: now,
	}); err != nil {
		return errors.Join(err, errors.New("records saved but the save was not journaled"))
	}
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return errors.Join(mapStoreError(err), errors.New("records saved but the checkpoint lagged"))
	}
	if _, err := s.refreshCheckpoint(ctx, round, func(cp *researchcontract.Checkpoint) {
		cp.SavedRecordIDs = appendEvidence(cp.SavedRecordIDs, ids...)
	}); err != nil {
		return errors.Join(err, errors.New("records saved but the checkpoint lagged"))
	}
	return nil
}

// replayDispatch serves an exact replay from the stored terminal result.
func (s *Supervisor) replayDispatch(attempt store.RoundAttempt) (DispatchOutput, error) {
	var result dispatchResult
	if len(attempt.Result) == 0 {
		return DispatchOutput{}, researchcontract.NewError(researchcontract.OutcomeStale, "generation",
			"attempt "+attempt.ID+" settled without a stored result; re-issue under a new key")
	}
	if err := json.Unmarshal(attempt.Result, &result); err != nil {
		return DispatchOutput{}, err
	}
	return DispatchOutput{AttemptID: attempt.ID, Outcome: result.Outcome,
		ObservationID: result.ObservationID, CaptureID: result.CaptureID,
		Receipt: result.Receipt, Usage: result.Usage, Replayed: true}, nil
}

// execContext bounds one execution: the first of the supervisor default, the
// caller bound and the run deadline, canceled on stop or caller abort. done
// stops the stop-watch goroutine; the caller closes it.
func (s *Supervisor) execContext(ctx context.Context, tracker *runTracker, runDeadline time.Time, def time.Duration, boundMs int64) (context.Context, context.CancelFunc, chan struct{}) {
	deadline := time.Now().Add(def)
	if boundMs > 0 {
		if bound := time.Now().Add(time.Duration(boundMs) * time.Millisecond); bound.Before(deadline) {
			deadline = bound
		}
	}
	if runDeadline.Before(deadline) {
		deadline = runDeadline
	}
	execCtx, cancel := context.WithDeadline(tracker.ctx, deadline)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-tracker.ctx.Done():
			cancel()
		case <-execCtx.Done():
		case <-done:
		}
	}()
	return execCtx, cancel, done
}

// fencedError reports the current fence state: nil while the run owns the
// current generation, else the contract error that applies.
func (s *Supervisor) fencedError(ctx context.Context, runID string) error {
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return mapStoreError(err)
	}
	switch round.State {
	case store.RoundRunning:
		return nil
	case store.RoundStopping, store.RoundPaused, store.RoundAwaitingInput:
		return researchcontract.NewError(researchcontract.OutcomeStopped, "run",
			fmt.Sprintf("run %s at generation %d; resume before new work", round.State, round.Generation))
	case store.RoundQueued:
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "run", "run not activated")
	default:
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "run",
			fmt.Sprintf("run terminal (%s)", round.State))
	}
}

func (s *Supervisor) currentGeneration(ctx context.Context, runID string) int64 {
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return -1
	}
	return round.Generation
}

func roundFresh(ctx context.Context, db *store.Store, runID string) *store.Round {
	round, err := db.Round(ctx, runID)
	if err != nil {
		return &store.Round{}
	}
	return &round
}

func appendEvidence(have []string, ids ...string) []string {
	seen := map[string]bool{}
	for _, id := range have {
		seen[id] = true
	}
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			have = append(have, id)
		}
	}
	return have
}

func uncertainReason(err error) string {
	switch {
	case err == nil:
		return "unconfirmed"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "unconfirmed"
	}
}

// settleUncertain journals an unverified outcome and leaves the attempt for
// the stop/reconcile path. It never invents a terminal state.
func (s *Supervisor) settleUncertain(ctx context.Context, runID, attemptID, fingerprint string, cause error) error {
	reason := uncertainReason(cause)
	now := time.Now()
	_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
		ID: "uncertain." + attemptID + "." + reason, RunID: runID, AttemptID: attemptID,
		Kind: superviseEventUncertain, RequestFingerprint: fingerprint,
		Outcome: researchcontract.OutcomeUncertain,
		Payload: mustPayload(struct {
			Reason string `json:"reason"`
		}{reason}),
		ObservedAt: now, RecordedAt: now,
	})
	return researchcontract.NewError(researchcontract.OutcomeUncertain, "attempt",
		fmt.Sprintf("attempt %s outcome unknown (%s); it settles via stop/reconcile", attemptID, reason))
}

// settleRefused unwinds a dispatch the executor certified it refused pre-claim
// (T24 F2/F3): the hold abandons with a full refund, the refusal journals as a
// run.dispatch_refused companion to the dispatch event, and the caller sees
// the refusal cause verbatim (invalid descriptor, exhausted budget). No
// checkpoint touch is needed: the refund restores exactly the pre-dispatch
// allowance the checkpoint already snapshots, and a cancelled attempt never
// lists as unresolved. If the abandon loses a race (Stop fenced the attempt
// first), the fence owns it and the dispatch settles uncertain as before.
func (s *Supervisor) settleRefused(ctx context.Context, runID, attemptID, fingerprint string, cause error) error {
	finisher := store.Actor{Kind: "administrator", ID: "owner"}
	if round, err := s.db.Round(ctx, runID); err == nil && round.Actor.Kind != "" && round.Actor.ID != "" {
		finisher = round.Actor
	}
	if err := s.db.AbandonDispatchedRoundAttempt(ctx, finisher, runID, attemptID); err != nil {
		return s.settleUncertain(ctx, runID, attemptID, fingerprint, cause)
	}
	outcome := researchcontract.OutcomeInvalid
	var cerr *researchcontract.Error
	if errors.As(cause, &cerr) && cerr.Code.Valid() {
		outcome = cerr.Code
	}
	now := time.Now()
	_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
		ID: "refused." + attemptID, RunID: runID, AttemptID: attemptID,
		Kind: SuperviseEventDispatchRefused, RequestFingerprint: fingerprint,
		Outcome: outcome,
		Payload: mustPayload(struct {
			Reason string `json:"reason"`
		}{"executor pre-claim refused; supervisor hold released with a full refund"}),
		ObservedAt: now, RecordedAt: now,
	})
	return cause
}

// settleLate records a completion that arrived past the fence as evidence
// only: the late result plus a late_observation event. It never writes a
// current result, checkpoint line or allowance line.
func (s *Supervisor) settleLate(ctx context.Context, runID string, attempt store.RoundAttempt, fingerprint string, outcome researchcontract.Outcome, raw json.RawMessage) error {
	if len(raw) == 0 || !json.Valid(raw) {
		raw = json.RawMessage(`{"outcome":"outcome_uncertain"}`)
	}
	_ = s.db.RecordLateRoundResult(context.WithoutCancel(ctx), runID, attempt.ID, raw)
	now := time.Now()
	_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
		ID: "late." + attempt.ID, RunID: runID, AttemptID: attempt.ID,
		Kind: researchcontract.EventLateObservation, RequestFingerprint: fingerprint,
		Outcome: outcome,
		Payload: mustPayload(struct {
			Reason string `json:"reason"`
		}{"fenced_after_execute"}),
		ObservedAt: now, RecordedAt: now,
	})
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return mapStoreError(err)
	}
	if round.State != store.RoundRunning {
		return researchcontract.NewError(researchcontract.OutcomeStopped, "run",
			fmt.Sprintf("run stopped during dispatch; completion kept as late evidence (generation %d)", round.Generation))
	}
	return researchcontract.NewError(researchcontract.OutcomeStale, "generation",
		fmt.Sprintf("generation rotated during dispatch; completion kept as late evidence (current %d)", round.Generation))
}

func (s *Supervisor) journalFenced(ctx context.Context, runID, attemptID, key string, gen int64) {
	now := time.Now()
	_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
		ID: "fenced." + attemptID, RunID: runID, AttemptID: attemptID,
		Kind: SuperviseEventDispatchFenced, Outcome: researchcontract.OutcomeStopped,
		Payload: mustPayload(struct {
			Key string `json:"requestKey"`
			Gen int64  `json:"generation"`
		}{key, gen}),
		ObservedAt: now, RecordedAt: now,
	})
}

// TurnInput scopes one model turn. Generation 0 means current. Empty Brief
// and Evidence are built from the checkpoint, the commission record and
// queued owner steers.
type TurnInput struct {
	RunID      string
	Generation int64
	RequestKey string
	Brief      string
	Evidence   string
}

// TurnOutput reports one settled turn. Replayed marks an exact replay served
// from the stored result.
type TurnOutput struct {
	AttemptID string
	ThreadID  string
	TurnID    string
	Status    string
	BriefUsed string
	NextWork  []string
	Replayed  bool
}

// turnResult is the durable terminal record behind a settled turn.
type turnResult struct {
	ThreadID string   `json:"threadId"`
	TurnID   string   `json:"turnId"`
	Status   string   `json:"status"`
	NextWork []string `json:"nextWork,omitempty"`
}

// ContinueRun runs one model turn with durable continuation: reserve, dispatch,
// run (resuming the stored thread when one exists), bind the remote ids,
// observe, finish and checkpoint. When the stored conversation is unavailable
// the turn starts fresh from the checkpoint and the recovery shows in both
// the brief and the journal.
func (s *Supervisor) ContinueRun(ctx context.Context, agent store.Actor, in TurnInput) (TurnOutput, error) {
	runner := s.turnRunner()
	if runner == nil {
		return TurnOutput{}, ErrNotReady
	}
	if agent.Kind != "agent" || agent.ID == "" {
		return TurnOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "agent",
			"delegated agent actor required")
	}
	if !validKey(in.RequestKey, maxIdempotency) {
		return TurnOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "idempotencyKey",
			"idempotency key required (1..128 chars, no surrounding space)")
	}
	if len(in.Brief) > maxTurnBrief || len(in.Evidence) > maxTurnEvidence {
		return TurnOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "brief",
			fmt.Sprintf("brief/evidence exceed %d/%d chars", maxTurnBrief, maxTurnEvidence))
	}
	gen := in.Generation
	round, err := s.db.Round(ctx, in.RunID)
	if err != nil {
		return TurnOutput{}, mapStoreError(err)
	}
	if gen == 0 {
		gen = round.Generation
	}
	// Liveness/delegation gate; exact turn allowance is enforced at reserve.
	if err := s.auth.Check(ctx, researchcontract.CheckInput{RunID: in.RunID, Generation: gen,
		Permission: researchcontract.PermissionResearchDispatch, Now: time.Now()}); err != nil {
		return TurnOutput{}, err
	}

	cp, err := s.loadCheckpoint(ctx, in.RunID)
	if errors.Is(err, store.ErrNotFound) {
		cp, err = s.rebuildCheckpoint(ctx, round)
	}
	if err != nil {
		return TurnOutput{}, err
	}
	brief := in.Brief
	if brief == "" {
		brief, err = s.buildTurnBrief(ctx, round, cp)
		if err != nil {
			return TurnOutput{}, err
		}
	}
	evidence := in.Evidence
	if evidence == "" {
		evidence = buildTurnEvidence(cp)
	}
	var threadID string
	if err := s.db.Read(ctx, func(r store.Reader) error {
		threadID, err = store.LatestRoundThreadTx(ctx, r, in.RunID)
		return err
	}); err != nil {
		return TurnOutput{}, mapStoreError(err)
	}
	recovered := threadID == "" && checkpointHasContent(cp)

	cost, ok := store.RoundOperationCost(store.RoundCodexTurn)
	if !ok {
		return TurnOutput{}, errors.New("rounds: codex turn cost missing")
	}
	// The turn brief is supervisor-derived state (checkpoint, allowance,
	// steers), not caller intent, so it stays out of the replay digest: a
	// retry or crash-replay with advanced derived state still addresses the
	// same turn slot. The idempotency key alone identifies the turn.
	attempt, _, err := s.db.ReserveRoundAttempt(ctx, agent, in.RunID, store.RoundAttemptInput{
		RequestKey: in.RequestKey, Operation: store.RoundCodexTurn,
		ResourceID: store.ResearchAuthorityResource, Cost: cost,
	})
	if err != nil {
		return TurnOutput{}, s.mapReserveError(ctx, in.RunID, in.RequestKey, err)
	}
	switch attempt.State {
	case store.AttemptReserved:
	case store.AttemptSucceeded, store.AttemptFailed,
		store.AttemptObservedSuccess, store.AttemptObservedFailure:
		return s.replayTurn(ctx, attempt, brief)
	case store.AttemptDispatched:
		return TurnOutput{}, researchcontract.NewError(researchcontract.OutcomeConflict, "idempotencyKey",
			fmt.Sprintf("key %q already runs turn attempt %s; observe it instead of re-running",
				in.RequestKey, attempt.ID))
	case store.AttemptUncertain:
		return TurnOutput{}, researchcontract.NewError(researchcontract.OutcomeUncertain, "idempotencyKey",
			fmt.Sprintf("key %q is held by uncertain turn %s; reconcile it before re-running",
				in.RequestKey, attempt.ID))
	default:
		return TurnOutput{}, researchcontract.NewError(researchcontract.OutcomeStale, "generation",
			fmt.Sprintf("key %q was fenced by a stop; re-issue under a new key", in.RequestKey))
	}

	now := time.Now()
	if err := s.journalAppend(ctx, researchcontract.Event{
		ID: "dispatch." + attempt.ID, RunID: in.RunID, AttemptID: attempt.ID,
		Kind: SuperviseEventDispatched, Outcome: researchcontract.OutcomeOK,
		Payload: mustPayload(struct {
			Operation string `json:"operation"`
			Key       string `json:"requestKey"`
			Gen       int64  `json:"generation"`
			Resumed   bool   `json:"resumedThread"`
		}{store.RoundCodexTurn, in.RequestKey, attempt.Generation, threadID != ""}),
		ObservedAt: now, RecordedAt: now,
	}); err != nil {
		_ = s.db.ReleaseRoundAttempt(ctx, agent, attempt.ID)
		return TurnOutput{}, errors.Join(
			researchcontract.NewError(researchcontract.OutcomeUncertain, "journal",
				"turn journaled nothing; the reservation was released, retry under a new key"), err)
	}
	if _, err := s.db.MarkRoundDispatched(ctx, in.RunID, attempt.ID); err != nil {
		_ = s.db.ReleaseRoundAttempt(ctx, agent, attempt.ID)
		s.journalFenced(ctx, in.RunID, attempt.ID, in.RequestKey, attempt.Generation)
		return TurnOutput{}, s.fencedError(ctx, in.RunID)
	}

	// Bind the active tool-session credential for the live turn: without it
	// the model's tool calls cannot authenticate, so issuance failure
	// settles the attempt uncertain instead of running blind. The
	// capability is revoked once the turn settles (best effort; finishing
	// the attempt fences it regardless).
	capability, err := s.db.IssueRoundToolCapability(ctx, in.RunID, attempt.ID, agent.ID)
	if err != nil {
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "",
			fmt.Errorf("issue turn capability: %w", err))
	}
	defer func() { _ = s.db.RevokeRoundToolCapability(context.WithoutCancel(ctx), capability) }()

	s.resolveMaxConc(ctx, in.RunID)
	tracker := s.tracker(in.RunID)
	tracker.wg.Add(1)
	defer tracker.wg.Done()
	turnCtx, cancel, done := s.execContext(ctx, tracker, round.Deadline, s.turnBudget, 0)
	defer cancel()
	defer close(done)
	rout, runErr := runner.RunTurn(turnCtx, agent, RunnerTurnInput{
		RunID: in.RunID, RequestKey: in.RequestKey, AttemptID: attempt.ID,
		ThreadID: threadID, Brief: brief, Evidence: evidence, Generation: attempt.Generation,
		Capability: capability,
	})
	if runErr != nil {
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "", runErr)
	}
	switch rout.Status {
	case "completed", "failed", "interrupted", "unknown":
	default:
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "",
			fmt.Errorf("turn runner returned invalid status %q", rout.Status))
	}
	if rout.ThreadID == "" || rout.TurnID == "" {
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "",
			errors.New("turn runner returned no remote ids"))
	}
	if err := s.db.BindRoundThread(ctx, in.RunID, attempt.ID, attempt.Generation, rout.ThreadID); err != nil {
		return TurnOutput{}, s.turnBindFailed(ctx, in.RunID, attempt, err)
	}
	if err := s.db.BindRoundTurn(ctx, in.RunID, attempt.ID, attempt.Generation, rout.ThreadID, rout.TurnID); err != nil {
		return TurnOutput{}, s.turnBindFailed(ctx, in.RunID, attempt, err)
	}
	if recovered {
		now = time.Now()
		_ = s.journalAppend(ctx, researchcontract.Event{
			ID: "recovered." + attempt.ID, RunID: in.RunID, AttemptID: attempt.ID,
			Kind: SuperviseEventRecovered, Outcome: researchcontract.OutcomeOK,
			Payload: mustPayload(struct {
				ThreadID string `json:"threadId"`
			}{rout.ThreadID}),
			ObservedAt: now, RecordedAt: now,
		})
	}
	if fenced := s.fencedError(ctx, in.RunID); fenced != nil || attempt.Generation != s.currentGeneration(ctx, in.RunID) {
		raw, _ := json.Marshal(turnResult{ThreadID: rout.ThreadID, TurnID: rout.TurnID, Status: rout.Status, NextWork: rout.NextWork})
		return TurnOutput{}, s.settleLate(ctx, in.RunID, attempt, "", turnOutcome(rout.Status), raw)
	}
	if rout.Status == "unknown" {
		observed := rout.Evidence
		if len(observed) == 0 {
			observed = json.RawMessage(`{"status":"unknown"}`)
		}
		_ = s.db.ObserveRoundTurn(ctx, in.RunID, attempt.ID, attempt.Generation,
			rout.ThreadID, rout.TurnID, "unknown", observed)
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "",
			errors.New("turn outcome unknown"))
	}
	observed := rout.Evidence
	if len(observed) == 0 {
		observed = mustPayload(struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Status   string `json:"status"`
		}{rout.ThreadID, rout.TurnID, rout.Status})
	}
	if err := s.db.ObserveRoundTurn(ctx, in.RunID, attempt.ID, attempt.Generation,
		rout.ThreadID, rout.TurnID, rout.Status, observed); err != nil {
		if errors.Is(err, store.ErrFenced) {
			raw, _ := json.Marshal(turnResult{ThreadID: rout.ThreadID, TurnID: rout.TurnID, Status: rout.Status, NextWork: rout.NextWork})
			return TurnOutput{}, s.settleLate(ctx, in.RunID, attempt, "", turnOutcome(rout.Status), raw)
		}
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "",
			fmt.Errorf("observe turn: %w", err))
	}
	result := turnResult{ThreadID: rout.ThreadID, TurnID: rout.TurnID, Status: rout.Status, NextWork: rout.NextWork}
	raw, err := json.Marshal(result)
	if err != nil {
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "", err)
	}
	success := rout.Status == "completed"
	errorCode := ""
	if !success {
		errorCode = rout.Status
	}
	if _, err := s.db.FinishRoundAttempt(ctx, round.Actor, in.RunID, attempt.ID, success, raw, errorCode); err != nil {
		if errors.Is(err, store.ErrFenced) {
			return TurnOutput{}, s.settleLate(ctx, in.RunID, attempt, "", turnOutcome(rout.Status), raw)
		}
		return TurnOutput{}, s.settleUncertain(ctx, in.RunID, attempt.ID, "",
			fmt.Errorf("finish turn: %w", err))
	}
	now = time.Now()
	obsErr := s.journalAppend(ctx, researchcontract.Event{
		ID:    "turn." + rout.ThreadID + "." + rout.TurnID + "." + rout.Status,
		RunID: in.RunID, AttemptID: attempt.ID, Kind: superviseEventTurnObserved,
		Outcome: turnOutcome(rout.Status),
		Payload: mustPayload(struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Status   string `json:"status"`
			Via      string `json:"via"`
		}{rout.ThreadID, rout.TurnID, rout.Status, "supervisor-continue"}),
		ObservedAt: now, RecordedAt: now,
	})
	output := TurnOutput{AttemptID: attempt.ID, ThreadID: rout.ThreadID, TurnID: rout.TurnID,
		Status: rout.Status, BriefUsed: brief, NextWork: rout.NextWork}
	fresh := roundFresh(ctx, s.db, in.RunID)
	refreshed, err := s.refreshCheckpoint(ctx, *fresh, func(cp *researchcontract.Checkpoint) {
		cp.NextWork = rout.NextWork
	})
	if err != nil {
		return output, errors.Join(err, errors.New("turn settled but the checkpoint lagged"))
	}
	now = time.Now()
	ckptErr := s.journalAppend(ctx, researchcontract.Event{
		ID:    fmt.Sprintf("checkpoint.%s.%d.%d", in.RunID, refreshed.Generation, now.UnixNano()),
		RunID: in.RunID, AttemptID: attempt.ID, Kind: SuperviseEventCheckpointed,
		Outcome: researchcontract.OutcomeOK,
		Payload: mustPayload(struct {
			Gen int64 `json:"generation"`
		}{refreshed.Generation}),
		ObservedAt: now, RecordedAt: now,
	})
	if obsErr != nil {
		return output, errors.Join(obsErr, errors.New("turn settled but the observation was not journaled"))
	}
	if ckptErr != nil {
		return output, errors.Join(ckptErr, errors.New("turn settled and checkpointed but the checkpoint was not journaled"))
	}
	return output, nil
}

func (s *Supervisor) turnBindFailed(ctx context.Context, runID string, attempt store.RoundAttempt, cause error) error {
	if errors.Is(cause, store.ErrFenced) {
		raw, _ := json.Marshal(turnResult{Status: "unknown"})
		return s.settleLate(ctx, runID, attempt, "", researchcontract.OutcomeUncertain, raw)
	}
	return s.settleUncertain(ctx, runID, attempt.ID, "", fmt.Errorf("bind remote ids: %w", cause))
}

func turnOutcome(status string) researchcontract.Outcome {
	switch status {
	case "completed":
		return researchcontract.OutcomeOK
	case "interrupted":
		return researchcontract.OutcomeStopped
	case "failed":
		return researchcontract.OutcomeInvalid
	default:
		return researchcontract.OutcomeUncertain
	}
}

// replayTurn serves an exact turn replay from the stored terminal result.
func (s *Supervisor) replayTurn(ctx context.Context, attempt store.RoundAttempt, brief string) (TurnOutput, error) {
	var result turnResult
	if len(attempt.Result) == 0 {
		return TurnOutput{}, researchcontract.NewError(researchcontract.OutcomeStale, "generation",
			"turn "+attempt.ID+" settled without a stored result; re-issue under a new key")
	}
	if err := json.Unmarshal(attempt.Result, &result); err != nil {
		return TurnOutput{}, err
	}
	return TurnOutput{AttemptID: attempt.ID, ThreadID: result.ThreadID, TurnID: result.TurnID,
		Status: result.Status, BriefUsed: brief, NextWork: result.NextWork, Replayed: true}, nil
}

func (s *Supervisor) mapReserveError(ctx context.Context, runID, key string, err error) error {
	switch {
	case errors.Is(err, store.ErrRoundIdempotencyConflict):
		return researchcontract.NewError(researchcontract.OutcomeConflict, "idempotencyKey",
			fmt.Sprintf("key %q already reserved with a different brief; re-issue under a new key", key))
	case errors.Is(err, store.ErrAllowance):
		return researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
			string(researchcontract.AuthorityAllowance), "turn allowance exhausted for run "+runID)
	case errors.Is(err, store.ErrUncertain):
		return researchcontract.NewError(researchcontract.OutcomeUncertain, "idempotencyKey",
			fmt.Sprintf("key %q is held by an uncertain turn; reconcile it before re-running", key))
	case errors.Is(err, store.ErrFenced):
		return s.fencedError(ctx, runID)
	case errors.Is(err, store.ErrExpired):
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityDeadline), "run deadline passed")
	case errors.Is(err, store.ErrNotFound):
		return researchcontract.NewError(researchcontract.OutcomeNotFound, "run", "unknown run "+runID)
	case errors.Is(err, store.ErrInvalid):
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "turn", "invalid turn reservation: "+err.Error())
	}
	return err
}

func checkpointHasContent(cp researchcontract.Checkpoint) bool {
	return len(cp.EvidenceIDs) > 0 || len(cp.SavedRecordIDs) > 0 ||
		len(cp.UnresolvedAttempts) > 0 || len(cp.NextWork) > 0
}

func truncateRunes(value string, max int) string {
	if len(value) <= max {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// buildTurnBrief assembles the supervisor-owned continuation brief from
// durable state only: never model prose, never an in-memory transcript. T17
// owns turn instructions; this supplies the facts.
func (s *Supervisor) buildTurnBrief(ctx context.Context, round store.Round, cp researchcontract.Checkpoint) (string, error) {
	var b strings.Builder
	rec, err := s.commissionRecord(ctx, round.ID)
	if err != nil {
		return "", err
	}
	if rec.Brief != "" {
		b.WriteString("Commissioned brief:\n" + truncateRunes(rec.Brief, 8000) + "\n\n")
	}
	if len(cp.NextWork) > 0 {
		b.WriteString("Next useful work:\n")
		for _, w := range cp.NextWork {
			b.WriteString("- " + truncateRunes(w, 500) + "\n")
		}
		b.WriteString("\n")
	}
	if len(cp.UnresolvedAttempts) > 0 {
		b.WriteString("Unresolved attempts:\n")
		for _, u := range cp.UnresolvedAttempts {
			b.WriteString("- " + u.AttemptID + ": " + truncateRunes(u.Reason, 300) + "\n")
		}
		b.WriteString("\n")
	}
	var ledger researchcontract.UsageLedger
	if len(cp.RemainingAllowance) > 0 {
		_ = json.Unmarshal(cp.RemainingAllowance, &ledger)
	}
	remaining := researchcontract.Allowance{
		Requests: ledger.Enforced.Requests - ledger.Reserved.Requests - ledger.Observed.Requests,
		Items:    ledger.Enforced.Items - ledger.Reserved.Items - ledger.Observed.Items,
		Tools:    ledger.Enforced.Tools - ledger.Reserved.Tools - ledger.Observed.Tools,
		Turns:    ledger.Enforced.Turns - ledger.Reserved.Turns - ledger.Observed.Turns,
	}
	fmt.Fprintf(&b, "Remaining allowance: requests=%d items=%d tools=%d turns=%d unknown=%v\n\n",
		remaining.Requests, remaining.Items, remaining.Tools, remaining.Turns, ledger.Unknown)
	steers, err := s.steerMessages(ctx, round.ID, 5)
	if err != nil {
		return "", err
	}
	for _, st := range steers {
		fmt.Fprintf(&b, "Owner steer (revision %d):\n%s\n\n", st.Revision, truncateRunes(st.Body, 2000))
	}
	var threadID string
	_ = s.db.Read(ctx, func(r store.Reader) error {
		threadID, _ = store.LatestRoundThreadTx(ctx, r, round.ID)
		return nil
	})
	if threadID == "" && checkpointHasContent(cp) {
		b.WriteString("Recovery: the previous conversation context is unavailable; " +
			"continue from this checkpoint and shared memory, not from recalled prose.\n")
	}
	return truncateRunes(b.String(), maxTurnBrief), nil
}

func buildTurnEvidence(cp researchcontract.Checkpoint) string {
	var b strings.Builder
	for _, id := range cp.EvidenceIDs {
		b.WriteString("evidence:" + id + "\n")
	}
	for _, id := range cp.SavedRecordIDs {
		b.WriteString("saved:" + id + "\n")
	}
	return truncateRunes(b.String(), maxTurnEvidence)
}

// Steer states: the durable acknowledgment of one steering message.
const (
	SteerApplied   = "applied"   // steered into the live turn, verified
	SteerQueued    = "queued"    // durable; the next turn carries it
	SteerDuplicate = "duplicate" // idempotent replay; see Applied
	SteerPaused    = "paused"    // correction fenced; unknown work remains, owner resumes
)

// SteerInput carries one owner steering message. Generation is the presented
// control generation and is always authority-checked (compare-and-swap: a
// stale view refreshes instead of steering the wrong turn). Correction marks
// changed facts or permissions, which rotate authority.
type SteerInput struct {
	Actor          store.Actor
	RunID          string
	Generation     int64
	Body           string
	IdempotencyKey string
	Correction     bool
}

// SteerOutput acknowledges one steering message.
type SteerOutput struct {
	Revision int64
	State    string
	Applied  bool // live-turn application confirmed (also served on replays)
	Live     bool // this call steered the live turn
}

// steerMessage is the durable steering record behind run.steer_received.
type steerMessage struct {
	Revision   int64  `json:"revision"`
	Body       string `json:"body"`
	Correction bool   `json:"correction,omitempty"`
	By         string `json:"by,omitempty"`
}

func steerReceivedID(runID, key string) string { return "steer." + runID + "." + key }
func steerAppliedID(runID, key string) string  { return "steer." + runID + "." + key + ".applied" }

// Steer records one owner message durably, then applies it: into the live
// turn when one is in flight and the conversation path is wired, else queued
// for the next turn. Corrections rotate authority first. Nothing is silently
// ignored: every path journals its acknowledgment.
func (s *Supervisor) Steer(ctx context.Context, in SteerInput) (SteerOutput, error) {
	if strings.TrimSpace(in.Body) == "" || len(in.Body) > maxSteerBody {
		return SteerOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "body",
			fmt.Sprintf("steering body required (1..%d chars, non-blank)", maxSteerBody))
	}
	if !validKey(in.IdempotencyKey, maxIdempotency) {
		return SteerOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "idempotencyKey",
			"idempotency key required (1..128 chars, no surrounding space)")
	}
	if in.Generation < 1 {
		return SteerOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "generation",
			"presented control generation required")
	}
	// Steering is an owner action (T10 owner-auths the endpoint). The
	// supervisor authority is owner-bound, so the caller gate is explicit
	// here while Check validates run/generation/liveness below.
	if err := requireOwner(in.Actor); err != nil {
		s.journalSteerRejected(ctx, in, err)
		return SteerOutput{}, err
	}
	if err := s.auth.Check(ctx, researchcontract.CheckInput{RunID: in.RunID, Generation: in.Generation,
		Permission: researchcontract.PermissionRunControl, Now: time.Now()}); err != nil {
		s.journalSteerRejected(ctx, in, err)
		return SteerOutput{}, err
	}

	revision, replay, err := s.receiveSteer(ctx, in)
	if err != nil {
		return SteerOutput{}, err
	}
	if replay {
		applied := s.steerApplied(ctx, in.RunID, in.IdempotencyKey)
		return SteerOutput{Revision: revision, State: SteerDuplicate, Applied: applied}, nil
	}
	if in.Correction {
		return s.applyCorrection(ctx, in, revision)
	}
	return s.applySteer(ctx, in, revision)
}

// journalSteerRejected journals a control-check failure with its code, field
// and detail (T12 contract). Best effort: the rejection itself is the result.
func (s *Supervisor) journalSteerRejected(ctx context.Context, in SteerInput, checkErr error) {
	var cerr *researchcontract.Error
	if !errors.As(checkErr, &cerr) {
		return
	}
	if cerr.Code == researchcontract.OutcomeNotFound {
		return // no run to journal against
	}
	now := time.Now()
	_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
		ID: "steer-rejected." + in.RunID + "." + in.IdempotencyKey, RunID: in.RunID,
		Kind: superviseEventSteerRejected, Outcome: cerr.Code,
		Payload: mustPayload(struct {
			Code   string `json:"code"`
			Field  string `json:"field"`
			Detail string `json:"detail"`
			Key    string `json:"requestKey"`
		}{string(cerr.Code), cerr.Field, cerr.Detail, in.IdempotencyKey}),
		ObservedAt: now, RecordedAt: now,
	})
}

// receiveSteer durably records the message with its revision inside one
// immediate transaction, so concurrent steers take distinct revisions and
// replays return the original revision. It returns (revision, replay, err).
func (s *Supervisor) receiveSteer(ctx context.Context, in SteerInput) (int64, bool, error) {
	var revision int64
	var replay bool
	err := s.db.ResearchWrite(ctx, func(db store.ResearchDB) error {
		if existing, err := store.GetRunEventTx(ctx, db, steerReceivedID(in.RunID, in.IdempotencyKey)); err == nil {
			var msg steerMessage
			if err := json.Unmarshal(existing.Payload, &msg); err != nil {
				return err
			}
			revision, replay = msg.Revision, true
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var maxRev int64
		cursor := ""
		for {
			events, next, err := store.ListRunEvents(ctx, db, in.RunID, cursor, 100)
			if err != nil {
				return err
			}
			for _, e := range events {
				if e.Kind != SuperviseEventSteerReceived {
					continue
				}
				var msg steerMessage
				if err := json.Unmarshal(e.Payload, &msg); err != nil {
					continue
				}
				if msg.Revision > maxRev {
					maxRev = msg.Revision
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
		revision = maxRev + 1
		now := time.Now()
		return store.AppendRunEventIdempotent(ctx, db, researchcontract.Event{
			ID: steerReceivedID(in.RunID, in.IdempotencyKey), RunID: in.RunID,
			Kind: SuperviseEventSteerReceived, Outcome: researchcontract.OutcomeOK,
			Payload: mustPayload(steerMessage{
				Revision: revision, Body: in.Body, Correction: in.Correction,
				By: in.Actor.Kind + ":" + in.Actor.ID,
			}),
			ObservedAt: now, RecordedAt: now,
		})
	})
	if err != nil {
		return 0, false, mapStoreError(err)
	}
	return revision, replay, nil
}

func (s *Supervisor) steerApplied(ctx context.Context, runID, key string) bool {
	e, err := journalGet(ctx, s.journal, steerAppliedID(runID, key))
	if err != nil {
		return false
	}
	var ack struct {
		Applied string `json:"applied"`
	}
	if err := json.Unmarshal(e.Payload, &ack); err != nil {
		return false
	}
	return ack.Applied == "live_turn"
}

// steerMessages returns the most recent durable steering messages, oldest
// first, for continuation briefs.
func (s *Supervisor) steerMessages(ctx context.Context, runID string, last int) ([]steerMessage, error) {
	var all []steerMessage
	cursor := ""
	for {
		events, next, err := s.journal.List(ctx, runID, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.Kind != SuperviseEventSteerReceived {
				continue
			}
			var msg steerMessage
			if err := json.Unmarshal(e.Payload, &msg); err != nil {
				continue
			}
			all = append(all, msg)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(all) > last {
		all = all[len(all)-last:]
	}
	return all, nil
}

func (s *Supervisor) journalSteered(ctx context.Context, in SteerInput, revision int64, applied, reason, fallback string) error {
	now := time.Now()
	outcome := researchcontract.OutcomeOK
	if applied != "live_turn" {
		outcome = researchcontract.OutcomeUncertain
	}
	return s.journalAppend(ctx, researchcontract.Event{
		ID: steerAppliedID(in.RunID, in.IdempotencyKey), RunID: in.RunID,
		Kind: superviseEventSteered, Outcome: outcome,
		Payload: mustPayload(struct {
			Revision int64  `json:"revision"`
			Applied  string `json:"applied"`
			Reason   string `json:"reason,omitempty"`
			Fallback string `json:"fallback,omitempty"`
		}{revision, applied, reason, fallback}),
		ObservedAt: now, RecordedAt: now,
	})
}

// applySteer routes a recorded message to the live turn or the queue.
func (s *Supervisor) applySteer(ctx context.Context, in SteerInput, revision int64) (SteerOutput, error) {
	var live store.RoundAttempt
	var found bool
	_ = s.db.Read(ctx, func(r store.Reader) error {
		a, err := store.DispatchedTurnAttemptTx(ctx, r, in.RunID, in.Generation)
		if err == nil {
			live, found = a, true
		}
		return nil
	})
	if !found {
		if err := s.journalSteered(ctx, in, revision, "next_turn", "no_live_turn", ""); err != nil {
			return SteerOutput{Revision: revision, State: SteerQueued}, errors.Join(err,
				errors.New("steer recorded but the acknowledgment was not journaled"))
		}
		return SteerOutput{Revision: revision, State: SteerQueued}, nil
	}
	converse := s.conversation()
	if converse == nil {
		if err := s.journalSteered(ctx, in, revision, "queued", "no_live_turn", "conversation_unwired"); err != nil {
			return SteerOutput{Revision: revision, State: SteerQueued}, errors.Join(err,
				errors.New("steer recorded but the acknowledgment was not journaled"))
		}
		return SteerOutput{Revision: revision, State: SteerQueued}, nil
	}
	if _, err := converse.SteerAttempt(ctx, in.RunID, live.ID, in.Body); err != nil {
		applied, reason := "queued", "steer_fallback"
		switch {
		case errors.Is(err, store.ErrFenced):
			applied, reason = "next_turn", "turn_terminal"
		case errors.Is(err, store.ErrUncertain):
			reason = "steer_unverified"
		case errors.Is(err, store.ErrInvalid):
			reason = "steer_refused"
		}
		if jerr := s.journalSteered(ctx, in, revision, applied, reason, "interrupt_reconcile"); jerr != nil {
			return SteerOutput{Revision: revision, State: SteerQueued}, errors.Join(jerr,
				errors.New("steer recorded but the acknowledgment was not journaled"))
		}
		return SteerOutput{Revision: revision, State: SteerQueued}, nil
	}
	if err := s.journalSteered(ctx, in, revision, "live_turn", "", ""); err != nil {
		return SteerOutput{Revision: revision, State: SteerQueued}, errors.Join(err,
			errors.New("steer applied to the live turn but the acknowledgment was not journaled"))
	}
	return SteerOutput{Revision: revision, State: SteerApplied, Applied: true, Live: true}, nil
}

// applyCorrection rotates authority for changed facts or permissions: fence,
// cancel, reconcile, then resume on the remaining allowance. Saved audited
// records survive; pending writes are fenced and reaped. When unknown work
// cannot settle, the run stays paused with the message durable and the owner
// resumes later.
func (s *Supervisor) applyCorrection(ctx context.Context, in SteerInput, revision int64) (SteerOutput, error) {
	round, err := s.db.Round(ctx, in.RunID)
	if err != nil {
		return SteerOutput{}, mapStoreError(err)
	}
	if round.State == store.RoundRunning {
		if _, err := s.Stop(ctx, in.Actor, in.RunID, "owner_correction"); err != nil {
			return SteerOutput{}, err
		}
	} else if round.State != store.RoundPaused {
		return SteerOutput{}, s.fencedError(ctx, in.RunID)
	}
	resumeOut, resumeErr := s.Resume(ctx, in.Actor, in.RunID)
	now := time.Now()
	_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
		ID: fmt.Sprintf("correction.%s.%d", in.RunID, revision), RunID: in.RunID,
		Kind: SuperviseEventCorrection, Outcome: researchcontract.OutcomeOK,
		Payload: mustPayload(struct {
			Revision   int64 `json:"revision"`
			Generation int64 `json:"generation"`
		}{revision, resumeOut.Generation}),
		ObservedAt: now, RecordedAt: now,
	})
	if resumeErr != nil {
		return SteerOutput{Revision: revision, State: SteerPaused}, resumeErr
	}
	return SteerOutput{Revision: revision, State: SteerApplied, Applied: true}, nil
}

// StopOutput reports a completed stop.
type StopOutput struct {
	Generation int64
	Released   int
	Uncertain  int
}

// Stop fences first (control-generation rotation in the store), then cancels
// owned execution, records cancel signals, reaps never-dispatched holds and
// pauses. Late completions past this boundary keep as evidence only.
func (s *Supervisor) Stop(ctx context.Context, actor store.Actor, runID, reason string) (StopOutput, error) {
	if err := requireOwner(actor); err != nil {
		return StopOutput{}, err
	}
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return StopOutput{}, mapStoreError(err)
	}
	switch round.State {
	case store.RoundRunning, store.RoundPaused:
	case store.RoundStopping, store.RoundAwaitingInput:
		return StopOutput{}, researchcontract.NewError(researchcontract.OutcomeStopped, "run",
			fmt.Sprintf("run %s; wait for it to settle", round.State))
	case store.RoundQueued:
		return StopOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "run", "run not started")
	default:
		return StopOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "run",
			fmt.Sprintf("run already terminal (%s)", round.State))
	}
	stopped, err := s.control.Stop(ctx, actor, runID)
	if err != nil {
		return StopOutput{}, mapStoreError(err)
	}
	released, releaseErr := s.reapStaleReserved(ctx, actor, runID, stopped.Generation)
	var uncertain []store.RoundAttempt
	_ = s.db.Read(ctx, func(r store.Reader) error {
		uncertain, _ = store.UncertainAttemptsTx(ctx, r, runID)
		return nil
	})
	uncertainIDs := []string{}
	for _, a := range uncertain {
		uncertainIDs = append(uncertainIDs, a.ID)
	}
	now := time.Now()
	journalErr := s.journalAppend(ctx, researchcontract.Event{
		ID: fmt.Sprintf("stopped.%s.%d", runID, stopped.Generation), RunID: runID,
		Kind: SuperviseEventStopped, Outcome: researchcontract.OutcomeStopped,
		Payload: mustPayload(struct {
			Generation int64    `json:"generation"`
			Reason     string   `json:"reason,omitempty"`
			Released   int      `json:"released"`
			Uncertain  []string `json:"uncertain"`
		}{stopped.Generation, reason, released, uncertainIDs}),
		ObservedAt: now, RecordedAt: now,
	})
	if _, err := s.refreshCheckpoint(ctx, stopped, nil); err != nil {
		releaseErr = errors.Join(releaseErr, err)
	}
	now = time.Now()
	_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
		ID:    fmt.Sprintf("checkpoint.%s.%d.%d", runID, stopped.Generation, now.UnixNano()),
		RunID: runID, Kind: SuperviseEventCheckpointed, Outcome: researchcontract.OutcomeOK,
		Payload: mustPayload(struct {
			Gen int64 `json:"generation"`
		}{stopped.Generation}),
		ObservedAt: now, RecordedAt: now,
	})
	out := StopOutput{Generation: stopped.Generation, Released: released, Uncertain: len(uncertain)}
	if releaseErr != nil {
		return out, errors.Join(releaseErr, errors.New("run stopped but post-stop cleanup lagged"))
	}
	if journalErr != nil {
		return out, errors.Join(journalErr, errors.New("run stopped but the stop was not journaled"))
	}
	return out, nil
}

// reapStaleReserved releases never-dispatched holds revoked by the fence,
// refunding allowance for work that can never run.
func (s *Supervisor) reapStaleReserved(ctx context.Context, actor store.Actor, runID string, gen int64) (int, error) {
	var stale []store.RoundAttempt
	if err := s.db.Read(ctx, func(r store.Reader) error {
		var err error
		stale, err = store.StaleReservedAttemptsTx(ctx, r, runID, gen)
		return err
	}); err != nil {
		return 0, err
	}
	var errs error
	released := 0
	for _, a := range stale {
		if err := s.db.ReleaseRoundAttempt(ctx, actor, a.ID); err != nil {
			errs = errors.Join(errs, fmt.Errorf("release %s: %w", a.ID, err))
			continue
		}
		released++
	}
	return released, errs
}

// ResumeOutput reports a resumed run on its remaining allowance.
type ResumeOutput struct {
	Generation int64
	Remaining  researchcontract.Allowance
	Ledger     researchcontract.UsageLedger
	Reconciled int
	Unknown    []string
}

// Resume reconciles in-flight work, rotates credentials and generation,
// restores the checkpoint and continues on the REMAINING allowance. Limits
// are never touched: a silent fresh budget is impossible by construction.
func (s *Supervisor) Resume(ctx context.Context, actor store.Actor, runID string) (ResumeOutput, error) {
	if err := requireOwner(actor); err != nil {
		return ResumeOutput{}, err
	}
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return ResumeOutput{}, mapStoreError(err)
	}
	switch round.State {
	case store.RoundPaused:
	case store.RoundRunning:
		return ResumeOutput{}, researchcontract.NewError(researchcontract.OutcomeConflict, "run",
			"run already running")
	case store.RoundStopping:
		return ResumeOutput{}, researchcontract.NewError(researchcontract.OutcomeStopped, "run",
			"stop in progress; wait for it to settle")
	case store.RoundQueued:
		return ResumeOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "run", "run not started")
	default:
		return ResumeOutput{}, researchcontract.NewError(researchcontract.OutcomeInvalid, "run",
			fmt.Sprintf("run terminal (%s)", round.State))
	}
	var before []store.RoundAttempt
	_ = s.db.Read(ctx, func(r store.Reader) error {
		before, _ = store.UncertainAttemptsTx(ctx, r, runID)
		return nil
	})
	resumed, err := s.control.Resume(ctx, actor, runID)
	if err != nil {
		return ResumeOutput{}, s.mapResumeError(ctx, runID, before, err)
	}
	if rotator, ok := s.auth.(CredentialRotator); ok {
		rotator.RotateCredentials(runID)
	}
	refreshed, ckptErr := s.refreshCheckpoint(ctx, resumed, nil)
	var after []store.RoundAttempt
	_ = s.db.Read(ctx, func(r store.Reader) error {
		after, _ = store.UncertainAttemptsTx(ctx, r, runID)
		return nil
	})
	ledger, err := s.auth.Usage(ctx, runID)
	if err != nil {
		return ResumeOutput{}, err
	}
	remaining := researchcontract.Allowance{
		Requests: ledger.Enforced.Requests - ledger.Reserved.Requests - ledger.Observed.Requests,
		Items:    ledger.Enforced.Items - ledger.Reserved.Items - ledger.Observed.Items,
		Tools:    ledger.Enforced.Tools - ledger.Reserved.Tools - ledger.Observed.Tools,
		Turns:    ledger.Enforced.Turns - ledger.Reserved.Turns - ledger.Observed.Turns,
	}
	for _, v := range []*int64{&remaining.Requests, &remaining.Items, &remaining.Tools, &remaining.Turns} {
		if *v < 0 {
			*v = 0
		}
	}
	unknown := []string{}
	for _, a := range after {
		unknown = append(unknown, a.ID)
	}
	reconciled := len(before) - len(after)
	if reconciled < 0 {
		reconciled = 0
	}
	now := time.Now()
	journalErr := s.journalAppend(ctx, researchcontract.Event{
		ID: fmt.Sprintf("resumed.%s.%d", runID, resumed.Generation), RunID: runID,
		Kind: superviseEventResumed, Outcome: researchcontract.OutcomeOK,
		Payload: mustPayload(struct {
			Generation int64                      `json:"generation"`
			Recon      int                        `json:"reconciled"`
			Unknown    []string                   `json:"unknown"`
			Remaining  researchcontract.Allowance `json:"remaining"`
		}{resumed.Generation, reconciled, unknown, remaining}),
		ObservedAt: now, RecordedAt: now,
	})
	_ = refreshed
	out := ResumeOutput{Generation: resumed.Generation, Remaining: remaining,
		Ledger: ledger, Reconciled: reconciled, Unknown: unknown}
	if ckptErr != nil {
		return out, errors.Join(ckptErr, errors.New("run resumed but the checkpoint lagged"))
	}
	if journalErr != nil {
		return out, errors.Join(journalErr, errors.New("run resumed but the resume was not journaled"))
	}
	return out, nil
}

// mapResumeError translates reconcile/resume failures and journals why the
// run stays paused.
func (s *Supervisor) mapResumeError(ctx context.Context, runID string, before []store.RoundAttempt, err error) error {
	switch {
	case errors.Is(err, store.ErrExpired):
		return researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityDeadline), "run deadline passed while paused")
	case errors.Is(err, store.ErrUncertain):
		var after []store.RoundAttempt
		_ = s.db.Read(ctx, func(r store.Reader) error {
			after, _ = store.UncertainAttemptsTx(ctx, r, runID)
			return nil
		})
		now := time.Now()
		for _, a := range after {
			_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
				ID: fmt.Sprintf("uncertain.%s.resume", a.ID), RunID: runID, AttemptID: a.ID,
				Kind: superviseEventUncertain, Outcome: researchcontract.OutcomeUncertain,
				Payload: mustPayload(struct {
					Reason string `json:"reason"`
				}{"reconcile_unsettled"}),
				ObservedAt: now, RecordedAt: now,
			})
		}
		return researchcontract.NewError(researchcontract.OutcomeUncertain, "attempt",
			fmt.Sprintf("%d of %d uncertain attempts still unsettled; the run stays paused", len(after), len(before)))
	case errors.Is(err, store.ErrAllowance):
		now := time.Now()
		_ = s.journalAppend(context.WithoutCancel(ctx), researchcontract.Event{
			ID: fmt.Sprintf("exhausted.%s.resume", runID), RunID: runID,
			Kind: researchcontract.EventExhausted, Outcome: researchcontract.OutcomeBudgetExhausted,
			Payload: mustPayload(struct {
				Reason string `json:"reason"`
			}{"reconcile_charge_failed"}),
			ObservedAt: now, RecordedAt: now,
		})
		return researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
			string(researchcontract.AuthorityAllowance),
			"remaining allowance cannot cover reconciliation; the run stays paused")
	case errors.Is(err, ErrNotReady):
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "run",
			"execution backend unavailable; the run stays paused")
	}
	return mapStoreError(err)
}

// Usage publishes the enforced/reserved/observed/unknown ledger. Unknown is
// a live reading that latches on any uncertain work and is never silently
// zero.
func (s *Supervisor) Usage(ctx context.Context, runID string) (researchcontract.UsageLedger, error) {
	return s.auth.Usage(ctx, runID)
}

// RunBounds publishes the effective execution bounds: per-run concurrency,
// per-operation and per-turn deadlines, the per-request/per-operation
// byte and request ceilings and the run deadline.
type RunBounds struct {
	MaxConcurrent int
	PerOpTimeout  time.Duration
	TurnTimeout   time.Duration
	MaxBodyBytes  int64
	MaxRequests   int
	Deadline      time.Time
}

// RunBoundsFor returns the effective bounds for one run.
func (s *Supervisor) RunBoundsFor(ctx context.Context, runID string) (RunBounds, error) {
	round, err := s.db.Round(ctx, runID)
	if err != nil {
		return RunBounds{}, mapStoreError(err)
	}
	return RunBounds{
		MaxConcurrent: s.resolveMaxConc(ctx, runID),
		PerOpTimeout:  s.perOp,
		TurnTimeout:   s.turnBudget,
		MaxBodyBytes:  MaxDispatchBodyBytes,
		MaxRequests:   MaxDispatchRequests,
		Deadline:      round.Deadline,
	}, nil
}
