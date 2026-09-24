package codexservice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// T08 focused protocol tests: correlation, cleanup and recovery inputs at the
// settled boundaries. The App Server is a net.Pipe double (installRuntime),
// the research store is never touched, and every database lives in t.TempDir.

func testBounds() conversationBounds {
	return conversationBounds{observeTries: 4, interruptTries: 3, confirmTries: 4}
}

func conversationRound(t *testing.T, db *store.Store, key string) (store.Round, store.Actor, store.Actor) {
	t.Helper()
	ctx := context.Background()
	owner := store.Actor{Kind: "administrator", ID: "owner"}
	agent := store.Actor{Kind: "agent", ID: "codex-runner"}
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := db.StartRound(ctx, owner, store.StartRoundInput{RequestKey: key, Intent: "Process supplied input", Outcome: "process_input", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{Resources: []string{"campaign:active"}, Operations: []string{store.RoundCodexTurn, store.RoundContextTool}, Delegates: []string{agent.ID}}, Limits: store.RoundAllowance{Requests: 8, Tools: 16, Turns: 6}})
	if err != nil || !created {
		t.Fatalf("start: %v", err)
	}
	r, err = db.ActivateRound(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r, owner, agent
}

func conversationAttempt(t *testing.T, db *store.Store, r store.Round, agent store.Actor, key string) store.RoundAttempt {
	t.Helper()
	ctx := context.Background()
	cost, ok := store.RoundOperationCost(store.RoundCodexTurn)
	if !ok {
		t.Fatal("round turn cost missing")
	}
	a, created, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: key, Operation: store.RoundCodexTurn, ResourceID: "campaign:active", Cost: cost})
	if err != nil || !created {
		t.Fatalf("reserve: %v", err)
	}
	if a, err = db.MarkRoundDispatched(ctx, r.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.BindRoundThread(ctx, r.ID, a.ID, a.Generation, "thread-a"); err != nil {
		t.Fatal(err)
	}
	if err = db.BindRoundTurn(ctx, r.ID, a.ID, a.Generation, "thread-a", "turn-a"); err != nil {
		t.Fatal(err)
	}
	return a
}

func journalFor(t *testing.T, s *Service, runID string) []researchcontract.Event {
	t.Helper()
	events, next, err := s.RunEventSink().List(context.Background(), runID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatal("unexpected journal page")
	}
	return events
}

func kindCounts(events []researchcontract.Event) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		out[e.Kind]++
	}
	return out
}

