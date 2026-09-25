// Package jevservice runs supplied Jev judgments only under round authority
// and records one private Jev exchange per separately charged HTTP request.
package jevservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

type CaptureClient interface {
	EncodedRequest(jev.Request) ([]byte, error)
	EvaluateOnceCaptured(context.Context, jev.Request) (jev.Result, jev.CapturedExchange, error)
	RequestedModel() string
}

type AttemptStore interface {
	Round(context.Context, string) (store.Round, error)
	ExpireRound(context.Context, string) (store.Round, error)
	ReserveRoundAttempt(context.Context, store.Actor, string, store.RoundAttemptInput) (store.RoundAttempt, bool, error)
	MarkRoundDispatched(context.Context, string, string) (store.RoundAttempt, error)
	FinishRoundAttempt(context.Context, store.Actor, string, string, bool, json.RawMessage, string) (store.RoundAttempt, error)
	BeginJevAttempt(context.Context, store.JevAttemptStart) (store.JevAttempt, error)
	FinishJevAttempt(context.Context, store.JevAttemptFinish) (store.JevAttempt, error)
	ReclassifyJevAttempt(context.Context, string, string, string) error
}

type Service struct {
	Store  AttemptStore
	Client CaptureClient
}

type Binding struct {
	Actor             store.Actor
	RoundID           string
	ResourceID        string // Must be in the active round scope.
	RequestKeyPrefix  string // Stable for one judgment; each HTTP stage adds an index.
	ProfileVersion    int64
	BoundCapability   string // Scoped agent-tool authority, when supplied.
	MaxReportedTokens int64  // Optional except for pack relevance.
}

type recordedStage struct {
	roundAttemptID string
	jevAttemptID   string
	providerErr    error
}

type recordingEvaluator struct {
	service           Service
	binding           Binding
	purpose           string
	rubric            string
	sourceRefs        []byte
	candidates        []byte
	maxReportedTokens int64
	stages            []recordedStage
	deadline          time.Time
}

