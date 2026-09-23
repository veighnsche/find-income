package rounds

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type fakeReady struct{ blocked bool }

func (f *fakeReady) CheckRound(context.Context, string) error {
	if f.blocked {
		return ErrNotReady
	}
	return nil
}

type fakeCanceller struct {
	db      *store.Store
	roundID string
	called  []string
}

func (f *fakeCanceller) CancelDispatch(ctx context.Context, attemptID string) error {
	r, err := f.db.Round(ctx, f.roundID)
	if err != nil || r.State != store.RoundStopping {
		return errors.New("cancellation ran before the durable fence")
	}
	f.called = append(f.called, attemptID)
	return nil
}

type fakeReconciler struct {
	observations []Observation
	called       []string
}

func (f *fakeReconciler) ObserveDispatch(_ context.Context, attemptID string) (Observation, error) {
	f.called = append(f.called, attemptID)
	value := f.observations[0]
	f.observations = f.observations[1:]
	return value, nil
}

func TestServiceFakeWorkerStopThenResumeRetainsAllowance(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: "administrator", ID: "owner"}
	input := store.StartRoundInput{RequestKey: "owner-start", Intent: "Find sourced roles", Outcome: "discover",
		ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope:  store.RoundScope{Operations: []string{"source.fetch"}, Resources: []string{"source:a"}},
		Limits: store.RoundAllowance{Requests: 3, Items: 1, Tools: 3, Turns: 1}}
	ready := &fakeReady{}
	reconciler := &fakeReconciler{observations: []Observation{
		{State: store.AttemptUncertain, Evidence: json.RawMessage(`{"checked":"not_confirmed"}`)},
		{State: store.AttemptObservedFailure, Evidence: json.RawMessage(`{"checked":"failed"}`)},
	}}
	svc := &Service{Store: db, Readiness: ready, Reconciler: reconciler}
	r, created, err := svc.Start(ctx, actor, input)
	if err != nil || !created || r.State != store.RoundRunning {
		t.Fatalf("start: %+v %v %v", r, created, err)
	}
	ready.blocked = true
	duplicate, created, err := svc.Start(ctx, actor, input)
	if err != nil || created || duplicate.ID != r.ID {
		t.Fatalf("idempotent start after readiness loss: %+v %v %v", duplicate, created, err)
	}
	ready.blocked = false
	attempt, created, err := svc.Reserve(ctx, actor, r.ID, store.RoundAttemptInput{RequestKey: "fetch-1",
		Operation: "source.fetch", ResourceID: "source:a", Cost: store.RoundAllowance{Requests: 1, Items: 1, Tools: 1, Turns: 1}})
	if err != nil || !created {
		t.Fatalf("reserve: %+v %v %v", attempt, created, err)
	}
	if _, err := svc.Dispatch(ctx, r.ID, attempt.ID); err != nil {
		t.Fatal(err)
	}
	canceller := &fakeCanceller{db: db, roundID: r.ID}
	svc.Canceller = canceller
	paused, err := svc.Stop(ctx, actor, r.ID)
	if err != nil || paused.State != store.RoundPaused || len(canceller.called) != 1 || canceller.called[0] != attempt.ID {
		t.Fatalf("stop: %+v called=%v err=%v", paused, canceller.called, err)
	}
	if _, err := svc.Complete(ctx, actor, r.ID, attempt.ID, json.RawMessage(`{"unsafe":true}`)); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stopped attempt wrote result: %v", err)
	}
	if _, err := svc.Resume(ctx, store.Actor{Kind: "agent", ID: "untrusted"}, r.ID); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("non-owner reconciliation: %v", err)
	}
	if _, err := svc.Resume(ctx, actor, r.ID); !errors.Is(err, store.ErrUncertain) {
		t.Fatalf("unknown observation resumed: %v", err)
	}
	resumed, err := svc.Resume(ctx, actor, r.ID)
	if err != nil || resumed.State != store.RoundRunning || resumed.Used.Requests != 3 || resumed.Used.Tools != 3 ||
		len(reconciler.called) != 2 {
		t.Fatalf("reconciled resume: %+v called=%v err=%v", resumed, reconciler.called, err)
	}
	if _, _, err := svc.Reserve(ctx, actor, r.ID, store.RoundAttemptInput{RequestKey: "retry",
		Operation: "source.fetch", ResourceID: "source:a", Cost: store.RoundAllowance{Requests: 1, Tools: 1}}); !errors.Is(err, store.ErrAllowance) {
		t.Fatalf("retry escaped allowance: %v", err)
	}
}