func findKind(t *testing.T, events []researchcontract.Event, kind string) researchcontract.Event {
	t.Helper()
	for _, e := range events {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("journal lacks %s: %v", kind, kindCounts(events))
	return researchcontract.Event{}
}

func TestRunEventSinkAppendListDedups(t *testing.T) {
	ctx := context.Background()
	sink := NewRunEventSink()
	at := time.Now()
	bad := []researchcontract.Event{
		{RunID: "r", Kind: "k", ObservedAt: at},                            // empty ID
		{ID: "a", Kind: "k", ObservedAt: at},                               // empty run
		{ID: "a", RunID: "r", ObservedAt: at},                              // empty kind
		{ID: "a", RunID: "r", Kind: "k"},                                   // zero observed
		{ID: "a", RunID: "r", Kind: "k", ObservedAt: at, Outcome: "bogus"}, // invalid outcome
		{ID: "a", RunID: "r", Kind: "k", ObservedAt: at, Payload: json.RawMessage(`"` + strings.Repeat("x", maxRunEventPayload) + `"`)},
	}
	for i, e := range bad {
		if err := sink.Append(ctx, e); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := sink.Append(canceled, researchcontract.Event{ID: "a", RunID: "r", Kind: "k", ObservedAt: at}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled append: %v", err)
	}
	// Interleaved runs page independently in journal order.
	for i := 0; i < 5; i++ {
		if err := sink.Append(ctx, researchcontract.Event{ID: "a" + string(rune('0'+i)), RunID: "run-a", Kind: "k", ObservedAt: at}); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := sink.Append(ctx, researchcontract.Event{ID: "b" + string(rune('0'+i)), RunID: "run-b", Kind: "k", ObservedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Idempotent re-append keeps the first write.
	if err := sink.Append(ctx, researchcontract.Event{ID: "a0", RunID: "run-a", Kind: "other", ObservedAt: at}); err != nil {
		t.Fatal(err)
	}
	page, next, err := sink.List(ctx, "run-a", "", 2)
	if err != nil || len(page) != 2 || page[0].ID != "a0" || page[0].Kind != "k" || page[1].ID != "a1" || next != "a1" {
		t.Fatalf("page one: %+v %q %v", page, next, err)
	}
	if page[0].RecordedAt.IsZero() {
		t.Fatal("recorded timestamp not defaulted")
	}
	page, next, err = sink.List(ctx, "run-a", next, 2)
	if err != nil || len(page) != 2 || page[0].ID != "a2" || next != "a3" {
		t.Fatalf("page two: %+v %q %v", page, next, err)
	}
	page, next, err = sink.List(ctx, "run-a", next, 2)
	if err != nil || len(page) != 1 || page[0].ID != "a4" || next != "" {
		t.Fatalf("page three: %+v %q %v", page, next, err)
	}
	if _, _, err = sink.List(ctx, "run-a", "missing", 2); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("unknown cursor: %v", err)
	}
	all, next, err := sink.List(ctx, "run-a", "", 0)
	if err != nil || len(all) != 5 || next != "" {
		t.Fatalf("default limit: %d %q %v", len(all), next, err)
	}
	other, next, err := sink.List(ctx, "run-b", "", 1000)
	if err != nil || len(other) != 2 || next != "" {
		t.Fatalf("run-b: %d %q %v", len(other), next, err)
	}
	if _, _, err = sink.List(canceled, "run-a", "", 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list: %v", err)
	}
}

func TestItemCorrelatorPairsStartAndComplete(t *testing.T) {
	sink := NewRunEventSink()
	corr := NewItemCorrelator(sink)
	at := time.Now()
	start := codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", ItemType: "agentMessage", State: "started"}
	corr.ObserveItem("run-a", "attempt-a", start, at)
	corr.ObserveItem("run-a", "attempt-a", start, at) // duplicate start dedups
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", ItemType: "agentMessage", State: "completed"}, at)
	// Malformed and unattributable input never reaches the sink.
	corr.ObserveItem("", "attempt-a", start, at)
	corr.ObserveItem("run-a", "", start, at)
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{State: "started"}, at)
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-b", State: "delta"}, at)
	events, _, err := sink.List(context.Background(), "run-a", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != RunEventItemStarted || events[1].Kind != RunEventItemCompleted {
		t.Fatalf("journal: %+v", events)
	}
	var completed runItemRecord
	if json.Unmarshal(events[1].Payload, &completed) != nil || completed.StartedEventID != events[0].ID || completed.ItemType != "agentMessage" {
		t.Fatalf("uncorrelated completion: %s", events[1].Payload)
	}
	if strings.Contains(string(events[0].Payload)+string(events[1].Payload), "raw") {
		t.Fatal("raw protocol bytes leaked into journal")
	}
}

func TestItemCorrelatorFlagsUnmatchedCompletion(t *testing.T) {
	sink := NewRunEventSink()
	corr := NewItemCorrelator(sink)
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-x", ItemType: "mcpToolCall", State: "completed"}, time.Now())
	events, _, err := sink.List(context.Background(), "run-a", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != RunEventItemUnmatched || events[0].Outcome != researchcontract.OutcomeUncertain {
		t.Fatalf("journal: %+v", events)
	}
	var payload runItemRecord
	if json.Unmarshal(events[0].Payload, &payload) != nil || payload.Reason != "missing_start" || payload.ItemID != "item-x" {
		t.Fatalf("payload: %s", events[0].Payload)
	}
}

func TestItemCorrelatorCloseTurnCleansOpenItems(t *testing.T) {
	sink := NewRunEventSink()
	corr := NewItemCorrelator(sink)
	at := time.Now()
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", ItemType: "agentMessage", State: "started"}, at)
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-b", ItemType: "mcpToolCall", State: "started"}, at)
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-b", ItemID: "item-c", ItemType: "agentMessage", State: "started"}, at)
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-a", ItemID: "item-a", State: "completed"}, at)
	corr.CloseTurn("run-a", "attempt-a", "thread-a", "turn-a", "completed", at)
	before, _, err := sink.List(context.Background(), "run-a", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := kindCounts(before)
	if counts[RunEventItemStarted] != 3 || counts[RunEventItemCompleted] != 1 || counts[RunEventTurnObserved] != 1 || counts[RunEventItemUnmatched] != 1 {
		t.Fatalf("counts: %v", counts)
	}
	unmatched := findKind(t, before, RunEventItemUnmatched)
	var payload runItemRecord
	if json.Unmarshal(unmatched.Payload, &payload) != nil || payload.ItemID != "item-b" || payload.Reason != "missing_completion" || payload.StartedEventID == "" {
		t.Fatalf("unresolved payload: %s", unmatched.Payload)
	}
	// Repeat close is idempotent; the other turn's item stays open and still pairs.
	corr.CloseTurn("run-a", "attempt-a", "thread-a", "turn-a", "completed", at)
	after, _, _ := sink.List(context.Background(), "run-a", "", 100)
	if len(after) != len(before) {
		t.Fatalf("repeat close grew journal: %d -> %d", len(before), len(after))
	}
	corr.ObserveItem("run-a", "attempt-a", codex.ItemEvent{ThreadID: "thread-a", TurnID: "turn-b", ItemID: "item-c", State: "completed"}, at)
	paired, _, _ := sink.List(context.Background(), "run-a", "", 100)
	if kindCounts(paired)[RunEventItemCompleted] != 2 {
		t.Fatalf("other turn item lost: %v", kindCounts(paired))
	}
	// An unknown turn outcome journals uncertain, not a fabricated terminal.
	corr.CloseTurn("run-a", "attempt-b", "thread-a", "turn-b", "unknown", at)
	events, _, _ := sink.List(context.Background(), "run-a", "", 100)
	if findKind(t, events, RunEventUncertain).Outcome != researchcontract.OutcomeUncertain {
		t.Fatal("unknown turn not uncertain")
	}
}

func TestRoundTurnJournalsCorrelatedItems(t *testing.T) {
	s, db := testService(t, testConfig())
	f := installRuntime(t, s)
	f.completeStatus = "completed"
	r, _, agent := roundTurnFixture(t, db)
	ctx := boundedContext(t)
	attempt, err := s.ExecuteRoundTurn(ctx, agent, r.ID, turnInput())
	if err != nil || attempt.State != store.AttemptSucceeded {
		t.Fatalf("turn: %+v %v", attempt, err)
	}
	events := journalFor(t, s, r.ID)
	counts := kindCounts(events)
	if counts[RunEventItemStarted] != 1 || counts[RunEventItemCompleted] != 1 || counts[RunEventTurnObserved] != 1 || len(events) != 3 {
		t.Fatalf("journal: %v", counts)
	}
	var completed runItemRecord
	done := findKind(t, events, RunEventItemCompleted)
	if json.Unmarshal(done.Payload, &completed) != nil || completed.StartedEventID != findKind(t, events, RunEventItemStarted).ID {
		t.Fatalf("live items uncorrelated: %s", done.Payload)
	}
	observed := findKind(t, events, RunEventTurnObserved)
	if observed.Outcome != researchcontract.OutcomeOK || observed.AttemptID != attempt.ID {
		t.Fatalf("live turn misjournaled: %+v", observed)
	}
}

func TestInterruptAttemptInterruptsActiveTurn(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.interruptSettles = "interrupted"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "interrupt-live")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	status, err := s.InterruptAttempt(ctx, r.ID, a.ID)
	if err != nil || status != "interrupted" {
		t.Fatalf("interrupt: %q %v", status, err)
	}
	if f.count("turn/interrupt") != 1 || f.count("thread/resume") != 0 {
		t.Fatal("interrupt was not a single observed write")
	}
	if f.count("turn/start") != 0 || f.count("thread/start") != 0 {
		t.Fatal("interrupt replayed the turn")
	}
	remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID)
	if err != nil || remote.ObservedStatus != "interrupted" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	events := journalFor(t, s, r.ID)
	done := findKind(t, events, RunEventInterrupted)
	var payload runTurnRecord
	if json.Unmarshal(done.Payload, &payload) != nil || !payload.Verified || !payload.Persisted || payload.TurnID != "turn-a" {
		t.Fatalf("interrupt misjournaled: %s", done.Payload)
	}
	if done.Outcome != researchcontract.OutcomeStopped {
		t.Fatalf("interrupt outcome: %s", done.Outcome)
	}
}

func TestInterruptAttemptRetriesBlindInterrupt(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.interruptFails = 1
	f.interruptSettles = "interrupted"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "interrupt-blind")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	status, err := s.InterruptAttempt(ctx, r.ID, a.ID)
	if err != nil || status != "interrupted" {
		t.Fatalf("interrupt: %q %v", status, err)
	}
	// The first blind interrupt races attach (-32600): one resume, one retry.
	if f.count("turn/interrupt") != 2 || f.count("thread/resume") != 1 {
		t.Fatalf("interrupt=%d resume=%d", f.count("turn/interrupt"), f.count("thread/resume"))
	}
	if remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID); err != nil || remote.ObservedStatus != "interrupted" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
}

func TestInterruptAttemptLeavesTerminalTurnAlone(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "completed"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "interrupt-settled")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	status, err := s.InterruptAttempt(ctx, r.ID, a.ID)
	if err != nil || status != "completed" {
		t.Fatalf("interrupt: %q %v", status, err)
	}
	if f.count("turn/interrupt") != 0 || f.count("thread/resume") != 0 {
		t.Fatal("settled turn was written to")
	}
	if remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID); err != nil || remote.ObservedStatus != "completed" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	if findKind(t, journalFor(t, s, r.ID), RunEventTurnObserved).Outcome != researchcontract.OutcomeOK {
		t.Fatal("settled turn misjournaled")
	}
}

