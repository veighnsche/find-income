package musecode

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptTransport replays canned events without touching the CLI.
type scriptTransport struct {
	events     []Event
	returnErr  error
	waitFor    <-chan struct{}
	emitAfter  []Event
	gotResume  Cursor
	resumeSeen bool
}

func (f *scriptTransport) Run(ctx context.Context, _ SessionSpec, _ SessionInput, resume Cursor, sink EventSink) error {
	f.gotResume = resume
	f.resumeSeen = true
	for _, event := range f.events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sink.Emit(event)
	}
	if f.waitFor != nil {
		select {
		case <-f.waitFor:
		case <-ctx.Done():
		}
		for _, event := range f.emitAfter {
			sink.Emit(event)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return f.returnErr
}

type memoryCursors struct {
	mu      sync.Mutex
	cursors map[string]Cursor
	fail    error
}

func (m *memoryCursors) SaveCursor(_ context.Context, cursor Cursor) error {
	if m.fail != nil {
		return m.fail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cursors == nil {
		m.cursors = map[string]Cursor{}
	}
	m.cursors[cursor.RunRef] = cursor
	return nil
}

func (m *memoryCursors) LoadCursor(_ context.Context, runRef string) (Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cursor, ok := m.cursors[runRef]
	if !ok {
		return Cursor{}, errors.New("no cursor for run")
	}
	return cursor, nil
}

func testSpec(t *testing.T, tier Tier, bounds Bounds) SessionSpec {
	t.Helper()
	spec, err := NewSession(Check(tier, readyFacts()), "/tmp/"+string(tier), bounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func acceptAll(string) error { return nil }

func TestAdmissionIsNotCompletion(t *testing.T) {
	transport := &scriptTransport{events: []Event{{Kind: EventFinished}}}
	supervisor := NewSupervisor(transport, &memoryCursors{}, acceptAll, readyFacts().EffectiveModel)
	admission, err := supervisor.StartRun(context.Background(), "run-1",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts())
	if err != nil {
		t.Fatal(err)
	}
	if admission.RunRef != "run-1" || admission.Tier != TierContributor {
		t.Fatalf("admission = %+v, want run-1 contributor", admission)
	}
	result, ok := supervisor.Result("run-1")
	if !ok || result.Outcome != OutcomeCompleted {
		t.Fatalf("result = %+v ok=%v, want completed", result, ok)
	}
	if _, ok := supervisor.Result("run-absent"); ok {
		t.Error("absent run reported a terminal result")
	}
}

func TestWrongTierInputAndPinMismatchFailClosed(t *testing.T) {
	supervisor := NewSupervisor(&scriptTransport{}, &memoryCursors{}, acceptAll, "pinned-model")
	facts := readyFacts()
	facts.EffectiveModel = "pinned-model"
	if _, err := supervisor.StartRun(context.Background(), "run-tier",
		testSpec(t, TierStandard, DefaultBounds()), PublicInput{}, facts); err == nil {
		t.Error("contributor input admitted to standard session")
	}
	if _, err := supervisor.StartRun(context.Background(), "run-tier-std",
		testSpec(t, TierContributor, DefaultBounds()), StandardInput{Purpose: "draft"}, facts); err == nil {
		t.Error("standard input admitted to contributor session")
	}
	admitted, err := supervisor.StartRun(context.Background(), "run-tier-ok",
		testSpec(t, TierStandard, DefaultBounds()), StandardInput{Purpose: "draft"}, facts)
	if err != nil {
		t.Fatalf("standard input rejected by standard session: %v", err)
	}
	if admitted.Tier != TierStandard {
		t.Errorf("admission = %+v, want standard tier", admitted)
	}
	drifted := facts
	drifted.EffectiveModel = "other-model"
	if _, err := supervisor.StartRun(context.Background(), "run-drift",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, drifted); err == nil {
		t.Error("drifted effective model admitted")
	}
	unproved := facts
	unproved.SubscriptionLaneProved = false
	if _, err := supervisor.StartRun(context.Background(), "run-lane",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, unproved); err == nil {
		t.Error("unproved subscription lane admitted")
	}
}

func TestNonAllowlistedToolAndBadSaveFailClosed(t *testing.T) {
	transport := &scriptTransport{events: []Event{
		{Kind: EventToolCall, Tool: "context_read"},
		{Kind: EventFinished},
	}}
	supervisor := NewSupervisor(transport, &memoryCursors{}, acceptAll, readyFacts().EffectiveModel)
	if _, err := supervisor.StartRun(context.Background(), "run-tool",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	result, _ := supervisor.Result("run-tool")
	if result.Outcome != OutcomeFailed || !strings.Contains(result.Detail, "context_read") {
		t.Fatalf("result = %+v, want failed naming context_read", result)
	}
	if len(result.SavedRefs) != 0 {
		t.Fatalf("saved refs = %v, want none", result.SavedRefs)
	}

	rejecting := NewSupervisor(&scriptTransport{events: []Event{
		{Kind: EventSaved, SaveRef: "save-1"},
		{Kind: EventFinished},
	}}, &memoryCursors{}, func(string) error { return errors.New("unknown ref") }, readyFacts().EffectiveModel)
	if _, err := rejecting.StartRun(context.Background(), "run-save",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	saved, _ := rejecting.Result("run-save")
	if saved.Outcome != OutcomeFailed || len(saved.SavedRefs) != 0 {
		t.Fatalf("result = %+v, want failed with no saves", saved)
	}
}

func TestBoundBreachExpiresTheRun(t *testing.T) {
	bounds := DefaultBounds()
	bounds.MaxModelSteps = 2
	transport := &scriptTransport{events: []Event{
		{Kind: EventModelStep}, {Kind: EventModelStep}, {Kind: EventModelStep},
		{Kind: EventFinished},
	}}
	supervisor := NewSupervisor(transport, &memoryCursors{}, acceptAll, readyFacts().EffectiveModel)
	if _, err := supervisor.StartRun(context.Background(), "run-bound",
		testSpec(t, TierContributor, bounds), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	result, _ := supervisor.Result("run-bound")
	if result.Outcome != OutcomeFailed || !strings.Contains(result.Detail, "model-step") {
		t.Fatalf("result = %+v, want failed on model-step bound", result)
	}
}

func TestStopFencesLateTools(t *testing.T) {
	release := make(chan struct{})
	transport := &scriptTransport{
		events:    []Event{{Kind: EventSaved, SaveRef: "save-1"}},
		waitFor:   release,
		emitAfter: []Event{{Kind: EventToolResult, Tool: "public_fetch"}, {Kind: EventSaved, SaveRef: "save-late"}},
	}
	supervisor := NewSupervisor(transport, &memoryCursors{}, acceptAll, readyFacts().EffectiveModel)
	done := make(chan error, 1)
	go func() {
		_, err := supervisor.StartRun(context.Background(), "run-stop",
			testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts())
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	supervisor.Stop("run-stop", "owner stop")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	result, ok := supervisor.Result("run-stop")
	if !ok || result.Outcome != OutcomeStopped {
		t.Fatalf("result = %+v ok=%v, want stopped", result, ok)
	}
	if len(result.SavedRefs) != 1 || result.SavedRefs[0] != "save-1" {
		t.Fatalf("saved refs = %v, want only the pre-stop save", result.SavedRefs)
	}
}

func TestDisconnectWritesCursorAndResumeRefusesReplay(t *testing.T) {
	cursors := &memoryCursors{}
	dying := &scriptTransport{
		events:    []Event{{Kind: EventSaved, SaveRef: "save-1"}},
		returnErr: ErrDisconnected,
	}
	supervisor := NewSupervisor(dying, cursors, acceptAll, readyFacts().EffectiveModel)
	if _, err := supervisor.StartRun(context.Background(), "run-crash",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	crashed, _ := supervisor.Result("run-crash")
	if crashed.Outcome != OutcomeCrashed {
		t.Fatalf("result = %+v, want crashed", crashed)
	}
	cursor, err := cursors.LoadCursor(context.Background(), "run-crash")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastSavedReceipt != "save-1" || cursor.SavedCount != 1 {
		t.Fatalf("cursor = %+v, want save-1 count 1", cursor)
	}
	if len(cursor.SavedRefs) != 1 || cursor.SavedRefs[0] != "save-1" {
		t.Fatalf("cursor refs = %v, want [save-1]", cursor.SavedRefs)
	}

	resuming := &scriptTransport{events: []Event{
		{Kind: EventSaved, SaveRef: "save-1"},
		{Kind: EventFinished},
	}}
	supervisor.transport = resuming
	if _, err := supervisor.Resume(context.Background(), "run-crash",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	if !resuming.resumeSeen || resuming.gotResume.LastSavedReceipt != "save-1" {
		t.Fatalf("resume cursor = %+v, want save-1", resuming.gotResume)
	}
	replayed, _ := supervisor.Result("run-crash")
	if replayed.Outcome != OutcomeFailed || !strings.Contains(replayed.Detail, "duplicate") {
		t.Fatalf("result = %+v, want failed on blind replay", replayed)
	}
	if _, err := supervisor.Resume(context.Background(), "run-absent",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err == nil {
		t.Error("resume without a cursor admitted")
	}
}

func TestResumeContinuesAfterCursor(t *testing.T) {
	cursors := &memoryCursors{}
	first := &scriptTransport{
		events:    []Event{{Kind: EventSaved, SaveRef: "save-1"}},
		returnErr: ErrDisconnected,
	}
	supervisor := NewSupervisor(first, cursors, acceptAll, readyFacts().EffectiveModel)
	if _, err := supervisor.StartRun(context.Background(), "run-continue",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	supervisor.transport = &scriptTransport{events: []Event{
		{Kind: EventSaved, SaveRef: "save-2"},
		{Kind: EventFinished},
	}}
	if _, err := supervisor.Resume(context.Background(), "run-continue",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	result, _ := supervisor.Result("run-continue")
	if result.Outcome != OutcomeCompleted {
		t.Fatalf("result = %+v, want completed", result)
	}
	if len(result.SavedRefs) != 2 || result.SavedRefs[0] != "save-1" || result.SavedRefs[1] != "save-2" {
		t.Fatalf("saved refs = %v, want [save-1 save-2]", result.SavedRefs)
	}
}

func TestCompletedRunsNeverReplay(t *testing.T) {
	supervisor := NewSupervisor(&scriptTransport{events: []Event{{Kind: EventFinished}}},
		&memoryCursors{}, acceptAll, readyFacts().EffectiveModel)
	if _, err := supervisor.StartRun(context.Background(), "run-done",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Resume(context.Background(), "run-done",
		testSpec(t, TierContributor, DefaultBounds()), PublicInput{}, readyFacts()); err == nil {
		t.Error("completed run admitted for resume")
	}
}
