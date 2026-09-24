// Package codexservice run/turn/steering events (T08 lane B).
//
// This file implements the T06 §5 run authority event/observation rules on the
// App Server path: item/turn correlation through the agreed
// researchcontract.EventSink, stored-conversation continuation
// (resume-before-continue), bounded observe-then-interrupt, and verified
// steering with a stop/reconcile fallback. Timing rules follow T02 §6:
// never interrupt blind after start, resume before continuing work on a fresh
// connection, and tolerate transient turns/list -32601 briefly.
//
// Durable binding: terminal turn observations persist through the existing
// run/attempt tables (ObserveRoundTurn). There is no append-only event journal
// table in 001_initial.sql and the schema is T07-owned, so RunEventSink is an
// in-process ordered journal for now; the durable table binding is a T13
// follow-up recorded in the T08-events note.
package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// B-owned run/turn/steering event kinds (T06 §5 permits B to add these at
// T08/T13). Payloads carry redacted IDs and states only, never raw protocol
// bytes, prompts, or credentials.
const (
	RunEventTurnObserved  = "run.turn_observed"
	RunEventItemStarted   = "run.item_started"
	RunEventItemCompleted = "run.item_completed"
	RunEventItemUnmatched = "run.item_unmatched"
	RunEventSteered       = "run.steered"
	RunEventSteerRejected = "run.steer_rejected"
	RunEventInterrupted   = "run.interrupted"
	RunEventResumed       = "run.resumed"
	RunEventUncertain     = "run.uncertain"
	RunEventUnknownEvents = "run.unknown_events"
)

const (
	// Mirror the durable journal bound (T13) so the in-process fake accepts
	// what the supervisor journals, including owner steering bodies.
	maxRunEventPayload  = store.MaxRunEventPayload
	maxRunEvents        = 100000
	defaultRunEventPage = 50
	maxRunEventPage     = 100
	// maxSteerText matches the ExecuteRoundTurn brief bound: steering text is
	// owner text, not a new commission.
	maxSteerText = 12000
)

var runEventSeq atomic.Uint64

var (
	errInterruptBudget      = errors.New("interrupt budget exhausted")
	errInterruptUnconfirmed = errors.New("interrupt accepted but turn still in flight")
	errSteerMismatch        = errors.New("steer confirmed a different turn")
	errSteerUnconfirmed     = errors.New("steer accepted but turn settled before verification")
)

// runTurnRecord is the redacted turn journal/persistence payload: protocol IDs
// plus the verified status and how it was established.
type runTurnRecord struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	Status    string `json:"status,omitempty"`
	Via       string `json:"via,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Applied   string `json:"applied,omitempty"`
	Persisted bool   `json:"persisted,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
}

// runItemRecord is the redacted item journal payload. Raw protocol bytes are
// never persisted here.
type runItemRecord struct {
	ThreadID       string `json:"threadId"`
	TurnID         string `json:"turnId"`
	ItemID         string `json:"itemId"`
	ItemType       string `json:"itemType,omitempty"`
	State          string `json:"state,omitempty"`
	Reason         string `json:"reason,omitempty"`
	StartedEventID string `json:"startedEventId,omitempty"`
}