func TestInterruptAttemptToleratesTransientListErrors(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.listFails = 2
	f.interruptSettles = "interrupted"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "interrupt-transient")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	status, err := s.InterruptAttempt(ctx, r.ID, a.ID)
	if err != nil || status != "interrupted" {
		t.Fatalf("interrupt: %q %v", status, err)
	}
	f.mu.Lock()
	remaining := f.listFails
	f.mu.Unlock()
	if remaining != 0 || f.count("thread/turns/list") < 3 {
		t.Fatalf("transient errors not tolerated: remaining=%d lists=%d", remaining, f.count("thread/turns/list"))
	}
}

func TestInterruptAttemptExhaustsBudgetThenUncertain(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.interruptFails = 99
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "interrupt-budget")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	status, err := s.InterruptAttempt(ctx, r.ID, a.ID)
	if !errors.Is(err, store.ErrUncertain) || status != "" {
		t.Fatalf("interrupt: %q %v", status, err)
	}
	// Bounded: exactly the configured tries, one resume, then honest uncertain.
	if f.count("turn/interrupt") != testBounds().interruptTries || f.count("thread/resume") != 1 {
		t.Fatalf("interrupt=%d resume=%d", f.count("turn/interrupt"), f.count("thread/resume"))
	}
	if f.count("turn/start") != 0 {
		t.Fatal("exhausted interrupt replayed the turn")
	}
	if remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID); err != nil || remote.ObservedStatus != "unknown" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	events := journalFor(t, s, r.ID)
	uncertain := findKind(t, events, RunEventUncertain)
	var payload runTurnRecord
	if json.Unmarshal(uncertain.Payload, &payload) != nil || payload.Reason != "unconfirmed" || !payload.Persisted {
		t.Fatalf("uncertain misjournaled: %s", uncertain.Payload)
	}
}

