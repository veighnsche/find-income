package agency

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/codexservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/jev"
	"github.com/veighnsche/find-income-dashboard/api/internal/jevservice"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

func (e *Engine) checkOfferComparison(ctx context.Context) error {
	if e == nil || e.Store == nil || e.Runtime == nil {
		return errors.New("offer comparison unavailable")
	}
	return e.Runtime.CheckRound(ctx, "compare_offers")
}

func offerIntakeScope(round store.Round) (string, error) {
	if len(round.Scope.Resources) != 2 || round.Scope.Resources[1] != "campaign:active" ||
		!strings.HasPrefix(round.Scope.Resources[0], "offer_intake:") || len(round.Scope.Resources[0]) == len("offer_intake:") {
		return "", store.ErrInvalid
	}
	return strings.TrimPrefix(round.Scope.Resources[0], "offer_intake:"), nil
}

func (e *Engine) launchOfferComparison(round store.Round) error {
	if round.State != store.RoundRunning || round.Outcome != "compare_offers" {
		return store.ErrInvalid
	}
	if _, err := offerIntakeScope(round); err != nil {
		return err
	}
	base := e.Context
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithDeadline(base, round.Deadline)
	if err := e.checkOfferComparison(ctx); err != nil {
		cancel()
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		e.active = map[string]*activeWorker{}
	}
	if _, exists := e.active[round.ID]; exists {
		cancel()
		return store.ErrConflict
	}
	worker := &activeWorker{cancel: cancel, done: make(chan struct{})}
	e.active[round.ID] = worker
	go func() { defer cancel(); defer e.workerDone(round.ID, worker); e.runOfferComparison(ctx, round) }()
	return nil
}

type offerReport struct {
	Code           string              `json:"code"`
	IntakeID       string              `json:"intakeId"`
	ComparisonID   string              `json:"comparisonId,omitempty"`
	TradeoffStatus string              `json:"tradeoffStatus"`
	Recommendation *homeRecommendation `json:"recommendation,omitempty"`
}

func (e *Engine) runOfferComparison(ctx context.Context, initial store.Round) {
	intakeID, err := offerIntakeScope(initial)
	if err != nil {
		return
	}
	report := offerReport{Code: "comparison_unavailable", IntakeID: intakeID, TradeoffStatus: "unavailable"}
	partial := true
	defer func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		r, err := e.Store.Round(cleanup, initial.ID)
		if err != nil || r.State != store.RoundRunning {
			return
		}
		facts := outcomeRecommendationFacts{Outcome: r.Outcome, Code: report.Code, ResultID: report.ComparisonID, TradeoffStatus: report.TradeoffStatus}
		report.Recommendation = e.computeOutcomeRecommendation(ctx, r, facts)
		data, _ := json.Marshal(report)
		status := "partial"
		if !partial {
			status = "complete"
		}
		_, _ = e.Store.FinishRound(cleanup, initial.Actor, r.ID, store.RoundCompleted, report.Code, status, data)
	}()
	r, err := e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion)
	if err != nil {
		report.Code = terminalCode(err)
		return
	}
	if found, err := e.Store.OfferComparisonByRound(ctx, r.ID); err == nil && found.Current {
		report.ComparisonID = found.ID
		report.TradeoffStatus = e.assessOfferTradeoff(ctx, r, found)
		report.Code = "comparison_ready"
		partial = report.TradeoffStatus != "selected" && report.TradeoffStatus != "unresolved"
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		report.Code = "comparison_unreadable"
		return
	}
	intake, err := e.Store.OfferIntake(ctx, intakeID)
	if err != nil {
		report.Code = "offer_intake_unavailable"
		return
	}
	evidence, err := json.Marshal(struct {
		IntakeID string `json:"intakeId"`
		Sources  any    `json:"sources"`
	}{intakeID, intake.Sources})
	if err != nil || len(evidence) > 32000 {
		report.Code = "offer_context_unbounded"
		return
	}
	brief := "Compare every complete saved offer source in evidence. Call offer_comparison_prepare with the supplied intakeId, exact offer IDs, extracted terms including cited employer and engagement labels, exact byte-for-byte citations, and one to eight cited review/clarify/weigh alternatives. Preserve unknowns. Do not invent employer, engagement, pay, hours, holiday, benefits, or arrangement; use Unknown employer when the employer is absent. Enter unsupported currency amounts as cited raw text; never convert currency or calculate project revenue as employment salary. Do not accept, reject, negotiate, or contact anyone. The tool computes exact pay comparisons and persists the result."
	_, err = e.Runtime.ExecuteRoundTurn(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, r.ID, codexservice.RoundTurnInput{RequestKey: "compare:" + intakeID, ResourceID: "offer_intake:" + intakeID, Brief: brief, Evidence: string(evidence)})
	if err != nil {
		report.Code = terminalCode(err)
		return
	}
	if _, err = e.live(ctx, initial.ID, initial.Generation, initial.ProfileVersion); err != nil {
		report.Code = terminalCode(err)
		return
	}
	found, err := e.Store.OfferComparisonByRound(ctx, r.ID)
	if err != nil || !found.Current {
		report.Code = "comparison_not_prepared"
		return
	}
	report.ComparisonID = found.ID
	report.TradeoffStatus = e.assessOfferTradeoff(ctx, r, found)
	report.Code = "comparison_ready"
	partial = report.TradeoffStatus != "selected" && report.TradeoffStatus != "unresolved"
}

