package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func openJobTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testJobRequest(key string) JobRequest {
	return JobRequest{
		Kind: "synthetic", Payload: json.RawMessage(`{"value":1}`),
		Actor:          Actor{Kind: "administrator", ID: "owner"},
		IdempotencyKey: key, MaxAttempts: 3,
	}
}

func claimTestJob(t *testing.T, s *Store, at time.Time) Job {
	t.Helper()
	job, found, err := s.ClaimNextJob(context.Background(), "worker-a", []string{"synthetic"}, time.Second, at)
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	return job
}

func TestJobEnqueueIdempotencyAndProvenance(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	request := testJobRequest("request-1")
	first, created, err := s.EnqueueJob(ctx, request)
	if err != nil || !created || first.State != JobQueued || first.PayloadSHA256 == "" {
		t.Fatalf("first enqueue: %+v created=%v err=%v", first, created, err)
	}
	request.Payload = json.RawMessage("{ \"value\" : 1 }")
	second, created, err := s.EnqueueJob(ctx, request)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("identical retry: %+v created=%v err=%v", second, created, err)
	}
	request.Payload = json.RawMessage(`{"value":2}`)
	if _, _, err := s.EnqueueJob(ctx, request); !errors.Is(err, ErrJobIdempotencyConflict) {
		t.Fatalf("altered payload: %v", err)
	}
	request = testJobRequest("request-1")
	request.Kind = "different"
	if _, _, err := s.EnqueueJob(ctx, request); !errors.Is(err, ErrJobIdempotencyConflict) {
		t.Fatalf("altered operation: %v", err)
	}
	request = testJobRequest("request-1")
	request.Actor.ID = "different-agent"
	third, created, err := s.EnqueueJob(ctx, request)
	if err != nil || !created || third.ID == first.ID {
		t.Fatalf("other actor: %+v created=%v err=%v", third, created, err)
	}
	var audits int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes WHERE entity_kind='job' AND operation='job.enqueue'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("enqueue audit count=%d err=%v", audits, err)
	}
	request.Payload = json.RawMessage(`{"bad":`)
	request.IdempotencyKey = "poison"
	if _, _, err := s.EnqueueJob(ctx, request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed JSON accepted: %v", err)
	}
}