func TestConversationRefusesWithoutRemoteIDs(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	r, _, agent := conversationRound(t, db, "missing-ids")
	cost, _ := store.RoundOperationCost(store.RoundCodexTurn)
	bare, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "bare", Operation: store.RoundCodexTurn, ResourceID: "campaign:active", Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkRoundDispatched(ctx, r.ID, bare.ID); err != nil {
		t.Fatal(err)
	}
	threadOnly, _, err := db.ReserveRoundAttempt(ctx, agent, r.ID, store.RoundAttemptInput{RequestKey: "thread-only", Operation: store.RoundCodexTurn, ResourceID: "campaign:active", Cost: cost})
	if err != nil {
		t.Fatal(err)
	}
	if threadOnly, err = db.MarkRoundDispatched(ctx, r.ID, threadOnly.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.BindRoundThread(ctx, r.ID, threadOnly.ID, threadOnly.Generation, "thread-a"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InterruptAttempt(ctx, r.ID, bare.ID); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("bare interrupt: %v", err)
	}
	if _, err = s.SteerAttempt(ctx, r.ID, threadOnly.ID, "owner steer"); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("thread-only steer: %v", err)
	}
	if _, err = s.InterruptAttempt(ctx, "wrong-round", bare.ID); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("wrong round: %v", err)
	}
	for _, method := range []string{"thread/turns/list", "thread/read", "turn/interrupt", "turn/steer", "thread/resume", "turn/start"} {
		if f.count(method) != 0 {
			t.Fatalf("missing IDs reached the runtime via %s", method)
		}
	}
	uncertain := findKind(t, journalFor(t, s, r.ID), RunEventUncertain)
	var payload runTurnRecord
	if json.Unmarshal(uncertain.Payload, &payload) != nil || payload.Reason != "missing_remote_ids" {
		t.Fatalf("payload: %s", uncertain.Payload)
	}
}