func (e *Engine) assessOfferTradeoff(ctx context.Context, round store.Round, comparison store.SavedOfferComparison) string {
	if comparison.TradeoffStatus != "pending" {
		return comparison.TradeoffStatus
	}
	input, err := comparison.Comparison.TradeoffInput(store.OfferTradeoffMaxReportedTokens)
	if err != nil {
		return "invalid"
	}
	prefix := "offer-tradeoff:" + comparison.ID
	ids, err := e.Store.JevAttemptIDsForRequestPrefix(ctx, round.ID, prefix)
	if err != nil {
		return "unavailable"
	}
	if len(ids) > 0 {
		attempt, err := e.Store.JevAttempt(ctx, ids[0])
		if err != nil {
			return "unavailable"
		}
		if attempt.Status == "failed" || attempt.Status == "budget_exceeded" {
			return "failed"
		}
		if attempt.Status == "invalid_response" {
			return "invalid"
		}
		if attempt.Status != "succeeded" || attempt.ResponseTruncated || attempt.ResponseReadError {
			return "uncertain"
		}
		result, err := jev.RecoverCapturedOfferTradeoff(input, attempt.LogicalRequestJSON, attempt.RawResponseBytes, attempt.RequestedModel)
		if err != nil {
			return "invalid"
		}
		if err := e.Store.SaveOfferTradeoff(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, comparison.ID, attempt.ID, result); err != nil {
			return "unavailable"
		}
		return string(result.Disposition)
	}
	if e.Tradeoffs == nil {
		return "unavailable"
	}
	if _, err = e.live(ctx, round.ID, round.Generation, round.ProfileVersion); err != nil {
		return "unavailable"
	}
	result, err := e.Tradeoffs.RunOfferTradeoff(ctx, jevservice.Binding{Actor: store.Actor{Kind: "agent", ID: "codex-runner"}, RoundID: round.ID, ResourceID: "offer_intake:" + comparison.IntakeID, RequestKeyPrefix: prefix, ProfileVersion: round.ProfileVersion, MaxReportedTokens: store.OfferTradeoffMaxReportedTokens}, input)
	if err != nil {
		return "unavailable"
	}
	ids, err = e.Store.JevAttemptIDsForRequestPrefix(ctx, round.ID, prefix)
	if err != nil || len(ids) != 1 {
		return "unavailable"
	}
	if err = e.Store.SaveOfferTradeoff(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, comparison.ID, ids[0], result); err != nil {
		return "unavailable"
	}
	return string(result.Disposition)
}

// RecoverLocalDispatch projects an exact captured Jev Choice before Resume.
// It never calls a provider and never spends a reconciliation allowance.
func (e *Engine) RecoverLocalDispatch(ctx context.Context, roundID, attemptID string, generation int64) (bool, bool, error) {
	round, err := e.Store.Round(ctx, roundID)
	if err != nil {
		return false, false, err
	}
	attempt, err := e.Store.RoundAttempt(ctx, attemptID)
	if err != nil {
		return false, false, err
	}
	if round.Generation != generation || round.State != store.RoundPaused || attempt.RoundID != roundID {
		return false, false, store.ErrFenced
	}
	if handled, resolved, err := e.recoverOutcomeRecommendation(ctx, round, attempt); handled || err != nil {
		return handled, resolved, err
	}
	if round.Outcome != "compare_offers" || attempt.Operation != store.RoundJevRequest {
		return false, false, nil
	}
	comparison, err := e.Store.OfferComparisonByRound(ctx, round.ID)
	if err != nil || !comparison.Current {
		return true, false, nil
	}
	capture, err := e.Store.OfferTradeoffJevAttemptByRoundAttempt(ctx, attempt.ID)
	if err != nil {
		return true, false, nil
	}
	if capture.Status == "succeeded" && !capture.ResponseTruncated && !capture.ResponseReadError {
		input, err := comparison.Comparison.TradeoffInput(store.OfferTradeoffMaxReportedTokens)
		if err != nil {
			return true, false, nil
		}
		result, err := jev.RecoverCapturedOfferTradeoff(input, capture.LogicalRequestJSON, capture.RawResponseBytes, capture.RequestedModel)
		if err != nil {
			return true, false, nil
		}
		if err := e.Store.SaveOfferTradeoff(ctx, store.Actor{Kind: "agent", ID: "codex-runner"}, comparison.ID, capture.ID, result); err != nil {
			return true, false, err
		}
		return true, true, e.Store.ResolveCapturedOfferRoundAttempt(ctx, round.ID, attempt.ID, capture.ID, true)
	}
	if capture.Status == "failed" || capture.Status == "invalid_response" || capture.Status == "budget_exceeded" {
		return true, true, e.Store.ResolveCapturedOfferRoundAttempt(ctx, round.ID, attempt.ID, capture.ID, false)
	}
	return true, false, nil
}