func (r *recordingEvaluator) Evaluate(ctx context.Context, request jev.Request) (jev.Result, error) {
	round, err := r.service.Store.Round(ctx, r.binding.RoundID)
	if err != nil {
		return jev.Result{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, round.Deadline)
	defer cancel()
	r.deadline = round.Deadline
	step := len(r.stages)
	logical, err := json.Marshal(struct {
		State     any                     `json:"state"`
		Questions map[string]jev.Question `json:"questions"`
	}{request.State, request.Questions})
	if err != nil {
		return jev.Result{}, &jev.Error{Kind: jev.ErrInvalidRequest}
	}
	sum := sha256.Sum256(logical)
	transport, preparationErr := r.service.Client.EncodedRequest(request)
	cost, _ := store.RoundOperationCost(store.RoundJevRequest)
	roundAttempt, created, err := r.service.Store.ReserveRoundAttempt(ctx, r.binding.Actor, r.binding.RoundID,
		store.RoundAttemptInput{RequestKey: fmt.Sprintf("%s/%d", r.binding.RequestKeyPrefix, step),
			Operation: store.RoundJevRequest, ResourceID: r.binding.ResourceID, Cost: cost, BoundCapability: r.binding.BoundCapability})
	if err != nil {
		return jev.Result{}, err
	}
	if !created {
		return jev.Result{}, store.ErrUncertain
	} // Never replay an existing dispatch.
	roundAttempt, err = r.service.Store.MarkRoundDispatched(ctx, r.binding.RoundID, roundAttempt.ID)
	if err != nil {
		return jev.Result{}, err
	}
	r.stages = append(r.stages, recordedStage{roundAttemptID: roundAttempt.ID})
	attempt, err := r.service.Store.BeginJevAttempt(ctx, store.JevAttemptStart{RoundID: r.binding.RoundID,
		RoundAttemptID: roundAttempt.ID, StepIndex: 0, Purpose: r.purpose, InputSHA256: hex.EncodeToString(sum[:]),
		SourceRefsJSON: r.sourceRefs, CandidateSetJSON: r.candidates, ProfileVersion: r.binding.ProfileVersion,
		RubricVersion: r.rubric, RequestedModel: r.service.Client.RequestedModel(),
		LogicalRequestJSON: logical, TransportRequestBytes: transport})
	if err != nil {
		return jev.Result{}, err
	}
	r.stages[step].jevAttemptID = attempt.ID
	var result jev.Result
	var exchange jev.CapturedExchange
	if preparationErr == nil {
		result, exchange, err = r.service.Client.EvaluateOnceCaptured(ctx, request)
		if !bytes.Equal(exchange.RequestBytes, transport) {
			err = &jev.Error{Kind: jev.ErrInvalidResponse}
			result = jev.Result{}
		}
	} else {
		err = preparationErr
	}
	if err == nil && r.maxReportedTokens > 0 &&
		(result.Usage.InputTokens > r.maxReportedTokens || result.Usage.OutputTokens > r.maxReportedTokens-result.Usage.InputTokens) {
		err = &jev.Error{Kind: jev.ErrBudgetExceeded}
		result = jev.Result{}
	}
	status, kind := classifyAttempt(err)
	if exchange.ResponseTruncated {
		kind = "response_truncated"
	}
	if exchange.ResponseReadError && err == nil {
		status, kind = "invalid_response", "response_read_error"
	}
	var httpStatus *int
	if exchange.HTTPStatus != 0 {
		httpStatus = &exchange.HTTPStatus
	}
	// A canceled transport context must not erase already observed response
	// bytes. This bounded cleanup records evidence only; it grants no authority.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanupCancel()
	_, finishErr := r.service.Store.FinishJevAttempt(cleanupCtx, store.JevAttemptFinish{ID: attempt.ID,
		Status: status, ReturnedModel: exchange.ReturnedModel, RawResponseBytes: exchange.ResponseBytes,
		ResponseTruncated: exchange.ResponseTruncated, ResponseReadError: exchange.ResponseReadError,
		ErrorKind: kind, HTTPStatus: httpStatus, InputTokens: exchange.InputTokens, OutputTokens: exchange.OutputTokens})
	if finishErr != nil {
		r.stages[step].providerErr = finishErr
		return jev.Result{}, finishErr
	}
	r.stages[step].providerErr = err
	return result, err
}

func classifyAttempt(err error) (string, string) {
	if err == nil {
		return "succeeded", ""
	}
	var provider *jev.Error
	if errors.As(err, &provider) {
		if provider.Kind == jev.ErrInvalidResponse {
			return "invalid_response", string(provider.Kind)
		}
		if provider.Kind == jev.ErrBudgetExceeded {
			return "budget_exceeded", string(provider.Kind)
		}
		return "failed", string(provider.Kind)
	}
	return "failed", "internal_error"
}

func safeErrorCode(err error) string {
	var provider *jev.Error
	if errors.As(err, &provider) {
		return "jev_" + string(provider.Kind)
	}
	if errors.Is(err, store.ErrAllowance) {
		return "jev_allowance"
	}
	if errors.Is(err, store.ErrFenced) {
		return "jev_fenced"
	}
	return "jev_failed"
}

func (r *recordingEvaluator) finish(ctx context.Context, helperErr error) error {
	if helperErr != nil && len(r.stages) > 0 {
		var provider *jev.Error
		last := r.stages[len(r.stages)-1]
		if last.providerErr == nil && last.jevAttemptID != "" && errors.As(helperErr, &provider) &&
			(provider.Kind == jev.ErrInvalidResponse || provider.Kind == jev.ErrBudgetExceeded) {
			status := "invalid_response"
			if provider.Kind == jev.ErrBudgetExceeded {
				status = "budget_exceeded"
			}
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := r.service.Store.ReclassifyJevAttempt(cleanupCtx, last.jevAttemptID, status, string(provider.Kind)); err != nil {
				return fmt.Errorf("record helper rejection: %w", err)
			}
		}
	}
	if !r.deadline.IsZero() && !time.Now().Before(r.deadline) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err := r.service.Store.ExpireRound(cleanupCtx, r.binding.RoundID)
		return errors.Join(store.ErrExpired, helperErr, err)
	}
	for i, stage := range r.stages {
		stageErr := stage.providerErr
		if stageErr == nil && i == len(r.stages)-1 {
			stageErr = helperErr
		}
		if stageErr != nil {
			if _, err := r.service.Store.FinishRoundAttempt(ctx, r.binding.Actor, r.binding.RoundID, stage.roundAttemptID, false, nil, safeErrorCode(stageErr)); err != nil {
				return err
			}
			continue
		}
		result, _ := json.Marshal(map[string]string{"jevAttemptId": stage.jevAttemptID})
		if _, err := r.service.Store.FinishRoundAttempt(ctx, r.binding.Actor, r.binding.RoundID, stage.roundAttemptID, true, result, ""); err != nil {
			return err
		}
	}
	return helperErr
}

