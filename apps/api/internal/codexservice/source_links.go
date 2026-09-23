package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// SourceLinksArgs names one preexisting company resource in the active round.
// The model cannot provide a URL: the server reads the stored company website.
type SourceLinksArgs struct {
	RoundID               string `json:"roundId"`
	Capability            string `json:"capability"`
	RequestKey            string `json:"requestKey"`
	ResourceID            string `json:"resourceId"`
	Offset                int    `json:"offset,omitempty"`
	ExpectedContentSHA256 string `json:"expectedContentSha256,omitempty"`
}
type sourceCall struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// RoundSourceLinks performs one charged, read-only official-site lookup. Links
// are unverified candidate evidence; this method does not create a company,
// vacancy, or I08 source record. Repeated keys never repeat the HTTP request.
func (s *Service) RoundSourceLinks(ctx context.Context, args SourceLinksArgs) (SourceLinksSnapshot, error) {
	if ctx == nil || args.RoundID == "" || args.Capability == "" || args.RequestKey == "" || !strings.HasPrefix(args.ResourceID, "company:") || len(args.ResourceID) <= len("company:") || args.Offset < 0 || args.Offset > maxSourceLinkOffset || args.Offset%maxSourceLinks != 0 || args.Offset > 0 && len(args.ExpectedContentSHA256) != 64 || args.Offset == 0 && args.ExpectedContentSHA256 != "" {
		return SourceLinksSnapshot{}, store.ErrInvalid
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return SourceLinksSnapshot{}, err
	}
	round, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return SourceLinksSnapshot{}, err
	}
	if !time.Now().Before(round.Deadline) {
		return SourceLinksSnapshot{}, store.ErrExpired
	}
	company, err := s.db.Company(ctx, strings.TrimPrefix(args.ResourceID, "company:"))
	if err != nil {
		return SourceLinksSnapshot{}, err
	}
	if company.ArchivedAt != "" || company.Website == "" {
		return SourceLinksSnapshot{}, store.ErrInvalid
	}
	sourceURL, err := officialSourceURL(company.Website)
	if err != nil {
		return SourceLinksSnapshot{}, store.ErrInvalid
	}
	cost, _ := store.RoundOperationCost(store.RoundSearchSource)
	attempt, created, err := s.db.ReserveRoundAttempt(ctx, authority.Actor, args.RoundID, store.RoundAttemptInput{RequestKey: args.RequestKey, Operation: store.RoundSearchSource, ResourceID: args.ResourceID, Cost: cost, BoundCapability: args.Capability})
	if err != nil {
		return SourceLinksSnapshot{}, err
	}
	if !created {
		if attempt.State != store.AttemptSucceeded {
			return SourceLinksSnapshot{}, store.ErrUncertain
		}
		var saved SourceLinksSnapshot
		if json.Unmarshal(attempt.Result, &saved) != nil {
			return SourceLinksSnapshot{}, store.ErrUncertain
		}
		if saved.Offset != args.Offset || (args.Offset > 0 && saved.ContentSHA256 != args.ExpectedContentSHA256) {
			return SourceLinksSnapshot{}, store.ErrRoundIdempotencyConflict
		}
		return saved, nil
	}
	if _, err = s.db.MarkRoundDispatched(ctx, args.RoundID, attempt.ID); err != nil {
		return SourceLinksSnapshot{}, err
	}
	deadline := time.Now().Add(sourceFetchTimeout)
	if round.Deadline.Before(deadline) {
		deadline = round.Deadline
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	call := &sourceCall{cancel: cancel, done: make(chan struct{})}
	s.sourceMu.Lock()
	if s.sourceCalls == nil {
		s.sourceCalls = map[string]*sourceCall{}
	}
	s.sourceCalls[attempt.ID] = call
	s.sourceMu.Unlock()
	defer func() {
		s.sourceMu.Lock()
		delete(s.sourceCalls, attempt.ID)
		close(call.done)
		s.sourceMu.Unlock()
		cancel()
	}()
	current, err := s.db.RoundAttempt(callCtx, attempt.ID)
	if err != nil || current.State != store.AttemptDispatched || current.Generation != attempt.Generation {
		return SourceLinksSnapshot{}, store.ErrFenced
	}
	snapshot, fetchErr := s.linkFetch(callCtx, sourceURL.String(), SourceLinkPage{Offset: args.Offset, ExpectedContentSHA256: args.ExpectedContentSHA256})
	finishCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if fetchErr != nil {
		code := sourceErrorCode(fetchErr)
		if !time.Now().Before(round.Deadline) {
			_, expireErr := s.db.ExpireRound(finishCtx, args.RoundID)
			late, _ := json.Marshal(map[string]any{"code": code, "remoteRequestMayHaveCompleted": true})
			_ = s.db.RecordLateRoundResult(finishCtx, args.RoundID, attempt.ID, late)
			return SourceLinksSnapshot{}, errors.Join(store.ErrExpired, fetchErr, expireErr)
		}
		if _, err := s.db.FinishRoundAttempt(finishCtx, authority.Actor, args.RoundID, attempt.ID, false, nil, code); err != nil {
			if errors.Is(err, store.ErrExpired) {
				_, _ = s.db.ExpireRound(finishCtx, args.RoundID)
			}
			late, _ := json.Marshal(map[string]any{"code": code, "remoteRequestMayHaveCompleted": true})
			_ = s.db.RecordLateRoundResult(finishCtx, args.RoundID, attempt.ID, late)
			return SourceLinksSnapshot{}, err
		}
		return SourceLinksSnapshot{}, fetchErr
	}
	snapshot.AttemptID = attempt.ID
	snapshot.CompanyID = company.ID
	snapshot.CompanyRevision = company.Revision
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return SourceLinksSnapshot{}, store.ErrInvalid
	}
	if !time.Now().Before(round.Deadline) {
		_, expireErr := s.db.ExpireRound(finishCtx, args.RoundID)
		_ = s.db.RecordLateRoundResult(finishCtx, args.RoundID, attempt.ID, encoded)
		return SourceLinksSnapshot{}, errors.Join(store.ErrExpired, expireErr)
	}
	if _, err := s.db.FinishRoundAttempt(finishCtx, authority.Actor, args.RoundID, attempt.ID, true, encoded, ""); err != nil {
		if errors.Is(err, store.ErrExpired) {
			_, _ = s.db.ExpireRound(finishCtx, args.RoundID)
		}
		_ = s.db.RecordLateRoundResult(finishCtx, args.RoundID, attempt.ID, encoded)
		return SourceLinksSnapshot{}, err
	}
	return snapshot, nil
}
func sourceErrorCode(err error) string {
	switch {
	case errors.Is(err, errSourceAddress):
		return "source_address_rejected"
	case errors.Is(err, errSourceTooLarge):
		return "source_response_too_large"
	case errors.Is(err, errSourceContentType):
		return "source_content_type"
	case errors.Is(err, errSourceChanged):
		return "source_changed"
	case errors.Is(err, context.Canceled):
		return "source_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "source_timeout"
	default:
		return "source_fetch_failed"
	}
}
func (s *Service) cancelSourceCalls() {
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	for _, call := range s.sourceCalls {
		call.cancel()
	}
}
func (s *Service) cancelSourceDispatch(ctx context.Context, attemptID string) error {
	s.sourceMu.Lock()
	call := s.sourceCalls[attemptID]
	if call != nil {
		call.cancel()
	}
	s.sourceMu.Unlock()
	if call == nil {
		return ErrUnavailable
	}
	select {
	case <-call.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