func mustEventPayload(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func turnOutcome(status string) researchcontract.Outcome {
	switch status {
	case "completed":
		return researchcontract.OutcomeOK
	case "interrupted":
		return researchcontract.OutcomeStopped
	case "failed":
		// T06 §1 receipt mapping: failed → invalid (with errorCode).
		return researchcontract.OutcomeInvalid
	default:
		return researchcontract.OutcomeUncertain
	}
}

// RunEventSink is the lane-B researchcontract.EventSink for run/turn/steering
// events: an in-process ordered journal with idempotent append (first write
// wins per event ID) and cursor paging per run. It never touches the research
// store; durable table binding is a T13 follow-up.
type RunEventSink struct {
	mu     sync.Mutex
	events []researchcontract.Event
	byID   map[string]int
}

// NewRunEventSink returns an empty journal.
func NewRunEventSink() *RunEventSink {
	return &RunEventSink{byID: make(map[string]int)}
}

var _ researchcontract.EventSink = (*RunEventSink)(nil)

// Append journals one event. A repeated ID is an idempotent no-op.
func (s *RunEventSink) Append(ctx context.Context, e researchcontract.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return store.ErrInvalid
	}
	if e.ID == "" || e.RunID == "" || e.Kind == "" || e.ObservedAt.IsZero() ||
		len(e.Payload) > maxRunEventPayload || (e.Outcome != "" && !e.Outcome.Valid()) {
		return store.ErrInvalid
	}
	if e.RecordedAt.IsZero() {
		e.RecordedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, duplicate := s.byID[e.ID]; duplicate {
		return nil
	}
	if len(s.events) >= maxRunEvents {
		return store.ErrInvalid
	}
	s.byID[e.ID] = len(s.events)
	s.events = append(s.events, e)
	return nil
}

// List pages one run's events in journal order. The cursor is the last seen
// event ID; empty starts at the beginning and an unknown cursor is rejected.
func (s *RunEventSink) List(ctx context.Context, runID string, cursor string, limit int) ([]researchcontract.Event, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s == nil || runID == "" {
		return nil, "", store.ErrInvalid
	}
	if limit <= 0 {
		limit = defaultRunEventPage
	}
	if limit > maxRunEventPage {
		limit = maxRunEventPage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if cursor != "" {
		index, ok := s.byID[cursor]
		if !ok {
			return nil, "", store.ErrInvalid
		}
		start = index + 1
	}
	var out []researchcontract.Event
	next := ""
	for i := start; i < len(s.events); i++ {
		if s.events[i].RunID != runID {
			continue
		}
		if len(out) == limit {
			more := false
			for j := i; j < len(s.events); j++ {
				if s.events[j].RunID == runID {
					more = true
					break
				}
			}
			if more {
				next = out[len(out)-1].ID
			}
			break
		}
		out = append(out, s.events[i])
	}
	return out, next, nil
}

// RunEventSink returns the service's run/turn/steering journal, creating it on
// first use. Callers must treat it as process-local until T13 binds it.
func (s *Service) RunEventSink() *RunEventSink {
	s.runEventsMu.Lock()
	defer s.runEventsMu.Unlock()
	if s.runEvents == nil {
		s.runEvents = NewRunEventSink()
	}
	return s.runEvents
}

// ItemCorrelator pairs item/started with item/completed per
// (thread, turn, item) and journals the correlation through the agreed sink.
// Completions without a start, and starts still open when the turn settles,
// are journaled explicitly as unmatched — never silently paired or dropped.
type ItemCorrelator struct {
	sink *RunEventSink
	mu   sync.Mutex
	open map[itemKey]openItem
}

type itemKey struct{ threadID, turnID, itemID string }

type openItem struct {
	runID, attemptID, itemType, startedEventID string
}

// NewItemCorrelator binds a correlator to a sink. A nil sink makes every
// method a no-op so live-turn wiring can never block or fail dispatch.
func NewItemCorrelator(sink *RunEventSink) *ItemCorrelator {
	return &ItemCorrelator{sink: sink, open: make(map[itemKey]openItem)}
}

func itemEventID(threadID, turnID, itemID, suffix string) string {
	return "item." + threadID + "." + turnID + "." + itemID + "." + suffix
}

// ObserveItem journals one item lifecycle step. Unattributable or malformed
// input is dropped: the sink never holds events it cannot scope to a run.
func (c *ItemCorrelator) ObserveItem(runID, attemptID string, ev codex.ItemEvent, at time.Time) {
	if c == nil || c.sink == nil || runID == "" || attemptID == "" ||
		ev.ThreadID == "" || ev.TurnID == "" || ev.ItemID == "" ||
		(ev.State != "started" && ev.State != "completed") {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	key := itemKey{ev.ThreadID, ev.TurnID, ev.ItemID}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch ev.State {
	case "started":
		id := itemEventID(ev.ThreadID, ev.TurnID, ev.ItemID, "started")
		_ = c.sink.Append(context.Background(), researchcontract.Event{
			ID: id, RunID: runID, AttemptID: attemptID, Kind: RunEventItemStarted,
			Outcome: researchcontract.OutcomeOK,
			Payload: mustEventPayload(runItemRecord{ThreadID: ev.ThreadID, TurnID: ev.TurnID,
				ItemID: ev.ItemID, ItemType: ev.ItemType, State: ev.State}),
			ObservedAt: at, RecordedAt: time.Now(),
		})
		if _, seen := c.open[key]; !seen {
			c.open[key] = openItem{runID: runID, attemptID: attemptID, itemType: ev.ItemType, startedEventID: id}
		}
	case "completed":
		open, ok := c.open[key]
		if !ok {
			_ = c.sink.Append(context.Background(), researchcontract.Event{
				ID:    itemEventID(ev.ThreadID, ev.TurnID, ev.ItemID, "unmatched-missing-start"),
				RunID: runID, AttemptID: attemptID, Kind: RunEventItemUnmatched,
				Outcome: researchcontract.OutcomeUncertain,
				Payload: mustEventPayload(runItemRecord{ThreadID: ev.ThreadID, TurnID: ev.TurnID,
					ItemID: ev.ItemID, ItemType: ev.ItemType, State: ev.State, Reason: "missing_start"}),
				ObservedAt: at, RecordedAt: time.Now(),
			})
			return
		}
		delete(c.open, key)
		_ = c.sink.Append(context.Background(), researchcontract.Event{
			ID:    itemEventID(ev.ThreadID, ev.TurnID, ev.ItemID, "completed"),
			RunID: open.runID, AttemptID: open.attemptID, Kind: RunEventItemCompleted,
			Outcome: researchcontract.OutcomeOK,
			Payload: mustEventPayload(runItemRecord{ThreadID: ev.ThreadID, TurnID: ev.TurnID,
				ItemID: ev.ItemID, ItemType: open.itemType, State: ev.State, StartedEventID: open.startedEventID}),
			ObservedAt: at, RecordedAt: time.Now(),
		})
	}
}

// CloseTurn journals the settled turn outcome and resolves every still-open
// item of that turn as unmatched. Items of other turns are untouched, and a
// repeated close is idempotent through sink dedup.
func (c *ItemCorrelator) CloseTurn(runID, attemptID, threadID, turnID, status string, at time.Time) {
	if c == nil || c.sink == nil || runID == "" || attemptID == "" || threadID == "" || turnID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	kind := RunEventTurnObserved
	if status != "completed" && status != "failed" && status != "interrupted" {
		kind, status = RunEventUncertain, "unknown"
	}
	_ = c.sink.Append(context.Background(), researchcontract.Event{
		ID:    "turn." + threadID + "." + turnID + "." + status,
		RunID: runID, AttemptID: attemptID, Kind: kind,
		Outcome:    turnOutcome(status),
		Payload:    mustEventPayload(runTurnRecord{ThreadID: threadID, TurnID: turnID, Status: status, Via: "turn"}),
		ObservedAt: at, RecordedAt: time.Now(),
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, open := range c.open {
		if key.threadID != threadID || key.turnID != turnID {
			continue
		}
		delete(c.open, key)
		_ = c.sink.Append(context.Background(), researchcontract.Event{
			ID:    itemEventID(key.threadID, key.turnID, key.itemID, "unmatched-missing-completion"),
			RunID: open.runID, AttemptID: open.attemptID, Kind: RunEventItemUnmatched,
			Outcome: researchcontract.OutcomeUncertain,
			Payload: mustEventPayload(runItemRecord{ThreadID: key.threadID, TurnID: key.turnID,
				ItemID: key.itemID, ItemType: open.itemType, State: "started", Reason: "missing_completion",
				StartedEventID: open.startedEventID}),
			ObservedAt: at, RecordedAt: time.Now(),
		})
	}
}

// conversationBounds caps every retry loop in the T08 conversation policy.
// All loops are count-based so tests set zero pauses and stay deterministic.
type conversationBounds struct {
	observeTries   int
	observePause   time.Duration
	interruptTries int
	interruptPause time.Duration
	confirmTries   int
	confirmPause   time.Duration
}

func defaultConversationBounds() conversationBounds {
	return conversationBounds{
		observeTries: 12, observePause: 100 * time.Millisecond,
		interruptTries: 6, interruptPause: 200 * time.Millisecond,
		confirmTries: 15, confirmPause: 200 * time.Millisecond,
	}
}

func (b conversationBounds) normalized() conversationBounds {
	def := defaultConversationBounds()
	if b.observeTries < 1 {
		b.observeTries = def.observeTries
	}
	if b.observePause < 0 {
		b.observePause = def.observePause
	}
	if b.interruptTries < 1 {
		b.interruptTries = def.interruptTries
	}
	if b.interruptPause < 0 {
		b.interruptPause = def.interruptPause
	}
	if b.confirmTries < 1 {
		b.confirmTries = def.confirmTries
	}
	if b.confirmPause < 0 {
		b.confirmPause = def.confirmPause
	}
	return b
}

// runtimeCause classifies a client failure for honest recovery. Only
// causeTransientList (read-only turns/list polling) is ever retried, and only
// within the conversation bounds.
type runtimeCause int

const (
	causeOther runtimeCause = iota
	causeTransientList
	causeThreadUnavailable
	causeBackpressure
	causeDisconnected
	causeCanceled
)

func classifyRuntimeError(err error) runtimeCause {
	if err == nil {
		return causeOther
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return causeCanceled
	}
	if errors.Is(err, codex.ErrBackpressure) {
		return causeBackpressure
	}
	if errors.Is(err, codex.ErrClosed) || errors.Is(err, codex.ErrUnavailable) || errors.Is(err, codex.ErrNotInitialized) {
		return causeDisconnected
	}
	var rpc *codex.RPCError
	if errors.As(err, &rpc) {
		switch rpc.Code {
		case -32601:
			return causeTransientList
		case -32600:
			return causeThreadUnavailable
		}
	}
	return causeOther
}

// uncertainReason names the honest journal reason for an unreconciled outcome.
func uncertainReason(err error) string {
	switch classifyRuntimeError(err) {
	case causeBackpressure:
		return "backpressure"
	case causeDisconnected:
		return "disconnected"
	case causeCanceled:
		return "canceled"
	case causeTransientList:
		return "transient_history"
	case causeThreadUnavailable:
		return "thread_unavailable"
	default:
		if errors.Is(err, codex.ErrHistoryIncomplete) {
			return "history_incomplete"
		}
		return "unconfirmed"
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// waitTurnStatus polls the authoritative turn list until the known dispatch is
// observed. Only transient -32601 is retried, briefly; every other failure
// returns immediately so the caller can settle honestly.
func waitTurnStatus(ctx context.Context, client *codex.Client, threadID, turnID string, tries int, pause time.Duration) (string, error) {
	if tries < 1 {
		tries = 1
	}
	last := codex.ErrHistoryIncomplete
	for i := 0; i < tries; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		status, err := client.ObserveTurn(ctx, threadID, turnID)
		if err == nil {
			return status, nil
		}
		if classifyRuntimeError(err) != causeTransientList {
			return "", err
		}
		last = err
		sleepCtx(ctx, pause)
	}
	return "", last
}

// turnControl carries one persisted dispatch plus its conversation bounds. It
// owns no authority: every outcome is verified against the turn list and
// recorded through the existing tables and the agreed sink.
type turnControl struct {
	client             *codex.Client
	sink               *RunEventSink
	roundID, attemptID string
	generation         int64
	threadID, turnID   string
	bounds             conversationBounds
	persist            func(ctx context.Context, status string, evidence json.RawMessage) error
}

func (s *Service) newTurnControl(client *codex.Client, attempt store.RoundAttempt, remote store.RoundRemoteDispatch) *turnControl {
	return &turnControl{
		client: client, sink: s.RunEventSink(),
		roundID: attempt.RoundID, attemptID: attempt.ID, generation: attempt.Generation,
		threadID: remote.ThreadID, turnID: remote.TurnID,
		bounds: s.converse.normalized(),
		persist: func(ctx context.Context, status string, evidence json.RawMessage) error {
			return s.db.ObserveRoundTurn(ctx, attempt.RoundID, attempt.ID, attempt.Generation,
				remote.ThreadID, remote.TurnID, status, evidence)
		},
	}
}

func (t *turnControl) journal(kind string, outcome researchcontract.Outcome, payload runTurnRecord) {
	if t == nil || t.sink == nil {
		return
	}
	now := time.Now()
	_ = t.sink.Append(context.Background(), researchcontract.Event{
		ID:    fmt.Sprintf("%s.%s.%d", kind, t.attemptID, runEventSeq.Add(1)),
		RunID: t.roundID, AttemptID: t.attemptID, Kind: kind, Outcome: outcome,
		Payload: mustEventPayload(payload), ObservedAt: now, RecordedAt: now,
	})
}

// persistStatus records one verified observation through the existing
// run/attempt tables. Persistence runs outside the caller's cancellation so a
// canceled operation still leaves its evidence behind.
func (t *turnControl) persistStatus(ctx context.Context, recorded string, evidence json.RawMessage) bool {
	if t == nil || t.persist == nil {
		return false
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return t.persist(c, recorded, evidence) == nil
}

// recordTerminal persists and journals one verified terminal status.
func (t *turnControl) recordTerminal(ctx context.Context, status, via string) {
	evidence := mustEventPayload(runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status, Via: via})
	persisted := t.persistStatus(ctx, status, evidence)
	t.journal(RunEventTurnObserved, turnOutcome(status),
		runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status, Via: via, Persisted: persisted})
}

// uncertain persists an honest unknown observation, journals it, and wraps
// the cause as store.ErrUncertain. It never replays the turn.
func (t *turnControl) uncertain(ctx context.Context, cause error, via string) error {
	reason := uncertainReason(cause)
	evidence := mustEventPayload(runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID,
		Status: "unknown", Via: via, Reason: reason})
	persisted := t.persistStatus(ctx, "unknown", evidence)
	t.journal(RunEventUncertain, researchcontract.OutcomeUncertain,
		runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: "unknown",
			Via: via, Reason: reason, Persisted: persisted})
	return errors.Join(store.ErrUncertain, cause)
}

// noteRunUncertain journals an uncertain outcome that has no observation to
// persist (missing IDs, failed pre-write read). It writes nothing else.
func (s *Service) noteRunUncertain(runID, attemptID, reason, via string) {
	now := time.Now()
	_ = s.RunEventSink().Append(context.Background(), researchcontract.Event{
		ID:    fmt.Sprintf("%s.%s.%d", RunEventUncertain, attemptID, runEventSeq.Add(1)),
		RunID: runID, AttemptID: attemptID, Kind: RunEventUncertain,
		Outcome:    researchcontract.OutcomeUncertain,
		Payload:    mustEventPayload(runTurnRecord{Status: "unknown", Via: via, Reason: reason}),
		ObservedAt: now, RecordedAt: now,
	})
}

// noteUnknownEvents journals the protocol events the client explicitly
// dropped as unknown since the snapshot. Counts only, never raw payloads.
func (s *Service) noteUnknownEvents(client *codex.Client, before uint64, runID, attemptID string) {
	if client == nil {
		return
	}
	after := client.Diagnostics().UnknownNotifications
	if after <= before {
		return
	}
	now := time.Now()
	_ = s.RunEventSink().Append(context.Background(), researchcontract.Event{
		ID:    fmt.Sprintf("%s.%s.%d", RunEventUnknownEvents, attemptID, runEventSeq.Add(1)),
		RunID: runID, AttemptID: attemptID, Kind: RunEventUnknownEvents,
		Outcome: researchcontract.OutcomeUncertain,
		Payload: mustEventPayload(struct {
			Count uint64 `json:"count"`
		}{after - before}),
		ObservedAt: now, RecordedAt: now,
	})
}

// conversationTarget loads the persisted dispatch a conversation operation
// acts on. Missing or generation-mismatched remote IDs stay uncertain without
// any RPC; a wrong round or non-turn operation is a caller bug.
func (s *Service) conversationTarget(ctx context.Context, roundID, attemptID string) (store.RoundAttempt, store.RoundRemoteDispatch, error) {
	if roundID == "" || attemptID == "" {
		return store.RoundAttempt{}, store.RoundRemoteDispatch{}, store.ErrInvalid
	}
	attempt, err := s.db.RoundAttempt(ctx, attemptID)
	if err != nil {
		return store.RoundAttempt{}, store.RoundRemoteDispatch{}, err
	}
	if attempt.RoundID != roundID || attempt.Operation != store.RoundCodexTurn {
		return store.RoundAttempt{}, store.RoundRemoteDispatch{}, store.ErrInvalid
	}
	remote, err := s.db.RoundRemoteDispatch(ctx, roundID, attemptID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.noteRunUncertain(roundID, attemptID, "missing_remote_ids", "target")
			return store.RoundAttempt{}, store.RoundRemoteDispatch{}, store.ErrUncertain
		}
		return store.RoundAttempt{}, store.RoundRemoteDispatch{}, err
	}
	if remote.Generation != attempt.Generation || remote.ThreadID == "" || remote.TurnID == "" {
		s.noteRunUncertain(roundID, attemptID, "missing_remote_ids", "target")
		return store.RoundAttempt{}, store.RoundRemoteDispatch{}, store.ErrUncertain
	}
	return attempt, remote, nil
}

// conversationClient takes a ready quiescent connection for conversation
// control. Like ObserveDispatch it refuses while a dispatch owns the service;
// steering or interrupting the live run itself stays with Stop/CancelDispatch.
// It also returns the pre-readiness unknown-event baseline so the whole
// operation's dropped protocol events are accounted.
func (s *Service) conversationClient(ctx context.Context) (*codex.Client, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy || s.disconnecting || s.closed {
		return nil, 0, ErrBusy
	}
	var unknown uint64
	if s.client != nil && s.client.Err() == nil {
		unknown = s.client.Diagnostics().UnknownNotifications
	}
	status := s.statusLocked(ctx)
	if status.State != "ready" {
		return nil, 0, ErrUnavailable
	}
	return s.client, unknown, nil
}

// ResumeStoredThread loads a previously persisted conversation on the current
// connection so a fresh connection can continue it (resume-before-continue).
// Observation alone never enables writes. runID attributes the journal entry
// (the round or ingestion the conversation belongs to).
func (s *Service) ResumeStoredThread(ctx context.Context, runID, threadID string) error {
	if ctx == nil || runID == "" || threadID == "" {
		return store.ErrInvalid
	}
	client, unknown, err := s.conversationClient(ctx)
	if err != nil {
		return err
	}
	defer s.noteUnknownEvents(client, unknown, runID, "")
	thread, err := client.ResumeThread(ctx, threadID, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	if thread.ID != threadID {
		return ErrUnavailable
	}
	now := time.Now()
	_ = s.RunEventSink().Append(context.Background(), researchcontract.Event{
		ID:    fmt.Sprintf("%s.%s.%d", RunEventResumed, threadID, runEventSeq.Add(1)),
		RunID: runID, AttemptID: "", Kind: RunEventResumed, Outcome: researchcontract.OutcomeOK,
		Payload: mustEventPayload(struct {
			ThreadID string `json:"threadId"`
		}{threadID}),
		ObservedAt: now, RecordedAt: now,
	})
	return nil
}

// InterruptAttempt stops a persisted in-flight turn: observe-then-interrupt
// with bounded retry, then a verified terminal observation. An already
// terminal turn is reconciled without any interrupt RPC. Anything unverified
// settles as honest uncertain through the existing tables; the turn is never
// replayed.
func (s *Service) InterruptAttempt(ctx context.Context, roundID, attemptID string) (string, error) {
	if ctx == nil {
		return "", store.ErrInvalid
	}
	attempt, remote, err := s.conversationTarget(ctx, roundID, attemptID)
	if err != nil {
		return "", err
	}
	client, unknown, err := s.conversationClient(ctx)
	if err != nil {
		return "", err
	}
	defer s.noteUnknownEvents(client, unknown, roundID, attemptID)
	ctl := s.newTurnControl(client, attempt, remote)
	status, err := waitTurnStatus(ctx, client, remote.ThreadID, remote.TurnID, ctl.bounds.observeTries, ctl.bounds.observePause)
	if err != nil {
		return "", ctl.uncertain(ctx, err, "interrupt-observe")
	}
	if status != "inProgress" {
		ctl.recordTerminal(ctx, status, "interrupt")
		return status, nil
	}
	return ctl.interrupt(ctx)
}

// interrupt runs the bounded interrupt/confirm loop for a turn that was just
// observed in flight. A -32600 resumes once (fresh connection) and otherwise
// retries within budget (turn-attach race); disconnect, backpressure and
// cancellation settle uncertain immediately with no further writes.
func (t *turnControl) interrupt(ctx context.Context) (string, error) {
	resumed := false
	for i := 0; i < t.bounds.interruptTries; i++ {
		if err := ctx.Err(); err != nil {
			return "", t.uncertain(ctx, err, "interrupt")
		}
		if i > 0 {
			status, err := waitTurnStatus(ctx, t.client, t.threadID, t.turnID, 2, t.bounds.observePause)
			if err == nil && status != "inProgress" {
				t.recordTerminal(ctx, status, "interrupt-raced")
				return status, nil
			}
			if err != nil && classifyRuntimeError(err) != causeTransientList {
				return "", t.uncertain(ctx, err, "interrupt-observe")
			}
		}
		if err := t.client.Interrupt(ctx, t.threadID, t.turnID); err == nil {
			return t.confirmInterrupted(ctx)
		} else {
			switch classifyRuntimeError(err) {
			case causeThreadUnavailable:
				if !resumed {
					resumed = true
					// Resume-before-write on a fresh connection. Failure is
					// non-fatal: the turn may simply not be active yet.
					_, _ = t.client.ResumeThread(ctx, t.threadID, true)
				}
				sleepCtx(ctx, t.bounds.interruptPause)
			case causeTransientList:
				sleepCtx(ctx, t.bounds.interruptPause)
			case causeBackpressure, causeDisconnected, causeCanceled:
				return "", t.uncertain(ctx, err, "interrupt")
			default:
				sleepCtx(ctx, t.bounds.interruptPause)
			}
		}
	}
	if status, err := waitTurnStatus(ctx, t.client, t.threadID, t.turnID, 2, t.bounds.observePause); err == nil && status != "inProgress" {
		t.recordTerminal(ctx, status, "interrupt-raced")
		return status, nil
	}
	return "", t.uncertain(ctx, errInterruptBudget, "interrupt")
}

// confirmInterrupted verifies the terminal state after an accepted interrupt.
// A turn still in flight at the end of the budget settles uncertain: the
// interrupt is not replayed beyond its budget.
func (t *turnControl) confirmInterrupted(ctx context.Context) (string, error) {
	status, err := waitTurnStatus(ctx, t.client, t.threadID, t.turnID, t.bounds.confirmTries, t.bounds.confirmPause)
	if err != nil {
		return "", t.uncertain(ctx, err, "interrupt-confirm")
	}
	if status != "interrupted" && status != "completed" && status != "failed" {
		return "", t.uncertain(ctx, errInterruptUnconfirmed, "interrupt-confirm")
	}
	if status != "interrupted" {
		t.recordTerminal(ctx, status, "interrupt-raced")
		return status, nil
	}
	evidence := mustEventPayload(runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status, Via: "interrupt"})
	persisted := t.persistStatus(ctx, status, evidence)
	t.journal(RunEventInterrupted, researchcontract.OutcomeStopped,
		runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status,
			Via: "interrupt", Persisted: persisted, Verified: true})
	return status, nil
}

// SteerAttempt continues a persisted in-flight turn with owner text. Steering
// is verified: the confirmed turn ID must match the persisted one and the
// turn must still be observable afterwards. Anything unverified — rejection,
// mismatch, transport loss — falls back to stopping the turn and reconciling
// its honest outcome. A terminal turn is reconciled without any steer RPC.
func (s *Service) SteerAttempt(ctx context.Context, roundID, attemptID, text string) (string, error) {
	if ctx == nil || strings.TrimSpace(text) == "" || len(text) > maxSteerText {
		return "", store.ErrInvalid
	}
	attempt, remote, err := s.conversationTarget(ctx, roundID, attemptID)
	if err != nil {
		return "", err
	}
	client, unknown, err := s.conversationClient(ctx)
	if err != nil {
		return "", err
	}
	defer s.noteUnknownEvents(client, unknown, roundID, attemptID)
	return s.newTurnControl(client, attempt, remote).steer(ctx, text)
}

func steerFailureReason(err error) string {
	switch classifyRuntimeError(err) {
	case causeBackpressure:
		return "backpressure"
	case causeDisconnected:
		return "disconnected"
	case causeCanceled:
		return "canceled"
	case causeThreadUnavailable:
		return "thread_unavailable"
	case causeTransientList:
		return "transient"
	default:
		return "rejected"
	}
}

func (t *turnControl) steer(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" || len(text) > maxSteerText {
		return "", store.ErrInvalid
	}
	status, err := waitTurnStatus(ctx, t.client, t.threadID, t.turnID, t.bounds.observeTries, t.bounds.observePause)
	if err != nil {
		return "", t.uncertain(ctx, err, "steer-observe")
	}
	if status != "inProgress" {
		t.recordTerminal(ctx, status, "steer")
		t.journal(RunEventSteerRejected, researchcontract.OutcomeStale,
			runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status,
				Via: "steer", Reason: "turn_terminal"})
		return "", store.ErrFenced
	}
	steered, err := t.client.SteerTurn(ctx, t.threadID, t.turnID, text)
	if err != nil && classifyRuntimeError(err) == causeThreadUnavailable {
		// One resume-before-write on a fresh connection, then one retry.
		if _, resumeErr := t.client.ResumeThread(ctx, t.threadID, true); resumeErr == nil {
			steered, err = t.client.SteerTurn(ctx, t.threadID, t.turnID, text)
		}
	}
	if err != nil {
		t.journal(RunEventSteerRejected, researchcontract.OutcomeUncertain,
			runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status,
				Via: "steer", Reason: steerFailureReason(err)})
		// Stop/reconcile fallback: halt the unverified turn and settle it.
		_, _ = t.interrupt(ctx)
		return "", errors.Join(store.ErrUncertain, err)
	}
	if steered != t.turnID {
		t.journal(RunEventSteerRejected, researchcontract.OutcomeUncertain,
			runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status,
				Via: "steer", Reason: "turn_mismatch"})
		_, _ = t.interrupt(ctx)
		return "", errors.Join(store.ErrUncertain, errSteerMismatch)
	}
	// Verify the steered turn is still observable and in flight.
	status, err = waitTurnStatus(ctx, t.client, t.threadID, t.turnID, 3, t.bounds.observePause)
	if err != nil {
		_, _ = t.interrupt(ctx)
		return "", errors.Join(store.ErrUncertain, err)
	}
	if status != "inProgress" {
		t.recordTerminal(ctx, status, "steer-raced")
		t.journal(RunEventSteered, researchcontract.OutcomeUncertain,
			runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status,
				Via: "steer", Applied: "unconfirmed"})
		return "", errors.Join(store.ErrUncertain, errSteerUnconfirmed)
	}
	t.journal(RunEventSteered, researchcontract.OutcomeOK,
		runTurnRecord{ThreadID: t.threadID, TurnID: t.turnID, Status: status,
			Via: "steer", Verified: true})
	return steered, nil
}
