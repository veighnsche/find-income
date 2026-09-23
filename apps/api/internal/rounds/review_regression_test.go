package rounds

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func reviewRound(t *testing.T) (*Service, store.Actor, store.Round, store.RoundAttempt) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p, err := db.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actor := store.Actor{Kind: "administrator", ID: "owner"}
	svc := &Service{Store: db, Readiness: &fakeReady{}, Worker: &fakeWorker{}}
	r, _, err := svc.Start(ctx, actor, store.StartRoundInput{RequestKey: "review", Intent: "Find roles", Outcome: "discover", ProfileVersion: p.Version, Deadline: time.Now().Add(time.Hour), Scope: store.RoundScope{Operations: []string{store.RoundFetchSource}, Resources: []string{"source"}}, Limits: store.RoundAllowance{Requests: 10, Tools: 10}})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := svc.Reserve(ctx, actor, r.ID, store.RoundAttemptInput{RequestKey: "fetch", Operation: store.RoundFetchSource, ResourceID: "source", Cost: store.RoundAllowance{Requests: 1, Tools: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return svc, actor, r, a
}

func TestReviewOldReservationCannotDispatchAfterStopResume(t *testing.T) {
	svc, actor, r, a := reviewRound(t)
	ctx := context.Background()
	if _, err := svc.Stop(ctx, actor, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(ctx, actor, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Dispatch(ctx, r.ID, a.ID); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stale pre-Stop reservation acquired resumed authority: %v", err)
	}
}

type reviewBlockingObserver struct{ entered, release chan struct{} }

func (b *reviewBlockingObserver) ObserveDispatch(ctx context.Context, _ string) (Observation, error) {
	close(b.entered)
	select {
	case <-b.release:
		return Observation{State: store.AttemptObservedFailure, Evidence: json.RawMessage(`{"done":true}`)}, nil
	case <-ctx.Done():
		return Observation{}, ctx.Err()
	}
}

func TestReviewStopDuringReconciliationPreventsResume(t *testing.T) {
	svc, actor, r, a := reviewRound(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := svc.Dispatch(ctx, r.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stop(ctx, actor, r.ID); err != nil {
		t.Fatal(err)
	}
	observer := &reviewBlockingObserver{entered: make(chan struct{}), release: make(chan struct{})}
	svc.Reconciler = observer
	done := make(chan error, 1)
	go func() { _, err := svc.Resume(ctx, actor, r.ID); done <- err }()
	select {
	case <-observer.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := svc.Stop(ctx, actor, r.ID); err != nil {
		close(observer.release)
		t.Fatal(err)
	}
	close(observer.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	current, err := svc.Store.Round(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State == store.RoundRunning {
		t.Fatal("Resume continued after a newer owner Stop")
	}
}
