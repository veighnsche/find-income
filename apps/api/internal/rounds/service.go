// Package rounds coordinates a commissioned outcome without choosing sources,
// classifying jobs or performing any provider operation itself.
package rounds

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type Readiness interface {
	CheckRound(context.Context, string) error
}

type Canceller interface {
	CancelDispatch(context.Context, string) error
}

type Observation struct {
	State    store.RoundAttemptState
	Evidence json.RawMessage
}

type Reconciler interface {
	ObserveDispatch(context.Context, string) (Observation, error)
}

// Worker accepts only a round already committed by owner Start or Resume.
// LaunchRound returns after ownership is established, not after provider work.
type Worker interface {
	LaunchRound(context.Context, store.Round) error
	CancelRound(string)
}

type Service struct {
	Store      *store.Store
	Readiness  Readiness
	Canceller  Canceller
	Reconciler Reconciler
	Worker     Worker
}

var ErrNotReady = errors.New("round capability unavailable")

func (s *Service) ready(ctx context.Context, outcome string) error {
	if s == nil || s.Store == nil || s.Readiness == nil || s.Worker == nil {
		return ErrNotReady
	}
	return s.Readiness.CheckRound(ctx, outcome)
}

// Start checks capability before creating new work. An identical request
// returns its durable round even if the runtime later becomes unavailable.
func (s *Service) Start(ctx context.Context, actor store.Actor, input store.StartRoundInput) (store.Round, bool, error) {
	if s == nil || s.Store == nil {
		return store.Round{}, false, ErrNotReady
	}
	_, err := s.Store.RoundByRequest(ctx, actor, input.RequestKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Round{}, false, err
	}
	if errors.Is(err, store.ErrNotFound) {
		if err := s.ready(ctx, input.Outcome); err != nil {
			return store.Round{}, false, err
		}
	}
	r, created, err := s.Store.StartRound(ctx, actor, input)
	if err != nil || !created {
		return r, created, err
	}
	r, err = s.Store.ActivateRound(ctx, actor, r.ID)
	if err != nil {
		return r, true, err
	}
	if err := s.Worker.LaunchRound(ctx, r); err != nil {
		failed, finishErr := s.Store.FinishRound(ctx, actor, r.ID, store.RoundFailed, "worker_unavailable", "none", json.RawMessage(`{"code":"worker_unavailable"}`))
		if finishErr != nil {
			return r, true, errors.Join(ErrNotReady, err, finishErr)
		}
		return failed, true, errors.Join(ErrNotReady, err)
	}
	return r, true, nil
}

// Stop commits the fence first. Cancellation is advisory and its result is
// recorded without treating acknowledgement as proof of remote completion.
func (s *Service) Stop(ctx context.Context, actor store.Actor, roundID string) (store.Round, error) {
	if s == nil || s.Store == nil {
		return store.Round{}, ErrNotReady
	}
	r, attempts, err := s.Store.StopRound(ctx, actor, roundID)
	if err != nil {
		return store.Round{}, err
	}
	if s.Worker != nil {
		s.Worker.CancelRound(roundID)
	}
	if r.State == store.RoundPaused {
		return r, nil
	}
	for _, attempt := range attempts {
		var cancelErr error
		if s.Canceller == nil {
			cancelErr = ErrNotReady
		} else {
			cancelCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cancelErr = s.Canceller.CancelDispatch(cancelCtx, attempt.ID)
			cancel()
		}
		if err := s.Store.RecordCancelSignal(ctx, roundID, attempt.ID, cancelErr); err != nil {
			return store.Round{}, err
		}
	}
	return s.Store.PauseStoppedRound(ctx, actor, roundID)
}

// Resume spends remaining request/tool allowance on each needed remote check.
// An unresolved check keeps the round paused and bars the same dispatch key.
func (s *Service) Resume(ctx context.Context, actor store.Actor, roundID string) (store.Round, error) {
	if s == nil || s.Store == nil {
		return store.Round{}, ErrNotReady
	}
	if actor.Kind != "administrator" || actor.ID == "" {
		return store.Round{}, store.ErrInvalid
	}
	r, err := s.Store.Round(ctx, roundID)
	if err != nil {
		return store.Round{}, err
	}
	if r.State != store.RoundPaused {
		return store.Round{}, store.ErrFenced
	}
	if !time.Now().Before(r.Deadline) {
		return store.Round{}, store.ErrExpired
	}
	if err := s.ready(ctx, r.Outcome); err != nil {
		return store.Round{}, err
	}
	attempts, err := s.Store.UncertainRoundAttempts(ctx, roundID)
	if err != nil {
		return store.Round{}, err
	}
	if len(attempts) != 0 && s.Reconciler == nil {
		return store.Round{}, store.ErrUncertain
	}
	for _, attempt := range attempts {
		check, err := s.Store.BeginRoundReconciliation(ctx, roundID, attempt.ID, r.Generation)
		if err != nil {
			return store.Round{}, err
		}
		observeCtx, cancel := context.WithDeadline(ctx, r.Deadline)
		observation, observeErr := s.Reconciler.ObserveDispatch(observeCtx, attempt.ID)
		cancel()
		if observeErr != nil {
			observation = Observation{State: store.AttemptUncertain, Evidence: json.RawMessage(`{"code":"check_failed"}`)}
		}
		if _, err := s.Store.FinishRoundReconciliation(ctx, check, observation.State, observation.Evidence); err != nil {
			return store.Round{}, err
		}
	}
	resumed, err := s.Store.ResumeRound(ctx, actor, roundID, r.Generation)
	if err != nil {
		return store.Round{}, err
	}
	if err := s.Worker.LaunchRound(ctx, resumed); err != nil {
		failed, finishErr := s.Store.FinishRound(ctx, actor, roundID, store.RoundFailed, "worker_unavailable", "partial", json.RawMessage(`{"code":"worker_unavailable"}`))
		if finishErr != nil {
			return resumed, errors.Join(ErrNotReady, err, finishErr)
		}
		return failed, errors.Join(ErrNotReady, err)
	}
	return resumed, nil
}

func (s *Service) Reserve(ctx context.Context, actor store.Actor, roundID string, input store.RoundAttemptInput) (store.RoundAttempt, bool, error) {
	if s == nil || s.Store == nil {
		return store.RoundAttempt{}, false, ErrNotReady
	}
	cost, ok := store.RoundOperationCost(input.Operation)
	if !ok {
		return store.RoundAttempt{}, false, store.ErrInvalid
	}
	input.Cost = cost
	return s.Store.ReserveRoundAttempt(ctx, actor, roundID, input)
}

func (s *Service) Dispatch(ctx context.Context, roundID, attemptID string) (store.RoundAttempt, error) {
	if s == nil || s.Store == nil {
		return store.RoundAttempt{}, ErrNotReady
	}
	return s.Store.MarkRoundDispatched(ctx, roundID, attemptID)
}

func (s *Service) Complete(ctx context.Context, actor store.Actor, roundID, attemptID string, result json.RawMessage) (store.RoundAttempt, error) {
	if s == nil || s.Store == nil {
		return store.RoundAttempt{}, ErrNotReady
	}
	return s.Store.FinishRoundAttempt(ctx, actor, roundID, attemptID, true, result, "")
}

func (s *Service) ApplyMutation(ctx context.Context, actor store.Actor, roundID string, input store.RoundMutationInput) (store.RoundMutationResult, bool, error) {
	if s == nil || s.Store == nil {
		return store.RoundMutationResult{}, false, ErrNotReady
	}
	return s.Store.ApplyRoundMutation(ctx, actor, roundID, input)
}
