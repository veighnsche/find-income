// Package jobs runs one durable, lease-fenced background worker. Handlers
// prepare results outside database transactions and must not publish external
// artifacts before the store accepts their fenced completion.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Queue is the small storage surface needed by the executor. The concrete
// *store.Store implements it; API handlers may enqueue through store directly.
type Queue interface {
	ClaimNextJob(context.Context, string, []string, time.Duration, time.Time) (store.Job, bool, error)
	RenewJobLease(context.Context, store.Job, time.Duration, time.Time) (bool, error)
	CompleteJob(context.Context, store.Job, store.JobResult, time.Time) (bool, error)
	FailJob(context.Context, store.Job, store.JobFailure, time.Time) (bool, error)
}

// Handler must respect context cancellation. A successful return is a staged
// result description, not permission to publish a PDF or other external effect.
type Handler func(context.Context, store.Job) (store.JobResult, error)

type HandlerError struct {
	Code        string
	SafeMessage string
	Cause       error
	Retryable   bool
	RetryAfter  time.Duration
}

// Error deliberately excludes Cause: raw provider errors can contain private
// payloads, headers or credentials and must not reach persisted job history.
func (e *HandlerError) Error() string {
	if message := strings.TrimSpace(e.SafeMessage); message != "" && len(message) <= 1024 {
		return message
	}
	if e.Retryable {
		return "Temporary job handler failure"
	}
	return "Job handler failed"
}
func (e *HandlerError) Unwrap() error { return e.Cause }

func Permanent(code string, cause error) error {
	if cause == nil {
		cause = errors.New("job handler failed")
	}
	return &HandlerError{Code: code, Cause: cause}
}

func Transient(code string, cause error) error {
	if cause == nil {
		cause = errors.New("job handler failed")
	}
	return &HandlerError{Code: code, Cause: cause, Retryable: true}
}

// TransientAfter preserves a provider's valid Retry-After minimum. The safe
// message is separate from the underlying cause and may be shown to users.
func TransientAfter(code, safeMessage string, cause error, wait time.Duration) error {
	if cause == nil {
		cause = errors.New("job handler failed")
	}
	return &HandlerError{
		Code: code, SafeMessage: safeMessage, Cause: cause,
		Retryable: true, RetryAfter: wait,
	}
}

type Worker struct {
	Queue         Queue
	ID            string
	Handlers      map[string]Handler
	PollInterval  time.Duration
	LeaseDuration time.Duration
	Now           func() time.Time // optional test clock; production defaults to time.Now
}

func (w *Worker) validate() ([]string, error) {
	if w.Queue == nil || strings.TrimSpace(w.ID) == "" || len(w.Handlers) == 0 ||
		w.PollInterval <= 0 || w.LeaseDuration <= 0 {
		return nil, fmt.Errorf("worker requires queue, ID, handlers, poll and lease durations")
	}
	kinds := make([]string, 0, len(w.Handlers))
	for kind, handler := range w.Handlers {
		if strings.TrimSpace(kind) == "" || handler == nil {
			return nil, fmt.Errorf("worker has invalid handler registration")
		}
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds, nil
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

// Run processes jobs serially until cancelled. Startup wiring belongs to the
// server owner; this package has no provider, document or network handler.
func (w *Worker) Run(ctx context.Context) error {
	if _, err := w.validate(); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		processed, err := w.ProcessOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if processed {
			continue
		}
		timer := time.NewTimer(w.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// ProcessOne claims at most one job and runs its handler without holding a
// SQLite transaction. It is also useful for deterministic service tests.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	kinds, err := w.validate()
	if err != nil {
		return false, err
	}
	claim, found, err := w.Queue.ClaimNextJob(ctx, w.ID, kinds, w.LeaseDuration, w.now())
	if err != nil || !found {
		return found, err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	stopHeartbeat := make(chan struct{})
	go w.heartbeat(jobCtx, cancel, claim, stopHeartbeat, heartbeatDone)
	result, handlerErr := w.Handlers[claim.Kind](jobCtx, claim)
	close(stopHeartbeat)
	heartbeatErr := <-heartbeatDone
	cancel()
	if ctx.Err() != nil {
		// Leave the lease for recovery after an interrupted service process.
		return true, nil
	}
	if heartbeatErr != nil {
		if errors.Is(heartbeatErr, errLeaseLost) {
			return true, nil
		}
		return true, heartbeatErr
	}
	if handlerErr == nil {
		_, err = w.Queue.CompleteJob(ctx, claim, result, w.now())
		if errors.Is(err, store.ErrInvalid) {
			_, err = w.Queue.FailJob(ctx, claim, store.JobFailure{
				Code: "invalid_result", Message: "Handler returned invalid result metadata",
			}, w.now())
		}
		return true, err
	}
	at := w.now()
	failure := classify(handlerErr, at)
	_, err = w.Queue.FailJob(ctx, claim, failure, at)
	return true, err
}

var errLeaseLost = errors.New("job lease lost")

func (w *Worker) heartbeat(ctx context.Context, cancel context.CancelFunc, claim store.Job, stop <-chan struct{}, done chan<- error) {
	interval := w.LeaseDuration / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			done <- nil
			return
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			ok, err := w.Queue.RenewJobLease(ctx, claim, w.LeaseDuration, w.now())
			if err != nil {
				cancel()
				done <- err
				return
			}
			if !ok {
				cancel()
				done <- errLeaseLost
				return
			}
		}
	}
}

const maxRetryAfter = 30 * 24 * time.Hour

func classify(err error, now time.Time) store.JobFailure {
	var typed *HandlerError
	if errors.As(err, &typed) {
		if typed.RetryAfter < 0 || typed.RetryAfter > maxRetryAfter {
			return store.JobFailure{Code: "invalid_retry_schedule", Message: "Invalid retry schedule"}
		}
		failure := store.JobFailure{
			Code: safeCode(typed.Code), Message: typed.Error(), Retryable: typed.Retryable,
		}
		if typed.Retryable && typed.RetryAfter > 0 {
			failure.RetryNotBefore = now.Add(typed.RetryAfter)
		}
		return failure
	}
	return store.JobFailure{Code: "handler_error", Message: "Job handler failed"}
}

func safeCode(value string) string {
	if len(value) == 0 || len(value) > 80 {
		return "handler_error"
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "handler_error"
		}
	}
	return value
}
