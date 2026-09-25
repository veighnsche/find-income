package musewire

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/musecode"
)

type runnerMemoryCursors struct {
	mu      sync.Mutex
	cursors map[string]musecode.Cursor
}

func (m *runnerMemoryCursors) SaveCursor(_ context.Context, cursor musecode.Cursor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cursors == nil {
		m.cursors = map[string]musecode.Cursor{}
	}
	m.cursors[cursor.RunRef] = cursor
	return nil
}

func (m *runnerMemoryCursors) LoadCursor(_ context.Context, runRef string) (musecode.Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cursor, ok := m.cursors[runRef]
	if !ok {
		return musecode.Cursor{}, errors.New("no cursor")
	}
	return cursor, nil
}

func standardRunnerFacts() musecode.Facts {
	return musecode.Facts{CLIPath: "/bin/muse", CLIReportVersion: "1.4.0",
		EffectiveModel: "meta/muse-spark-1.3-standard", SubscriptionLaneProved: true,
		SessionProtocolProved: true, WorkspaceIsolatedProved: true}
}

func standardRunnerSpec() musecode.SessionSpec {
	return musecode.SessionSpec{Tier: musecode.TierStandard, Workspace: "/tmp/e13-runner-test",
		Public: false, Bounds: musecode.Bounds{MaxWallClock: time.Minute, MaxModelSteps: 5,
			MaxToolCalls: 5, MaxBytesPerOp: 1 << 20, MaxBytesTotal: 4 << 20}}
}

func standardRunnerInput() musecode.StandardInput {
	return musecode.StandardInput{Purpose: standardDraftPurpose, BundleRef: "check-1",
		Context: map[string]string{"prompt": "facts"}, Targets: []string{"q1"}}
}

func TestNewSupervisorStandardRunnerGuards(t *testing.T) {
	transport := scriptTransport{}
	cursors := &runnerMemoryCursors{}
	validate := func(string) error { return nil }
	spec := standardRunnerSpec()
	facts := standardRunnerFacts()
	if _, err := NewSupervisorStandardRunner(transport, cursors, validate, spec, facts, "e13-run"); err != nil {
		t.Fatalf("valid runner: %v", err)
	}
	public := spec
	public.Tier = musecode.TierContributor
	public.Public = true
	if _, err := NewSupervisorStandardRunner(transport, cursors, validate, public, facts, "e13-run"); err == nil {
		t.Fatal("contributor spec admitted")
	}
	unproved := facts
	unproved.SubscriptionLaneProved = false
	if _, err := NewSupervisorStandardRunner(transport, cursors, validate, spec, unproved, "e13-run"); err == nil {
		t.Fatal("unproved lane admitted")
	}
	if _, err := NewSupervisorStandardRunner(nil, cursors, validate, spec, facts, "e13-run"); err == nil {
		t.Fatal("nil transport admitted")
	}
	if _, err := NewSupervisorStandardRunner(transport, cursors, validate, spec, facts, "../escape"); err == nil {
		t.Fatal("bad run ref admitted")
	}
}

func TestSupervisorStandardRunnerCollectsTexts(t *testing.T) {
	transport := scriptTransport{events: []musecode.Event{
		{Kind: musecode.EventModelStep},
		{Kind: musecode.EventModelText, Text: `{"drafts":[]}`, BytesOut: 13},
		{Kind: musecode.EventFinished},
	}}
	runner, err := NewSupervisorStandardRunner(transport, &runnerMemoryCursors{},
		func(string) error { return nil }, standardRunnerSpec(), standardRunnerFacts(), "e13-texts")
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunStandard(context.Background(), standardRunnerInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Messages[0] != `{"drafts":[]}` {
		t.Fatalf("messages = %q", result.Messages)
	}
	if _, err := runner.RunStandard(context.Background(), standardRunnerInput()); err == nil {
		t.Fatal("second call admitted on a single-use runner")
	}
}

func TestSupervisorStandardRunnerFailsClosed(t *testing.T) {
	ctx := context.Background()
	// Unproved lane never reaches the transport.
	var called bool
	transport := scriptTransport{events: []musecode.Event{{Kind: musecode.EventFinished}}}
	_ = transport
	recording := roundTripFunc(func(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error {
		called = true
		return nil
	})
	facts := standardRunnerFacts()
	facts.SubscriptionLaneProved = false
	// Constructor already refuses the unproved lane; RunStandard re-checks
	// facts it was built with, so mutate after construction to prove the
	// re-check path.
	proved := standardRunnerFacts()
	runner, err := NewSupervisorStandardRunner(recording, &runnerMemoryCursors{},
		func(string) error { return nil }, standardRunnerSpec(), proved, "e13-unproved")
	if err != nil {
		t.Fatal(err)
	}
	runner.Facts = facts
	if _, err := runner.RunStandard(ctx, standardRunnerInput()); !errors.Is(err, musecode.ErrSessionUnavailable) {
		t.Fatalf("unproved lane: %v", err)
	}
	if called {
		t.Fatal("transport ran without a proved lane")
	}

	// A failed turn returns no messages.
	failed := scriptTransport{err: errors.New("boom")}
	runner, err = NewSupervisorStandardRunner(failed, &runnerMemoryCursors{},
		func(string) error { return nil }, standardRunnerSpec(), standardRunnerFacts(), "e13-failed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunStandard(ctx, standardRunnerInput()); err == nil {
		t.Fatal("failed turn returned messages")
	}
}

type roundTripFunc func(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error

func (f roundTripFunc) Run(ctx context.Context, spec musecode.SessionSpec, input musecode.SessionInput, resume musecode.Cursor, sink musecode.EventSink) error {
	return f(ctx, spec, input, resume, sink)
}