func TestInterruptDisconnectStaysUncertainWithoutReplay(t *testing.T) {
	ctx := boundedContext(t)
	left, right := net.Pipe()
	done := make(chan struct{})
	kill := make(chan struct{})
	var killOnce sync.Once
	go func() {
		defer close(done)
		defer right.Close()
		reader := bufio.NewReader(right)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &req) != nil {
			return
		}
		raw, _ := json.Marshal(map[string]any{"id": req.ID, "result": map[string]any{
			"userAgent": "jobseek_dashboard/0.153.4 (Linux; x86_64)", "codexHome": "/runner/state",
			"platformFamily": "unix", "platformOs": "linux"}})
		if _, err := right.Write(append(raw, '\n')); err != nil {
			return
		}
		// Consume "initialized", then wait for the kill signal so the
		// handshake fully settles before the transport loss mid-conversation.
		if _, err := reader.ReadBytes('\n'); err != nil {
			return
		}
		<-kill
	}()
	t.Cleanup(func() { killOnce.Do(func() { close(kill) }); <-done })
	client, err := codex.NewClient(left, codex.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	killOnce.Do(func() { close(kill) })
	<-done
	var mu sync.Mutex
	var persists []string
	ctl := &turnControl{
		client: client, sink: NewRunEventSink(),
		roundID: "round-x", attemptID: "attempt-x", generation: 1,
		threadID: "thread-a", turnID: "turn-a", bounds: testBounds(),
		persist: func(_ context.Context, status string, _ json.RawMessage) error {
			mu.Lock()
			persists = append(persists, status)
			mu.Unlock()
			return nil
		},
	}
	status, err := ctl.interrupt(ctx)
	if !errors.Is(err, store.ErrUncertain) || status != "" {
		t.Fatalf("interrupt: %q %v", status, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(persists) != 1 || persists[0] != "unknown" {
		t.Fatalf("persists: %v", persists)
	}
	events, _, listErr := ctl.sink.List(ctx, "round-x", "", 100)
	if listErr != nil || len(events) != 1 || events[0].Kind != RunEventUncertain {
		t.Fatalf("journal: %+v %v", events, listErr)
	}
	var payload runTurnRecord
	if json.Unmarshal(events[0].Payload, &payload) != nil || payload.Reason != "disconnected" {
		t.Fatalf("payload: %s", events[0].Payload)
	}
}

func TestSteerAttemptVerifiesSteering(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "steer-verified")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	steered, err := s.SteerAttempt(ctx, r.ID, a.ID, "owner steer text")
	if err != nil || steered != "turn-a" {
		t.Fatalf("steer: %q %v", steered, err)
	}
	if f.count("turn/steer") != 1 || f.count("turn/interrupt") != 0 || f.count("thread/resume") != 0 {
		t.Fatal("verified steer took an unexpected path")
	}
	if f.count("turn/start") != 0 {
		t.Fatal("steer started a new turn")
	}
	// A verified steer writes no observation: the turn is still in flight.
	if remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID); err != nil || remote.ObservedStatus != "" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	events := journalFor(t, s, r.ID)
	steer := findKind(t, events, RunEventSteered)
	var payload runTurnRecord
	if json.Unmarshal(steer.Payload, &payload) != nil || !payload.Verified || steer.Outcome != researchcontract.OutcomeOK {
		t.Fatalf("steer misjournaled: %s", steer.Payload)
	}
}

