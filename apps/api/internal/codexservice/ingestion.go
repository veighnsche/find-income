package codexservice

import (
	"context"
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/codex"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// RetryIngestion is the owner-facing route for a terminal ingestion. The store
// rejects an unresolved remote dispatch. This adapter observes the exact
// persisted turn through supported authenticated history before asking the
// store to permit a new job; it never guesses from a transport timeout.
func (s *Service) RetryIngestion(ctx context.Context, actor store.Actor, id string, fullText *string) (store.IngestionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.busy || s.disconnecting || s.logoutRequired {
		return store.IngestionRequest{}, ErrBusy
	}
	item, err := s.db.RetryIngestion(ctx, actor, id, fullText)
	if !errors.Is(err, store.ErrUncertain) {
		return item, err
	}
	if err := s.reconcileIngestionLocked(ctx, actor, id); err != nil {
		return store.IngestionRequest{}, err
	}
	return s.db.RetryIngestion(ctx, actor, id, fullText)
}

// ReconcileIngestion records only an observed terminal status. Missing IDs,
// unsupported history, an active remote turn or a different local job remain
// unresolved; no new dispatch occurs here.
func (s *Service) ReconcileIngestion(ctx context.Context, actor store.Actor, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.busy || s.disconnecting || s.logoutRequired {
		return ErrBusy
	}
	return s.reconcileIngestionLocked(ctx, actor, id)
}

func (s *Service) reconcileIngestionLocked(ctx context.Context, actor store.Actor, id string) error {
	item, err := s.db.Ingestion(ctx, id)
	if err != nil {
		return err
	}
	if actor.ID == "" || actor != item.Actor && actor.Kind != "administrator" {
		return store.ErrInvalid
	}
	if !item.DispatchStarted || item.CodexThreadID == "" || item.CodexTurnID == "" {
		return store.ErrUncertain
	}
	if item.JobState != store.JobSucceeded && item.JobState != store.JobFailed && item.JobState != store.JobCancelled {
		return store.ErrUncertain
	}
	client, reason := s.ensureLocked(ctx)
	if reason != "" {
		return ErrUnavailable
	}
	account, err := client.ReadAccount(ctx)
	if err != nil || account.Account == nil || account.Account.Type != "chatgpt" {
		return ErrUnavailable
	}
	status, err := client.ObserveTurn(ctx, item.CodexThreadID, item.CodexTurnID)
	if err != nil {
		if errors.Is(err, codex.ErrHistoryIncomplete) || errors.Is(err, codex.ErrUnsupported) {
			return store.ErrUncertain
		}
		return ErrUnavailable
	}
	if status != "completed" && status != "failed" && status != "interrupted" {
		return store.ErrUncertain
	}
	return s.db.ConfirmIngestionTerminal(ctx, item.ID, item.JobID, item.CodexThreadID, item.CodexTurnID, status)
}
