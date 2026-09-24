// The commissioned agent loop: drive one run's turns sequentially through
// the supervisor until the work settles or policy stops it.
//
// Owner: lane B (runtime), T17. Turns are caller-serialized: the loop
// issues one ContinueRun at a time with deterministic per-turn request
// keys, so a crash-restart replays settled turns instead of duplicating
// them. Executor operations dispatched by the model's research_execute
// calls run under the supervisor's per-run semaphore concurrently. The
// loop never auto-retries uncertain work and never grants fresh budget:
// it stops for owner reconcile and spends the remaining allowance only.
package researchservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/rounds"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

// Loop stop reasons: policy outcomes, not failures.
const (
	LoopTurnsExhausted  = "turn_budget_reached"
	LoopBudgetExhausted = "budget_exhausted"
	LoopStopped         = "run_stopped"
	LoopTurnInterrupted = "turn_interrupted"
)

// LoopOpts bounds one loop invocation. MaxTurns 0 means the run's own
// turn bound; TurnKeyPrefix namespaces the deterministic per-turn
// request keys (default "loop-turn").
type LoopOpts struct {
	MaxTurns      int
	TurnKeyPrefix string
}

// LoopTurn aggregates one loop iteration's settled outcome.
type LoopTurn struct {
	Index     int      `json:"index"`
	AttemptID string   `json:"attemptId"`
	ThreadID  string   `json:"threadId"`
	TurnID    string   `json:"turnId"`
	Status    string   `json:"status"`
	Replayed  bool     `json:"replayed"`
	NextWork  []string `json:"nextWork,omitempty"`
}

// LoopOutcome aggregates the loop's actual outcomes from durable state:
// per-turn results, saved record ids, unresolved attempts, the unknown
// latch and the policy stop reason.
type LoopOutcome struct {
	RunID      string                               `json:"runId"`
	Turns      []LoopTurn                           `json:"turns"`
	SavedIDs   []string                             `json:"savedIds"`
	Unresolved []researchcontract.UnresolvedAttempt `json:"unresolved"`
	Unknown    bool                                 `json:"unknown"`
	StopReason string                               `json:"stopReason"`
}

// RunLoop drives one commissioned run to a policy stop. Terminal-by-policy
// outcomes (turn budget reached, allowance exhausted, stopped run,
// interrupted turn) return a nil error with StopReason set; uncertain
// work, cancellation and internal failures return the outcome so far plus
// a non-nil error. Uncertain attempts are never retried here: the owner
// reconciles first.
func (s *Service) RunLoop(ctx context.Context, agent store.Actor, runID string, opts LoopOpts) (LoopOutcome, error) {
	outcome := LoopOutcome{RunID: runID, Turns: []LoopTurn{}, SavedIDs: []string{}, Unresolved: []researchcontract.UnresolvedAttempt{}}
	if agent.Kind != "agent" || agent.ID == "" {
		return outcome, researchcontract.NewError(researchcontract.OutcomeInvalid, "agent",
			"the agent loop requires the delegated agent actor")
	}
	if runID == "" {
		return outcome, researchcontract.NewError(researchcontract.OutcomeInvalid, "run", "run id required")
	}
	// RunBoundsFor fails for unknown runs, so the loop never starts blind.
	if _, err := s.sup.RunBoundsFor(ctx, runID); err != nil {
		return outcome, err
	}
	turns := opts.MaxTurns
	if turns <= 0 {
		turns = s.turnBound(ctx, runID)
	}
	if turns <= 0 {
		return outcome, researchcontract.NewError(researchcontract.OutcomeInvalid, "allowance",
			"run "+runID+" has no turn budget to loop over")
	}
	prefix := opts.TurnKeyPrefix
	if prefix == "" {
		prefix = "loop-turn"
	}
	for i := 0; i < turns; i++ {
		if err := ctx.Err(); err != nil {
			s.aggregateLoopState(ctx, &outcome)
			return outcome, err
		}
		turn, err := s.sup.ContinueRun(ctx, agent, rounds.TurnInput{
			RunID: runID, RequestKey: fmt.Sprintf("%s-%03d", prefix, i+1)})
		if err != nil {
			s.aggregateLoopState(ctx, &outcome)
			var cerr *researchcontract.Error
			if errors.As(err, &cerr) {
				switch cerr.Code {
				case researchcontract.OutcomeBudgetExhausted:
					outcome.StopReason = LoopBudgetExhausted
					return outcome, nil
				case researchcontract.OutcomeStopped:
					outcome.StopReason = LoopStopped
					return outcome, nil
				}
			}
			return outcome, err
		}
		outcome.Turns = append(outcome.Turns, LoopTurn{Index: i + 1,
			AttemptID: turn.AttemptID, ThreadID: turn.ThreadID, TurnID: turn.TurnID,
			Status: turn.Status, Replayed: turn.Replayed, NextWork: turn.NextWork})
		if turn.Status == "interrupted" {
			s.aggregateLoopState(ctx, &outcome)
			outcome.StopReason = LoopTurnInterrupted
			return outcome, nil
		}
	}
	s.aggregateLoopState(ctx, &outcome)
	outcome.StopReason = LoopTurnsExhausted
	return outcome, nil
}

// turnBound reads the run's commissioned turn budget: the ledger's
// enforced turns. Zero when unreadable.
func (s *Service) turnBound(ctx context.Context, runID string) int {
	ledger, err := s.sup.Usage(ctx, runID)
	if err != nil {
		return 0
	}
	return int(ledger.Enforced.Turns)
}

// aggregateLoopState folds the durable checkpoint and ledger into the
// outcome. Read failures leave the last aggregated state in place: the
// loop reports what it verified, never placeholders.
func (s *Service) aggregateLoopState(ctx context.Context, outcome *LoopOutcome) {
	cp, err := s.sup.Checkpoint(ctx, outcome.RunID)
	if err == nil {
		outcome.SavedIDs = append([]string{}, cp.SavedRecordIDs...)
		outcome.Unresolved = append([]researchcontract.UnresolvedAttempt{}, cp.UnresolvedAttempts...)
	}
	if outcome.SavedIDs == nil {
		outcome.SavedIDs = []string{}
	}
	if outcome.Unresolved == nil {
		outcome.Unresolved = []researchcontract.UnresolvedAttempt{}
	}
	if ledger, err := s.sup.Usage(ctx, outcome.RunID); err == nil && ledger.Unknown {
		outcome.Unknown = true
	}
}
