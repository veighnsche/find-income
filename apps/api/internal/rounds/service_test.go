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

type fakeWorker struct {
	launched  []string
	cancelled []string
	err       error
}

func (f *fakeWorker) LaunchRound(_ context.Context, r store.Round) error {
	if f.err != nil {
		return f.err
	}
	f.launched = append(f.launched, r.ID)
	return nil
}
func (f *fakeWorker) CancelRound(id string) { f.cancelled = append(f.cancelled, id) }

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
	input := store.StartRoundInput{RequestKey: "owner-start", Intent: "Find sourced roles", Outcome: "process_input",
		ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour),
		Scope:  store.RoundScope{Operations: []string{"source.fetch"}, Resources: []string{"source:a"}},
		Limits: store.RoundAllowance{Requests: 3, Items: 1, Tools: 3, Turns: 1}}
	ready := &fakeReady{}
	reconciler := &fakeReconciler{observations: []Observation{
		{State: store.AttemptUncertain, Evidence: json.RawMessage(`{"checked":"not_confirmed"}`)},
		{State: store.AttemptObservedFailure, Evidence: json.RawMessage(`{"checked":"failed"}`)},
	}}
	worker := &fakeWorker{}
	svc := &Service{Store: db, Readiness: ready, Reconciler: reconciler, Worker: worker}
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
	if len(worker.cancelled) != 1 || worker.cancelled[0] != r.ID {
		t.Fatalf("worker cancellation after stop: %v", worker.cancelled)
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
		len(reconciler.called) != 2 || len(worker.launched) != 2 {
		t.Fatalf("reconciled resume: %+v called=%v err=%v", resumed, reconciler.called, err)
	}
	if _, _, err := svc.Reserve(ctx, actor, r.ID, store.RoundAttemptInput{RequestKey: "retry",
		Operation: "source.fetch", ResourceID: "source:a", Cost: store.RoundAllowance{Requests: 1, Tools: 1}}); !errors.Is(err, store.ErrAllowance) {
		t.Fatalf("retry escaped allowance: %v", err)
	}
}

func TestExpiredPausedRoundIsTerminalBeforeResumeOrNewStart(t *testing.T) {
	for _, action := range []string{"resume", "start"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			profile, err := db.CurrentPreferences(ctx)
			if err != nil {
				t.Fatal(err)
			}
			owner := store.Actor{Kind: "administrator", ID: "owner"}
			worker := &fakeWorker{}
			svc := &Service{Store: db, Readiness: &fakeReady{}, Worker: worker}
			input := store.StartRoundInput{RequestKey: "expiring", Intent: "Inspect a source", Outcome: "process_input", ProfileVersion: profile.Version,
				Scope:  store.RoundScope{Resources: []string{"source:a"}, Operations: []string{store.RoundFetchSource}},
				Limits: store.RoundAllowance{Requests: 2, Tools: 2}, Deadline: time.Now().Add(100 * time.Millisecond)}
			round, _, err := svc.Start(ctx, owner, input)
			if err != nil {
				t.Fatal(err)
			}
			attempt, _, err := svc.Reserve(ctx, owner, round.ID, store.RoundAttemptInput{RequestKey: "fetch", Operation: store.RoundFetchSource, ResourceID: "source:a"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Dispatch(ctx, round.ID, attempt.ID); err != nil {
				t.Fatal(err)
			}
			paused, err := svc.Stop(ctx, owner, round.ID)
			if err != nil || paused.State != store.RoundPaused {
				t.Fatalf("stop: %+v %v", paused, err)
			}
			time.Sleep(time.Until(input.Deadline) + 10*time.Millisecond)
			if action == "resume" {
				if _, err := svc.Resume(ctx, owner, round.ID); !errors.Is(err, store.ErrExpired) {
					t.Fatalf("resume after deadline: %v", err)
				}
			}
			input.RequestKey = "after-expiry"
			input.Deadline = time.Now().Add(time.Hour)
			next, created, err := svc.Start(ctx, owner, input)
			if err != nil || !created || next.ID == round.ID {
				t.Fatalf("new commission: %+v %v %v", next, created, err)
			}
			terminal, err := db.Round(ctx, round.ID)
			if err != nil || terminal.State != store.RoundFailed || terminal.StopReason != "deadline_reached" || !terminal.ReconciliationRequired || terminal.Used != paused.Used {
				t.Fatalf("expired evidence: %+v %v", terminal, err)
			}
			uncertain, err := db.RoundAttempt(ctx, attempt.ID)
			if err != nil || uncertain.State != store.AttemptUncertain || uncertain.ID != attempt.ID {
				t.Fatalf("uncertain dispatch retained: %+v %v", uncertain, err)
			}
			if len(worker.launched) != 2 {
				t.Fatalf("unexpected dispatches: %v", worker.launched)
			}
		})
	}
}