func TestTwoClaimersOnlyOneOwnsJob(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstStore, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	if _, _, err := firstStore.EnqueueJob(ctx, testJobRequest("claim-once")); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	type outcome struct {
		job   Job
		found bool
		err   error
	}
	results := make(chan outcome, 2)
	for index, s := range []*Store{firstStore, secondStore} {
		wg.Add(1)
		go func(index int, s *Store) {
			defer wg.Done()
			<-start
			job, found, err := s.ClaimNextJob(ctx, "worker-"+string(rune('a'+index)), []string{"synthetic"}, time.Second, time.Now().Add(time.Second))
			results <- outcome{job, found, err}
		}(index, s)
	}
	close(start)
	wg.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.found {
			claimed++
			if result.job.AttemptCount != 1 || result.job.LeaseToken == "" {
				t.Fatalf("bad claim: %+v", result.job)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("claims=%d, want one", claimed)
	}
}

func TestJobLeaseExpiryRestartAndFencedResult(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	request := testJobRequest("restart")
	request.MaxAttempts = 2
	created, _, err := s.EnqueueJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(time.Second)
	oldClaim, found, err := s.ClaimNextJob(ctx, "old", []string{"synthetic"}, 100*time.Millisecond, base)
	if err != nil || !found {
		t.Fatalf("initial claim: %v %v", found, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	newClaim, found, err := s.ClaimNextJob(ctx, "new", []string{"synthetic"}, time.Second, base.Add(200*time.Millisecond))
	if err != nil || !found || newClaim.ID != created.ID || newClaim.AttemptCount != 2 || newClaim.LeaseToken == oldClaim.LeaseToken {
		t.Fatalf("recovery claim: %+v found=%v err=%v", newClaim, found, err)
	}
	if applied, err := s.CompleteJob(ctx, oldClaim, JobResult{Ref: "stale"}, base.Add(250*time.Millisecond)); err != nil || applied {
		t.Fatalf("stale completion applied=%v err=%v", applied, err)
	}
	result := JobResult{Ref: "digest:new", Metadata: json.RawMessage(`{"version":2}`)}
	if applied, err := s.CompleteJob(ctx, newClaim, result, base.Add(250*time.Millisecond)); err != nil || !applied {
		t.Fatalf("new completion applied=%v err=%v", applied, err)
	}
	if applied, err := s.CompleteJob(ctx, newClaim, JobResult{Ref: "duplicate"}, base.Add(260*time.Millisecond)); err != nil || applied {
		t.Fatalf("duplicate completion applied=%v err=%v", applied, err)
	}
	current, err := s.Job(ctx, created.ID)
	if err != nil || current.State != JobSucceeded || current.Result.Ref != "digest:new" {
		t.Fatalf("published result: %+v err=%v", current, err)
	}
	attempts, err := s.JobAttempts(ctx, created.ID)
	if err != nil || len(attempts) != 2 || attempts[0].Outcome != "expired" || attempts[1].Outcome != "succeeded" || attempts[1].Result.Ref != result.Ref {
		t.Fatalf("attempt history: %+v err=%v", attempts, err)
	}
}

func TestJobRetryPermanentFailureAndAttemptLimit(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	job, _, err := s.EnqueueJob(ctx, testJobRequest("retries"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(time.Second)
	for attempt := 1; attempt <= 3; attempt++ {
		claim, found, err := s.ClaimNextJob(ctx, "retry-worker", []string{"synthetic"}, time.Second, base)
		if err != nil || !found || claim.AttemptCount != attempt {
			t.Fatalf("claim %d: %+v found=%v err=%v", attempt, claim, found, err)
		}
		applied, err := s.FailJob(ctx, claim, JobFailure{Code: "temporary", Message: "synthetic", Retryable: true}, base.Add(100*time.Millisecond))
		if err != nil || !applied {
			t.Fatalf("failure %d applied=%v err=%v", attempt, applied, err)
		}
		current, err := s.Job(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			if current.State != JobQueued || !current.AvailableAt.After(base) {
				t.Fatalf("retry state %d: %+v", attempt, current)
			}
			if _, found, err := s.ClaimNextJob(ctx, "early", []string{"synthetic"}, time.Second, base.Add(101*time.Millisecond)); err != nil || found {
				t.Fatalf("early retry claimed=%v err=%v", found, err)
			}
			base = current.AvailableAt
		} else if current.State != JobFailed || current.CompletedAt.IsZero() {
			t.Fatalf("exhausted state: %+v", current)
		}
	}
	attempts, err := s.JobAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 3 || attempts[0].Outcome != "retry" || attempts[2].Outcome != "failed" {
		t.Fatalf("retry attempts: %+v err=%v", attempts, err)
	}

	poison, _, err := s.EnqueueJob(ctx, testJobRequest("permanent"))
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTestJob(t, s, time.Now().Add(10*time.Second))
	if claim.ID != poison.ID {
		t.Fatalf("claimed %s, want poison %s", claim.ID, poison.ID)
	}
	if applied, err := s.FailJob(ctx, claim, JobFailure{Code: "invalid_input", Message: "bad fields"}, time.Now().Add(10*time.Second)); err != nil || !applied {
		t.Fatalf("permanent failure applied=%v err=%v", applied, err)
	}
	current, err := s.Job(ctx, poison.ID)
	if err != nil || current.State != JobFailed || current.AttemptCount != 1 {
		t.Fatalf("poison state: %+v err=%v", current, err)
	}
}

func TestCancelQueuedAndRunningFencesCompletion(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	actor := Actor{Kind: "administrator", ID: "owner"}
	queued, _, err := s.EnqueueJob(ctx, testJobRequest("cancel-queued"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	if changed, err := s.CancelJob(ctx, queued.ID, actor, now); err != nil || !changed {
		t.Fatalf("queued cancellation changed=%v err=%v", changed, err)
	}
	if changed, err := s.CancelJob(ctx, queued.ID, actor, now); err != nil || changed {
		t.Fatalf("repeat cancellation changed=%v err=%v", changed, err)
	}
	if _, found, err := s.ClaimNextJob(ctx, "worker", []string{"synthetic"}, time.Second, now); err != nil || found {
		t.Fatalf("cancelled queued claimed=%v err=%v", found, err)
	}
	running, _, err := s.EnqueueJob(ctx, testJobRequest("cancel-running"))
	if err != nil {
		t.Fatal(err)
	}
	claim := claimTestJob(t, s, now)
	if claim.ID != running.ID {
		t.Fatalf("claimed %s, want %s", claim.ID, running.ID)
	}
	if changed, err := s.CancelJob(ctx, running.ID, actor, now.Add(10*time.Millisecond)); err != nil || !changed {
		t.Fatalf("running cancellation changed=%v err=%v", changed, err)
	}
	if renewed, err := s.RenewJobLease(ctx, claim, time.Second, now.Add(20*time.Millisecond)); err != nil || renewed {
		t.Fatalf("cancelled renewal accepted=%v err=%v", renewed, err)
	}
	if applied, err := s.CompleteJob(ctx, claim, JobResult{Ref: "late"}, now.Add(20*time.Millisecond)); err != nil || applied {
		t.Fatalf("cancelled completion accepted=%v err=%v", applied, err)
	}
	current, err := s.Job(ctx, running.ID)
	if err != nil || current.State != JobCancelled || current.Result.Ref != "" || current.CancelledBy.ID != actor.ID {
		t.Fatalf("cancelled state: %+v err=%v", current, err)
	}
	attempts, err := s.JobAttempts(ctx, running.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "cancelled" {
		t.Fatalf("cancelled attempt: %+v err=%v", attempts, err)
	}
}

func TestExpiredFinalAttemptFailsWithoutReclaim(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	request := testJobRequest("expired-final")
	request.MaxAttempts = 1
	job, _, err := s.EnqueueJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(time.Second)
	claim, found, err := s.ClaimNextJob(ctx, "worker", []string{"synthetic"}, 100*time.Millisecond, base)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if _, found, err := s.ClaimNextJob(ctx, "later", []string{"synthetic"}, time.Second, base.Add(200*time.Millisecond)); err != nil || found {
		t.Fatalf("expired final attempt reclaimed=%v err=%v", found, err)
	}
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.State != JobFailed || current.LastErrorCode != "lease_expired" {
		t.Fatalf("expired final state: %+v err=%v", current, err)
	}
	if applied, err := s.CompleteJob(ctx, claim, JobResult{Ref: "too-late"}, base.Add(250*time.Millisecond)); err != nil || applied {
		t.Fatalf("late completion applied=%v err=%v", applied, err)
	}
	attempts, err := s.JobAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != "expired" {
		t.Fatalf("expired attempt: %+v err=%v", attempts, err)
	}
}

func TestProviderRetryMinimumSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := s.EnqueueJob(ctx, testJobRequest("provider-wait"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(time.Second)
	claim := claimTestJob(t, s, base)
	providerMinimum := base.Add(120 * time.Second)
	if applied, err := s.FailJob(ctx, claim, JobFailure{
		Code: "provider_throttled", Message: "Provider requested a later retry",
		Retryable: true, RetryNotBefore: providerMinimum,
	}, base.Add(100*time.Millisecond)); err != nil || !applied {
		t.Fatalf("deferred failure applied=%v err=%v", applied, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err := s.Job(ctx, job.ID)
	if err != nil || current.AvailableAt.Before(providerMinimum) {
		t.Fatalf("persisted provider minimum: %+v err=%v", current, err)
	}
	if _, found, err := s.ClaimNextJob(ctx, "early", []string{"synthetic"}, time.Second, base.Add(119*time.Second)); err != nil || found {
		t.Fatalf("early claim found=%v err=%v", found, err)
	}
	if next, found, err := s.ClaimNextJob(ctx, "later", []string{"synthetic"}, time.Second, base.Add(121*time.Second)); err != nil || !found || next.ID != job.ID {
		t.Fatalf("late claim found=%v job=%+v err=%v", found, next, err)
	}
	attempts, err := s.JobAttempts(ctx, job.ID)
	if err != nil || len(attempts) != 2 || attempts[0].RetryNotBefore.Before(providerMinimum) {
		t.Fatalf("provider wait provenance: %+v err=%v", attempts, err)
	}
}
