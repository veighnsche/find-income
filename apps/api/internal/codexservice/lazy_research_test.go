package codexservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// lazyProbeTurns proves a supervisor turn runner is already bound: binding
// it must fail single-assignment once Lazy has wired the service runner.
type lazyProbeTurns struct{}

func (*lazyProbeTurns) RunTurn(context.Context, store.Actor, rounds.RunnerTurnInput) (rounds.RunnerTurnOutput, error) {
	return rounds.RunnerTurnOutput{}, nil
}

type lazyProbeConversation struct{}

func (*lazyProbeConversation) SteerAttempt(context.Context, string, string, string) (string, error) {
	return "", nil
}

type lazyProbeObserver struct{ marker rounds.Observation }

func (f *lazyProbeObserver) ObserveDispatch(context.Context, string) (rounds.Observation, error) {
	return f.marker, nil
}

func newLazyTestSupervisor(t *testing.T, deps rounds.SupervisorDeps) *rounds.Supervisor {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	journal, err := store.NewRunEventJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	deps.DB, deps.Authority, deps.Journal = db, &fakeResearchAuthority{}, journal
	sup, err := rounds.NewSupervisor(deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)
	return sup
}

// Before construction the wiring stages pending (last call wins); once
// closed it is ignored like the config setters.
func TestLazySetResearchWiringPendingAndClosed(t *testing.T) {
	l := &Lazy{}
	first, second := &ResearchToolchain{}, &ResearchToolchain{}
	l.SetResearchWiring(first, nil)
	l.SetResearchWiring(second, nil)
	if l.researchTC != second {
		t.Fatal("pending research wiring did not keep the last call")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l.SetResearchWiring(first, nil)
	if l.researchTC != second {
		t.Fatal("SetResearchWiring applied after Close, want ignored")
	}
}

// After construction the wiring applies immediately: the toolchain lands
// on the service and the supervisor's three service-bound deps bind.
func TestLazySetResearchWiringAppliesImmediately(t *testing.T) {
	l := &Lazy{service: &Service{}}
	sup := newLazyTestSupervisor(t, rounds.SupervisorDeps{})
	tc := &ResearchToolchain{}
	l.SetResearchWiring(tc, sup)
	if l.service.researchTools() != tc {
		t.Fatal("toolchain not wired onto the constructed service")
	}
	if err := sup.SetTurnRunner(&lazyProbeTurns{}); err == nil {
		t.Fatal("supervisor turn runner unbound after wiring")
	}
	if err := sup.SetConversation(&lazyProbeConversation{}); err == nil {
		t.Fatal("supervisor conversation unbound after wiring")
	}
	if err := sup.SetObserver(&lazyProbeObserver{}); err == nil {
		t.Fatal("supervisor observer unbound after wiring")
	}
}

// A nil toolchain skips the tool wiring and a nil supervisor skips the
// turn-dep binding, so either side stages independently.
func TestLazySetResearchWiringNilSides(t *testing.T) {
	l := &Lazy{service: &Service{}}
	sup := newLazyTestSupervisor(t, rounds.SupervisorDeps{})
	l.SetResearchWiring(nil, sup)
	if l.service.researchTools() != nil {
		t.Fatal("nil toolchain wired research tools, want skipped")
	}
	if err := sup.SetTurnRunner(&lazyProbeTurns{}); err == nil {
		t.Fatal("supervisor turn runner unbound after nil-toolchain wiring")
	}

	l2 := &Lazy{service: &Service{}}
	tc := &ResearchToolchain{}
	l2.SetResearchWiring(tc, nil)
	if l2.service.researchTools() != tc {
		t.Fatal("toolchain not wired when the supervisor is nil")
	}
}

// Construction-time supervisor deps win: late binding drops conflicts and
// the original observer still serves observations.
func TestLazyResearchWiringConstructionDepsWin(t *testing.T) {
	l := &Lazy{service: &Service{}}
	orig := &lazyProbeObserver{marker: rounds.Observation{
		State: store.AttemptUncertain, Evidence: json.RawMessage(`{"code":"orig"}`),
	}}
	sup := newLazyTestSupervisor(t, rounds.SupervisorDeps{Observer: orig})
	l.SetResearchWiring(&ResearchToolchain{}, sup)
	got, err := sup.ObserveDispatch(context.Background(), "attempt")
	if err != nil {
		t.Fatalf("ObserveDispatch: %v", err)
	}
	if string(got.Evidence) != `{"code":"orig"}` {
		t.Fatalf("ObserveDispatch evidence = %s, want the construction-time observer", got.Evidence)
	}
}
