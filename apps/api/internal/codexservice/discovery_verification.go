package codexservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type DiscoveryOfficialLinksArgs struct {
	RoundID                string `json:"roundId"`
	Capability             string `json:"capability"`
	RequestKey             string `json:"requestKey"`
	CandidateID            string `json:"candidateId"`
	CompanyDetailAttemptID string `json:"companyDetailAttemptId"`
	ParentAttemptID        string `json:"parentAttemptId,omitempty"`
	LinkURL                string `json:"linkUrl,omitempty"`
}

type DiscoveryOfficialSnapshot struct {
	AttemptID, CandidateID, CompanyDetailAttemptID, ParentAttemptID string
	ClaimURL, SourceURL, Status, ErrorCode                          string
	Depth                                                           int
	Evidence                                                        SourceLinksSnapshot
}

func (s *Service) discoveryOfficialLinksTool(ctx context.Context, args DiscoveryOfficialLinksArgs) (map[string]any, error) {
	snapshot, err := s.RoundDiscoveryOfficialLinks(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"snapshot": snapshot}, nil
}

func (s *Service) discoveryBoardRegisterTool(ctx context.Context, args store.DiscoveryBoardInput) (map[string]any, error) {
	result, err := s.db.RegisterDiscoveryBoard(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": result}, nil
}

// RoundDiscoveryOfficialLinks reads an evidenced company website, or one exact
// same-origin careers anchor from its prior read. A second hop is not offered.
func (s *Service) RoundDiscoveryOfficialLinks(ctx context.Context, args DiscoveryOfficialLinksArgs) (DiscoveryOfficialSnapshot, error) {
	if ctx == nil || args.RoundID == "" || args.Capability == "" || args.RequestKey == "" || args.CandidateID == "" || args.CompanyDetailAttemptID == "" || (args.ParentAttemptID == "") != (args.LinkURL == "") {
		return DiscoveryOfficialSnapshot{}, store.ErrInvalid
	}
	round, err := s.db.Round(ctx, args.RoundID)
	if err != nil {
		return DiscoveryOfficialSnapshot{}, err
	}
	if !time.Now().Before(round.Deadline) {
		_, _ = s.db.ExpireRound(ctx, args.RoundID)
		return DiscoveryOfficialSnapshot{}, store.ErrExpired
	}
	authority, err := s.db.VerifyRoundToolCapability(ctx, args.Capability, args.RoundID)
	if err != nil {
		return DiscoveryOfficialSnapshot{}, err
	}
	if err := s.db.CheckSelectedDiscoveryCandidate(ctx, args.RoundID, args.CandidateID); err != nil {
		return DiscoveryOfficialSnapshot{}, err
	}
	claim, err := s.db.DiscoveryCompanyClaim(ctx, args.CandidateID, args.CompanyDetailAttemptID, round.Actor.ID)
	if err != nil {
		return DiscoveryOfficialSnapshot{}, err
	}
	claimed, err := url.Parse(claim.ClaimedWebsiteURL)
	if err != nil {
		return DiscoveryOfficialSnapshot{}, store.ErrInvalid
	}
	claimed.RawQuery = ""
	claimed.ForceQuery = false
	claimed.Fragment = ""
	claimed.RawFragment = ""
	base, err := officialSourceURL(claimed.String())
	if err != nil {
		return DiscoveryOfficialSnapshot{}, store.ErrInvalid
	}
	sourceURL := base.String()
	depth := 0
	if args.ParentAttemptID != "" {
		parent, err := s.db.DiscoveryOfficialRead(ctx, args.ParentAttemptID)
		if err != nil {
			return DiscoveryOfficialSnapshot{}, err
		}
		if parent.CandidateID != args.CandidateID || parent.CompanyDetailAttemptID != args.CompanyDetailAttemptID || parent.ParentAttemptID != "" || parent.Status != "ok" || parent.ClaimURL != claim.ClaimedWebsiteURL {
			return DiscoveryOfficialSnapshot{}, store.ErrFenced
		}
		parentAttempt, err := s.db.RoundAttempt(ctx, args.ParentAttemptID)
		if err != nil || parentAttempt.State != store.AttemptSucceeded {
			return DiscoveryOfficialSnapshot{}, store.ErrFenced
		}
		var saved DiscoveryOfficialSnapshot
		if json.Unmarshal(parent.Snapshot, &saved) != nil || saved.Status != "ok" || saved.Evidence.Status != "ok" {
			return DiscoveryOfficialSnapshot{}, store.ErrFenced
		}
		found := false
		for _, link := range saved.Evidence.Links {
			if link.URL == args.LinkURL {
				found = true
				break
			}
		}
		if !found {
			return DiscoveryOfficialSnapshot{}, store.ErrFenced
		}
		next, err := officialSourceURL(args.LinkURL)
		if err != nil || !strings.EqualFold(next.Hostname(), base.Hostname()) {
			return DiscoveryOfficialSnapshot{}, store.ErrInvalid
		}
		sourceURL = next.String()
		depth = 1
	}
	cost, _ := store.RoundOperationCost(store.RoundFetchSource)
	attempt, created, err := s.db.ReserveRoundAttempt(ctx, authority.Actor, args.RoundID, store.RoundAttemptInput{RequestKey: args.RequestKey, Operation: store.RoundFetchSource, ResourceID: "discovery:himalayas", Cost: cost, BoundCapability: args.Capability})
	if err != nil {
		if errors.Is(err, store.ErrExpired) {
			_, _ = s.db.ExpireRound(ctx, args.RoundID)
		}
		return DiscoveryOfficialSnapshot{}, err
	}
	if !created {
		if attempt.State != store.AttemptSucceeded {
			return DiscoveryOfficialSnapshot{}, store.ErrUncertain
		}
		read, err := s.db.DiscoveryOfficialRead(ctx, attempt.ID)
		if err != nil || read.CandidateID != args.CandidateID || read.CompanyDetailAttemptID != args.CompanyDetailAttemptID || read.ParentAttemptID != args.ParentAttemptID || read.SourceURL != sourceURL {
			return DiscoveryOfficialSnapshot{}, store.ErrRoundIdempotencyConflict
		}
		var saved DiscoveryOfficialSnapshot
		if json.Unmarshal(attempt.Result, &saved) != nil {
			return DiscoveryOfficialSnapshot{}, store.ErrUncertain
		}
		return saved, nil
	}
	if _, err = s.db.MarkRoundDispatched(ctx, args.RoundID, attempt.ID); err != nil {
		return DiscoveryOfficialSnapshot{}, err
	}
	preflight := store.DiscoveryOfficialRead{AttemptID: attempt.ID, RoundID: args.RoundID, CandidateID: args.CandidateID, CompanyDetailAttemptID: args.CompanyDetailAttemptID, ParentAttemptID: args.ParentAttemptID, SourceURL: sourceURL, ClaimURL: claim.ClaimedWebsiteURL, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = s.db.PrepareDiscoveryOfficialRead(ctx, preflight); err != nil {
		return DiscoveryOfficialSnapshot{}, err
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
	links, fetchErr := s.linkFetch(callCtx, sourceURL, SourceLinkPage{})
	result := DiscoveryOfficialSnapshot{AttemptID: attempt.ID, CandidateID: args.CandidateID, CompanyDetailAttemptID: args.CompanyDetailAttemptID, ParentAttemptID: args.ParentAttemptID, ClaimURL: claim.ClaimedWebsiteURL, SourceURL: sourceURL, Depth: depth, Evidence: links}
	if fetchErr != nil {
		result.Status = "unavailable"
		result.ErrorCode = sourceErrorCode(fetchErr)
	} else {
		result.Status = links.Status
	}
	if result.Status == "" {
		result.Status = "unavailable"
	}
	encoded, _ := json.Marshal(result)
	finishCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err = s.db.CompleteDiscoveryOfficialRead(finishCtx, attempt.ID, result.Status, encoded, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return DiscoveryOfficialSnapshot{}, err
	}
	if !time.Now().Before(round.Deadline) {
		_, _ = s.db.ExpireRound(finishCtx, args.RoundID)
		return DiscoveryOfficialSnapshot{}, store.ErrExpired
	}
	if _, err = s.db.FinishRoundAttempt(finishCtx, authority.Actor, args.RoundID, attempt.ID, true, encoded, ""); err != nil {
		if errors.Is(err, store.ErrExpired) {
			_, _ = s.db.ExpireRound(finishCtx, args.RoundID)
		}
		_ = s.db.RecordLateRoundResult(finishCtx, args.RoundID, attempt.ID, encoded)
		return DiscoveryOfficialSnapshot{}, err
	}
	return result, nil
}