func TestSteerAttemptFallsBackToStopOnMismatch(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.steerMismatch = true
	f.interruptSettles = "interrupted"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "steer-mismatch")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	steered, err := s.SteerAttempt(ctx, r.ID, a.ID, "owner steer text")
	if !errors.Is(err, store.ErrUncertain) || steered != "" {
		t.Fatalf("steer: %q %v", steered, err)
	}
	// Stop/reconcile fallback: the unverified turn is halted and settled.
	if f.count("turn/steer") != 1 || f.count("turn/interrupt") != 1 {
		t.Fatalf("steer=%d interrupt=%d", f.count("turn/steer"), f.count("turn/interrupt"))
	}
	if remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID); err != nil || remote.ObservedStatus != "interrupted" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	events := journalFor(t, s, r.ID)
	rejected := findKind(t, events, RunEventSteerRejected)
	var payload runTurnRecord
	if json.Unmarshal(rejected.Payload, &payload) != nil || payload.Reason != "turn_mismatch" {
		t.Fatalf("rejection misjournaled: %s", rejected.Payload)
	}
	findKind(t, events, RunEventInterrupted)
}

func TestSteerAttemptFallsBackToStopOnRejection(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "inProgress"
	f.steerFails = true
	f.interruptSettles = "interrupted"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "steer-rejected")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	steered, err := s.SteerAttempt(ctx, r.ID, a.ID, "owner steer text")
	if !errors.Is(err, store.ErrUncertain) || steered != "" {
		t.Fatalf("steer: %q %v", steered, err)
	}
	// One resume-before-write, one retry, then the stop/reconcile fallback.
	if f.count("turn/steer") != 2 || f.count("thread/resume") != 1 || f.count("turn/interrupt") != 1 {
		t.Fatalf("steer=%d resume=%d interrupt=%d", f.count("turn/steer"), f.count("thread/resume"), f.count("turn/interrupt"))
	}
	if remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID); err != nil || remote.ObservedStatus != "interrupted" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	events := journalFor(t, s, r.ID)
	rejected := findKind(t, events, RunEventSteerRejected)
	var payload runTurnRecord
	if json.Unmarshal(rejected.Payload, &payload) != nil || payload.Reason != "thread_unavailable" {
		t.Fatalf("rejection misjournaled: %s", rejected.Payload)
	}
	findKind(t, events, RunEventInterrupted)
}

func TestSteerAttemptRejectsTerminalTurn(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	f.mu.Lock()
	f.historyStatus = "failed"
	f.mu.Unlock()
	r, _, agent := conversationRound(t, db, "steer-terminal")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	steered, err := s.SteerAttempt(ctx, r.ID, a.ID, "owner steer text")
	if !errors.Is(err, store.ErrFenced) || steered != "" {
		t.Fatalf("steer: %q %v", steered, err)
	}
	if f.count("turn/steer") != 0 || f.count("turn/interrupt") != 0 {
		t.Fatal("settled turn was written to")
	}
	if remote, err := db.RoundRemoteDispatch(ctx, r.ID, a.ID); err != nil || remote.ObservedStatus != "failed" {
		t.Fatalf("remote: %+v %v", remote, err)
	}
	events := journalFor(t, s, r.ID)
	rejected := findKind(t, events, RunEventSteerRejected)
	var payload runTurnRecord
	if json.Unmarshal(rejected.Payload, &payload) != nil || payload.Reason != "turn_terminal" || payload.Status != "failed" {
		t.Fatalf("rejection misjournaled: %s", rejected.Payload)
	}
}

func TestSteerAttemptValidatesOwnerText(t *testing.T) {
	s, _ := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	// Bogus IDs prove validation precedes every load and RPC.
	for _, text := range []string{"", "   ", strings.Repeat("x", maxSteerText+1)} {
		if _, err := s.SteerAttempt(ctx, "no-round", "no-attempt", text); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("text %q: %v", text, err)
		}
	}
	if _, err := s.SteerAttempt(nil, "no-round", "no-attempt", "text"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("nil ctx: %v", err)
	}
	if _, err := s.InterruptAttempt(nil, "no-round", "no-attempt"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("nil ctx interrupt: %v", err)
	}
	for _, method := range []string{"account/read", "thread/turns/list", "turn/steer", "turn/interrupt", "thread/resume"} {
		if f.count(method) != 0 {
			t.Fatalf("invalid input reached the runtime via %s", method)
		}
	}
}