func (s Service) ready(binding Binding) error {
	if s.Store == nil || s.Client == nil || binding.RoundID == "" || binding.ResourceID == "" || binding.RequestKeyPrefix == "" || binding.ProfileVersion < 1 || binding.Actor.ID == "" {
		return &jev.Error{Kind: jev.ErrInvalidConfig}
	}
	return nil
}

func (s Service) RunDecision(ctx context.Context, binding Binding, input jev.DecisionInput) (jev.DecisionResult, error) {
	if err := s.ready(binding); err != nil {
		return jev.DecisionResult{}, err
	}
	refs, _ := json.Marshal(input.Sources)
	candidates, _ := json.Marshal(input.Candidates)
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: string(input.Kind), rubric: "decision-v1", sourceRefs: refs, candidates: candidates}
	result, err := jev.SelectDecision(ctx, evaluator, input)
	if err = evaluator.finish(ctx, err); err != nil {
		return jev.DecisionResult{}, err
	}
	return result, nil
}

func (s Service) RunScreening(ctx context.Context, binding Binding, input jev.ScreeningInput) (jev.ScreeningResult, error) {
	if err := s.ready(binding); err != nil {
		return jev.ScreeningResult{}, err
	}
	refs, _ := json.Marshal(input.Spans)
	criteria, _ := json.Marshal(input.Criteria)
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: "responsibility_screening", rubric: "screening-v2", sourceRefs: refs, candidates: criteria}
	result, err := jev.ScreenResponsibilities(ctx, evaluator, input)
	if err = evaluator.finish(ctx, err); err != nil {
		return jev.ScreeningResult{}, err
	}
	return result, nil
}

func (s Service) RunOrganisation(ctx context.Context, binding Binding, input jev.OrganisationInput) (jev.OrganisationResult, error) {
	if err := s.ready(binding); err != nil {
		return jev.OrganisationResult{}, err
	}
	refs, _ := json.Marshal(input.Facts)
	categories, _ := json.Marshal(input.Categories)
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: "organisation", rubric: "organisation-v1", sourceRefs: refs, candidates: categories}
	result, err := jev.Organise(ctx, evaluator, input)
	if err = evaluator.finish(ctx, err); err != nil {
		return jev.OrganisationResult{}, err
	}
	return result, nil
}

func (s Service) RunOfferTradeoff(ctx context.Context, binding Binding, input jev.OfferTradeoffInput) (jev.OfferTradeoffResult, error) {
	if err := s.ready(binding); err != nil {
		return jev.OfferTradeoffResult{}, err
	}
	refs, _ := json.Marshal(input.Sources)
	candidates, _ := json.Marshal(input.Candidates)
	evaluator := &recordingEvaluator{service: s, binding: binding, purpose: "offer_tradeoff", rubric: "offer-tradeoff-v1", sourceRefs: refs, candidates: candidates, maxReportedTokens: input.MaxReportedTokens}
	result, err := jev.SelectOfferTradeoff(ctx, evaluator, input)
	if err = evaluator.finish(ctx, err); err != nil {
		return jev.OfferTradeoffResult{}, err
	}
	return result, nil
}
