package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func testQueue(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func enqueue(t *testing.T, s *store.Store, key string) store.Job {
	t.Helper()
	job, created, err := s.EnqueueJob(context.Background(), store.JobRequest{
		Kind: "fixture", Payload: json.RawMessage(`{"fixture":true}`),
		Actor:          store.Actor{Kind: "system", ID: "test"},
		IdempotencyKey: key, MaxAttempts: 3,
	})
	if err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}
	return job
}

func TestSlowHandlerHoldsNoDatabaseTransaction(t *testing.T) {
	ctx := context.Background()
	s := testQueue(t)
	job := enqueue(t, s, "slow")
	started := make(chan struct{})
	release := make(chan struct{})
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: 3 * time.Second,
		Handlers: map[string]Handler{"fixture": func(ctx context.Context, _ store.Job) (store.JobResult, error) {
			close(started)
			select {
			case <-release:
				return store.JobResult{Ref: "prepared:one"}, nil
			case <-ctx.Done():
				return store.JobResult{}, ctx.Err()
			}
		}},
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.ProcessOne(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler never started")
	}
	writeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, _, err := s.EnqueueJob(writeCtx, store.JobRequest{
		Kind: "fixture", Payload: json.RawMessage(`{"fixture":true}`),
		Actor:          store.Actor{Kind: "system", ID: "test"},
		IdempotencyKey: "while-running", MaxAttempts: 1,
	}); err != nil {
		t.Fatalf("database write blocked by handler: %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.State != store.JobSucceeded || current.Result.Ref != "prepared:one" {
		t.Fatalf("result: %+v err=%v", current, err)
	}
}

func TestCancellationStopsHandlerAndRejectsLateResult(t *testing.T) {
	ctx := context.Background()
	s := testQueue(t)
	job := enqueue(t, s, "cancel")
	started := make(chan struct{})
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: 150 * time.Millisecond,
		Handlers: map[string]Handler{"fixture": func(ctx context.Context, _ store.Job) (store.JobResult, error) {
			close(started)
			<-ctx.Done()
			return store.JobResult{Ref: "late-result"}, nil
		}},
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.ProcessOne(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler never started")
	}
	if changed, err := s.CancelJob(ctx, job.ID, store.Actor{Kind: "administrator", ID: "owner"}, time.Now()); err != nil || !changed {
		t.Fatalf("cancel changed=%v err=%v", changed, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled handler did not stop")
	}
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.State != store.JobCancelled || current.Result.Ref != "" {
		t.Fatalf("late result published: %+v err=%v", current, err)
	}
}

func TestWorkerClassifiesPoisonAndTransientFailure(t *testing.T) {
	ctx := context.Background()
	s := testQueue(t)
	poison := enqueue(t, s, "poison")
	now := time.Now().Add(time.Second)
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: time.Second,
		Now: func() time.Time { return now },
		Handlers: map[string]Handler{"fixture": func(_ context.Context, _ store.Job) (store.JobResult, error) {
			return store.JobResult{}, Permanent("invalid_input", errors.New("synthetic invalid payload"))
		}},
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("poison process=%v err=%v", processed, err)
	}
	current, err := s.Job(ctx, poison.ID)
	if err != nil || current.State != store.JobFailed || current.AttemptCount != 1 || current.LastErrorCode != "invalid_input" {
		t.Fatalf("poison state: %+v err=%v", current, err)
	}
	transient := enqueue(t, s, "transient")
	calls := 0
	w.Handlers["fixture"] = func(_ context.Context, _ store.Job) (store.JobResult, error) {
		calls++
		if calls == 1 {
			return store.JobResult{}, Transient("temporary", errors.New("try later"))
		}
		return store.JobResult{Ref: "prepared:success"}, nil
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("transient process=%v err=%v", processed, err)
	}
	current, err = s.Job(ctx, transient.ID)
	if err != nil || current.State != store.JobQueued || current.AttemptCount != 1 {
		t.Fatalf("retry queue state: %+v err=%v", current, err)
	}
	now = current.AvailableAt.Add(time.Millisecond)
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("retry process=%v err=%v", processed, err)
	}
	current, err = s.Job(ctx, transient.ID)
	if err != nil || current.State != store.JobSucceeded || current.AttemptCount != 2 || current.Result.Ref != "prepared:success" {
		t.Fatalf("retry result: %+v err=%v", current, err)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := testQueue(t)
	job := enqueue(t, s, "run")
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: time.Second,
		Handlers: map[string]Handler{"fixture": func(_ context.Context, _ store.Job) (store.JobResult, error) {
			return store.JobResult{Ref: "finished"}, nil
		}},
	}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.After(time.Second)
	for {
		current, err := s.Job(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State == store.JobSucceeded {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not complete job")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestHeartbeatKeepsSlowHandlerLease(t *testing.T) {
	ctx := context.Background()
	s := testQueue(t)
	job := enqueue(t, s, "heartbeat")
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: 90 * time.Millisecond,
		Handlers: map[string]Handler{"fixture": func(ctx context.Context, _ store.Job) (store.JobResult, error) {
			select {
			case <-time.After(250 * time.Millisecond):
				return store.JobResult{Ref: "slow-prepared"}, nil
			case <-ctx.Done():
				return store.JobResult{}, ctx.Err()
			}
		}},
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("slow process=%v err=%v", processed, err)
	}
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.State != store.JobSucceeded || current.Result.Ref != "slow-prepared" {
		t.Fatalf("heartbeat result: %+v err=%v", current, err)
	}
}

func TestInvalidResultFailsPermanently(t *testing.T) {
	ctx := context.Background()
	s := testQueue(t)
	job := enqueue(t, s, "invalid-result")
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: time.Second,
		Handlers: map[string]Handler{"fixture": func(_ context.Context, _ store.Job) (store.JobResult, error) {
			return store.JobResult{Metadata: json.RawMessage(`{"broken":`)}, nil
		}},
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("invalid result process=%v err=%v", processed, err)
	}
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.State != store.JobFailed || current.AttemptCount != 1 || current.LastErrorCode != "invalid_result" {
		t.Fatalf("invalid result state: %+v err=%v", current, err)
	}
}

func TestProviderCauseIsNotPersistedAndRetryAfterIsHonored(t *testing.T) {
	ctx := context.Background()
	s := testQueue(t)
	job := enqueue(t, s, "provider-secret")
	const sentinel = "SENTINEL_SECRET_SHOULD_NOT_PERSIST"
	cause := errors.New("raw provider response with " + sentinel)
	handlerErr := TransientAfter("provider_throttled", "Provider requested a later retry", cause, 120*time.Second)
	if strings.Contains(handlerErr.Error(), sentinel) || !errors.Is(handlerErr, cause) {
		t.Fatalf("handler error did not separate safe text from cause: %v", handlerErr)
	}
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: time.Second,
		Handlers: map[string]Handler{"fixture": func(_ context.Context, _ store.Job) (store.JobResult, error) {
			return store.JobResult{}, handlerErr
		}},
	}
	started := time.Now()
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("provider failure processed=%v err=%v", processed, err)
	}
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.State != store.JobQueued || current.AvailableAt.Before(started.Add(120*time.Second)) {
		t.Fatalf("provider wait: %+v err=%v", current, err)
	}
	attempts, err := s.JobAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 1 || attempts[0].RetryNotBefore.Before(started.Add(120*time.Second)) {
		t.Fatalf("provider wait attempt: %+v err=%v", attempts, err)
	}
	if strings.Contains(current.LastError, sentinel) || strings.Contains(attempts[0].ErrorMessage, sentinel) ||
		current.LastError != "Provider requested a later retry" {
		t.Fatalf("raw provider cause persisted: current=%q attempt=%q", current.LastError, attempts[0].ErrorMessage)
	}
}

func TestInvalidProviderRetryWindowFailsPermanently(t *testing.T) {
	ctx := context.Background()
	s := testQueue(t)
	job := enqueue(t, s, "invalid-provider-wait")
	w := &Worker{
		Queue: s, ID: "worker", PollInterval: 10 * time.Millisecond, LeaseDuration: time.Second,
		Handlers: map[string]Handler{"fixture": func(_ context.Context, _ store.Job) (store.JobResult, error) {
			return store.JobResult{}, TransientAfter("provider_throttled", "wait", errors.New("raw"), 31*24*time.Hour)
		}},
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("invalid wait process=%v err=%v", processed, err)
	}
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.State != store.JobFailed || current.LastErrorCode != "invalid_retry_schedule" || current.AttemptCount != 1 {
		t.Fatalf("invalid wait state: %+v err=%v", current, err)
	}
}