func TestResumeStoredThreadLoadsConversation(t *testing.T) {
	s, _ := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	if err := s.ResumeStoredThread(ctx, "", "thread-a"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty run: %v", err)
	}
	if err := s.ResumeStoredThread(ctx, "run-a", ""); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("empty thread: %v", err)
	}
	if f.count("thread/resume") != 0 {
		t.Fatal("invalid resume reached the runtime")
	}
	if err := s.ResumeStoredThread(ctx, "run-a", "thread-a"); err != nil {
		t.Fatal(err)
	}
	if f.count("thread/resume") != 1 {
		t.Fatal("stored conversation was not resumed")
	}
	events := journalFor(t, s, "run-a")
	resumed := findKind(t, events, RunEventResumed)
	if resumed.Outcome != researchcontract.OutcomeOK || !strings.Contains(string(resumed.Payload), "thread-a") {
		t.Fatalf("resume misjournaled: %s", resumed.Payload)
	}
}

func TestConversationNotesUnknownEvents(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	ctx := boundedContext(t)
	if status := s.Status(ctx); status.State != "ready" {
		t.Fatalf("warmup: %+v", status)
	}
	f.mu.Lock()
	f.historyStatus = "completed"
	f.mu.Unlock()
	f.notify("novel/future", map[string]any{"threadId": "thread-a"})
	r, _, agent := conversationRound(t, db, "unknown-events")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	status, err := s.InterruptAttempt(ctx, r.ID, a.ID)
	if err != nil || status != "completed" {
		t.Fatalf("interrupt: %q %v", status, err)
	}
	events := journalFor(t, s, r.ID)
	novel := findKind(t, events, RunEventUnknownEvents)
	var payload struct {
		Count uint64 `json:"count"`
	}
	if json.Unmarshal(novel.Payload, &payload) != nil || payload.Count != 1 {
		t.Fatalf("unknown events misjournaled: %s", novel.Payload)
	}
	if strings.Contains(string(novel.Payload), "novel") {
		t.Fatal("raw unknown payload leaked into journal")
	}
}

func TestConversationRefusesWhileBusy(t *testing.T) {
	s, db := testService(t, testConfig())
	s.converse = testBounds()
	f := installRuntime(t, s)
	r, _, agent := conversationRound(t, db, "busy-guard")
	a := conversationAttempt(t, db, r, agent, "turn-one")
	ctx := boundedContext(t)
	s.mu.Lock()
	s.busy = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }()
	if _, err := s.InterruptAttempt(ctx, r.ID, a.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("interrupt: %v", err)
	}
	if _, err := s.SteerAttempt(ctx, r.ID, a.ID, "owner steer"); !errors.Is(err, ErrBusy) {
		t.Fatalf("steer: %v", err)
	}
	if err := s.ResumeStoredThread(ctx, r.ID, "thread-a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("resume: %v", err)
	}
	for _, method := range []string{"account/read", "thread/turns/list", "turn/steer", "turn/interrupt", "thread/resume"} {
		if f.count(method) != 0 {
			t.Fatalf("busy guard dialed the runtime via %s", method)
		}
	}
}

func TestRuntimeCauseClassification(t *testing.T) {
	cases := []struct {
		err   error
		cause runtimeCause
		why   string
	}{
		{context.Canceled, causeCanceled, "canceled"},
		{context.DeadlineExceeded, causeCanceled, "canceled"},
		{codex.ErrBackpressure, causeBackpressure, "backpressure"},
		{codex.ErrClosed, causeDisconnected, "disconnected"},
		{codex.ErrUnavailable, causeDisconnected, "disconnected"},
		{&codex.RPCError{Code: -32601}, causeTransientList, "transient_history"},
		{&codex.RPCError{Code: -32600}, causeThreadUnavailable, "thread_unavailable"},
		{&codex.RPCError{Code: -32000}, causeOther, "unconfirmed"},
		{codex.ErrHistoryIncomplete, causeOther, "history_incomplete"},
		{errors.New("boom"), causeOther, "unconfirmed"},
	}
	for _, c := range cases {
		if got := classifyRuntimeError(c.err); got != c.cause {
			t.Fatalf("%v: cause %d", c.err, got)
		}
		if got := uncertainReason(c.err); got != c.why {
			t.Fatalf("%v: reason %q", c.err, got)
		}
	}
	// Only the transient list cause is ever retried: backpressure and
	// disconnect settle immediately with no further writes by construction.
	if classifyRuntimeError(codex.ErrBackpressure) == causeTransientList ||
		classifyRuntimeError(codex.ErrUnavailable) == causeTransientList {
		t.Fatal("backpressure or disconnect became retryable")
	}
}
