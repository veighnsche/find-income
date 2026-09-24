package store

// Run-authority checks, ledger and reservation release. Owner: lane B
// (runtime), T12. The read helpers take a Reader so they compose inside any
// transaction (D's save transactions, ResearchWrite) as well as standalone
// Store.Read calls; *sql.Tx and *sql.Conn both satisfy Reader.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// ResearchAuthorityResource is the fixed scope resource for research dispatch
// reservations. Commissioning a research run lists the research.* operations
// plus this resource in the round scope; per-request identity (payload hash)
// rides CursorAttemptID so scope stays enumerable.
const ResearchAuthorityResource = "research"

// ReconciledUncertain is the Reconciliation.State value recorded when an
// uncertain attempt was settled without a terminal observation. C's
// expired-lease takeover accepts it exactly like a terminal prior attempt.
const ReconciledUncertain = "reconciled_uncertain"

// recordWriteOperations are the scope operations that grant record.write:
// every operation that durably writes companies, opportunities or evidence.
var recordWriteOperations = []string{
	RoundCreateCompany, RoundCreateOpportunity, RoundSaveSourceOpportunity,
	RoundCorrectEvidence, RoundCorrectOpportunity,
}

// authorityMinimum maps a permission to the scope operations that grant it
// plus the minimum allowance the run must still hold. run.control needs no
// allowance: stopping is always affordable.
func authorityMinimum(perm researchcontract.Permission) ([]string, RoundAllowance, bool) {
	switch perm {
	case researchcontract.PermissionResearchDispatch:
		return ResearchOperations(), RoundAllowance{Requests: 1, Tools: 1}, true
	case researchcontract.PermissionRecordWrite:
		return recordWriteOperations, RoundAllowance{Requests: 1, Items: 1, Tools: 1}, true
	case researchcontract.PermissionJevRequest:
		return []string{RoundJevRequest}, RoundAllowance{Requests: 1}, true
	case researchcontract.PermissionRunControl:
		return nil, RoundAllowance{}, true
	}
	return nil, RoundAllowance{}, false
}

func scopeHasAny(have, want []string) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

func clampNonzero(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func formatRoundAllowance(a RoundAllowance) string {
	return fmt.Sprintf("requests=%d items=%d tools=%d turns=%d",
		a.Requests, a.Items, a.Tools, a.Turns)
}

// ParseAuthorityTime parses round/attempt/check timestamps (utcNow format,
// record layout tolerated).
func ParseAuthorityTime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	return time.Parse(recordTimeLayout, value)
}

// CheckRoundAuthorityTx validates run/generation/liveness/deadline/permission/
// allowance in that order and reports WHICH failed plus what remains allowed;
// there is no generic authority error. It returns the remaining allowance on
// both success and failure so callers can report or budget without re-reading.
// now is the caller's clock; a zero now means time.Now.
func CheckRoundAuthorityTx(ctx context.Context, r Reader, roundID string, generation int64, perm researchcontract.Permission, actor Actor, now time.Time) (RoundAllowance, error) {
	if r == nil || roundID == "" {
		return RoundAllowance{}, researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityRun), "run id required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	round, err := scanRound(r.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
	if errors.Is(err, sql.ErrNoRows) {
		return RoundAllowance{}, researchcontract.NewError(researchcontract.OutcomeNotFound,
			string(researchcontract.AuthorityRun), "unknown run "+roundID)
	}
	if err != nil {
		return RoundAllowance{}, err
	}
	remaining := RoundAllowance{
		Requests: clampNonzero(round.Limits.Requests - round.Used.Requests),
		Items:    clampNonzero(round.Limits.Items - round.Used.Items),
		Tools:    clampNonzero(round.Limits.Tools - round.Used.Tools),
		Turns:    clampNonzero(round.Limits.Turns - round.Used.Turns),
	}
	if generation != round.Generation {
		return remaining, researchcontract.NewError(researchcontract.OutcomeStale,
			string(researchcontract.AuthorityGeneration),
			fmt.Sprintf("presented generation %d, current is %d (remaining %s)",
				generation, round.Generation, formatRoundAllowance(remaining)))
	}
	switch round.State {
	case RoundRunning:
	case RoundPaused, RoundStopping, RoundAwaitingInput:
		return remaining, researchcontract.NewError(researchcontract.OutcomeStopped,
			string(researchcontract.AuthorityRun),
			fmt.Sprintf("round %s at generation %d; resume before new work (remaining %s)",
				round.State, round.Generation, formatRoundAllowance(remaining)))
	case RoundQueued:
		return remaining, researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityRun),
			fmt.Sprintf("round not activated (remaining %s)", formatRoundAllowance(remaining)))
	default:
		return remaining, researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityRun),
			fmt.Sprintf("round terminal (%s) (remaining %s)", round.State, formatRoundAllowance(remaining)))
	}
	if !now.Before(round.Deadline) {
		return remaining, researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityDeadline),
			fmt.Sprintf("deadline %s passed (checked at %s; remaining %s)",
				round.Deadline.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339),
				formatRoundAllowance(remaining)))
	}
	ops, minimum, ok := authorityMinimum(perm)
	if !ok {
		return remaining, researchcontract.NewError(researchcontract.OutcomeInvalid,
			string(researchcontract.AuthorityPermission), fmt.Sprintf("unknown permission %q", perm))
	}
	if perm == researchcontract.PermissionRunControl {
		if actor.Kind != "administrator" || actor.ID == "" {
			return remaining, researchcontract.NewError(researchcontract.OutcomeForbidden,
				string(researchcontract.AuthorityPermission), "run.control requires the owner administrator")
		}
	} else {
		if !scopeAllowsActor(round.Scope, actor) {
			return remaining, researchcontract.NewError(researchcontract.OutcomeForbidden,
				string(researchcontract.AuthorityPermission),
				fmt.Sprintf("%s denied for %s %q: not delegated on this run (remaining %s)",
					perm, actor.Kind, actor.ID, formatRoundAllowance(remaining)))
		}
		if !scopeHasAny(round.Scope.Operations, ops) {
			return remaining, researchcontract.NewError(researchcontract.OutcomeForbidden,
				string(researchcontract.AuthorityPermission),
				fmt.Sprintf("%s needs a scoped operation in %v (remaining %s)",
					perm, ops, formatRoundAllowance(remaining)))
		}
	}
	if remaining.Requests < minimum.Requests || remaining.Items < minimum.Items ||
		remaining.Tools < minimum.Tools || remaining.Turns < minimum.Turns {
		return remaining, researchcontract.NewError(researchcontract.OutcomeBudgetExhausted,
			string(researchcontract.AuthorityAllowance),
			fmt.Sprintf("remaining %s cannot cover %s minimum %s",
				formatRoundAllowance(remaining), perm, formatRoundAllowance(minimum)))
	}
	return remaining, nil
}

func toContractAllowance(a RoundAllowance) researchcontract.Allowance {
	return researchcontract.Allowance{Requests: a.Requests, Items: a.Items, Tools: a.Tools, Turns: a.Turns}
}

// RoundAuthorityLedgerTx reads the enforced/reserved/observed/unknown ledger.
// Reserved sums in-flight attempts (reserved, dispatched, uncertain);
// observed is charged-but-not-in-flight, which also covers reconciliation
// overhead; unknown is true while any attempt is uncertain or any
// reconciliation check settled unknown.
func RoundAuthorityLedgerTx(ctx context.Context, r Reader, roundID string) (researchcontract.UsageLedger, error) {
	round, err := scanRound(r.QueryRowContext(ctx, `SELECT `+roundColumns+` FROM rounds WHERE id=?`, roundID))
	if errors.Is(err, sql.ErrNoRows) {
		return researchcontract.UsageLedger{}, ErrNotFound
	}
	if err != nil {
		return researchcontract.UsageLedger{}, err
	}
	rows, err := r.QueryContext(ctx, `SELECT requests_reserved,items_reserved,tools_reserved,turns_reserved,state
  FROM round_attempts WHERE round_id=?`, roundID)
	if err != nil {
		return researchcontract.UsageLedger{}, err
	}
	defer rows.Close()
	var reserved RoundAllowance
	unknown := false
	for rows.Next() {
		var cost RoundAllowance
		var state RoundAttemptState
		if err := rows.Scan(&cost.Requests, &cost.Items, &cost.Tools, &cost.Turns, &state); err != nil {
			return researchcontract.UsageLedger{}, err
		}
		switch state {
		case AttemptReserved, AttemptDispatched, AttemptUncertain:
			reserved.Requests += cost.Requests
			reserved.Items += cost.Items
			reserved.Tools += cost.Tools
			reserved.Turns += cost.Turns
			if state == AttemptUncertain {
				unknown = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return researchcontract.UsageLedger{}, err
	}
	if !unknown {
		var one int
		if err := r.QueryRowContext(ctx, `SELECT 1 FROM round_reconciliation_checks
  WHERE round_id=? AND state='unknown' LIMIT 1`, roundID).Scan(&one); err == nil {
			unknown = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return researchcontract.UsageLedger{}, err
		}
	}
	observed := RoundAllowance{
		Requests: clampNonzero(round.Used.Requests - reserved.Requests),
		Items:    clampNonzero(round.Used.Items - reserved.Items),
		Tools:    clampNonzero(round.Used.Tools - reserved.Tools),
		Turns:    clampNonzero(round.Used.Turns - reserved.Turns),
	}
	return researchcontract.UsageLedger{
		Enforced: toContractAllowance(round.Limits),
		Reserved: toContractAllowance(reserved),
		Observed: toContractAllowance(observed),
		Unknown:  unknown,
	}, nil
}

// RoundReconciliationTx returns how an attempt was settled: its terminal
// state, or reconciled_uncertain when a reconciliation check settled unknown.
// Attempts with no settlement (reserved, dispatched, uncertain without an
// unknown check, pending/fenced checks only) and unknown ids return ErrNotFound.
func RoundReconciliationTx(ctx context.Context, r Reader, attemptID string) (researchcontract.Reconciliation, error) {
	if attemptID == "" {
		return researchcontract.Reconciliation{}, ErrInvalid
	}
	var state RoundAttemptState
	var finished, updated sql.NullString
	err := r.QueryRowContext(ctx, `SELECT state,finished_at,updated_at FROM round_attempts WHERE id=?`,
		attemptID).Scan(&state, &finished, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return researchcontract.Reconciliation{}, fmt.Errorf("%w: unknown attempt %s", ErrNotFound, attemptID)
	}
	if err != nil {
		return researchcontract.Reconciliation{}, err
	}
	stamp := updated.String
	if finished.Valid && finished.String != "" {
		stamp = finished.String
	}
	switch state {
	case AttemptSucceeded, AttemptFailed, AttemptObservedSuccess, AttemptObservedFailure, AttemptCancelled:
		recorded, err := ParseAuthorityTime(stamp)
		if err != nil {
			return researchcontract.Reconciliation{}, err
		}
		return researchcontract.Reconciliation{AttemptID: attemptID, State: string(state), RecordedAt: recorded}, nil
	}
	var checkFinished sql.NullString
	err = r.QueryRowContext(ctx, `SELECT finished_at FROM round_reconciliation_checks
  WHERE attempt_id=? AND state='unknown' ORDER BY finished_at DESC LIMIT 1`, attemptID).Scan(&checkFinished)
	if err == nil {
		recorded, err := ParseAuthorityTime(checkFinished.String)
		if err != nil {
			return researchcontract.Reconciliation{}, err
		}
		return researchcontract.Reconciliation{AttemptID: attemptID, State: ReconciledUncertain, RecordedAt: recorded}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return researchcontract.Reconciliation{}, err
	}
	return researchcontract.Reconciliation{}, fmt.Errorf("%w: attempt %s in state %s has no reconciliation record",
		ErrNotFound, attemptID, state)
}

// AbandonedPreClaimCode marks an attempt unwound by
// AbandonDispatchedRoundAttempt: the executor certified a pre-claim refusal,
// so no external work started and the hold refunded.
const AbandonedPreClaimCode = "pre_claim_refused"

// ReleaseRoundAttempt drops a pre-dispatch reservation and refunds its
// allowance. Only state='reserved' releases; dispatch and release are mutually
// exclusive through the state predicate, so a racing dispatch fails fenced.
// Release is allowed in any round state: it unwinds a hold, never new work,
// so cleanup after Stop still refunds.
func (s *Store) ReleaseRoundAttempt(ctx context.Context, actor Actor, attemptID string) error {
	if !requiredActor(actor) || attemptID == "" {
		return ErrInvalid
	}
	var roundID string
	if err := s.db.QueryRowContext(ctx, `SELECT round_id FROM round_attempts WHERE id=?`,
		attemptID).Scan(&roundID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	tx, _, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state RoundAttemptState
	var cost RoundAllowance
	if err := tx.QueryRowContext(ctx, `SELECT state,requests_reserved,items_reserved,tools_reserved,turns_reserved
  FROM round_attempts WHERE id=? AND round_id=?`, attemptID, roundID).Scan(
		&state, &cost.Requests, &cost.Items, &cost.Tools, &cost.Turns); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != AttemptReserved {
		return ErrFenced
	}
	now := utcNow()
	res, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='cancelled',error_code='released',
  finished_at=?,updated_at=? WHERE id=? AND round_id=? AND state='reserved'`,
		now, now, attemptID, roundID)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used-?,
  items_used=items_used-?,tools_used=tools_used-?,turns_used=turns_used-?,
  revision=revision+1,updated_at=? WHERE id=?`,
		cost.Requests, cost.Items, cost.Tools, cost.Turns, now, roundID); err != nil {
		return err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.release", roundID); err != nil {
		return err
	}
	return tx.Commit()
}

// AbandonDispatchedRoundAttempt unwinds a dispatched attempt whose executor
// certified a pre-claim refusal (T24 F2/F3): no external work started and no
// lease is held, so the hold refunds exactly like a never-dispatched
// reservation and the attempt lands cancelled with AbandonedPreClaimCode. Only
// state='dispatched' abandons; a racing Stop fence (uncertain) or settle fails
// fenced and the caller must take the stop/reconcile path instead. Allowed in
// any round state like Release: it unwinds a hold, never new work.
//
// Call ONLY on a certified pre-claim refusal (researchexecute.RefusedPreClaim):
// abandoning an attempt whose flight may have started under-charges real work.
func (s *Store) AbandonDispatchedRoundAttempt(ctx context.Context, actor Actor, roundID, attemptID string) error {
	if !requiredActor(actor) || roundID == "" || attemptID == "" {
		return ErrInvalid
	}
	var found string
	if err := s.db.QueryRowContext(ctx, `SELECT round_id FROM round_attempts WHERE id=? AND round_id=?`,
		attemptID, roundID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	tx, _, err := s.roundWriter(ctx, roundID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state RoundAttemptState
	var cost RoundAllowance
	if err := tx.QueryRowContext(ctx, `SELECT state,requests_reserved,items_reserved,tools_reserved,turns_reserved
  FROM round_attempts WHERE id=? AND round_id=?`, attemptID, roundID).Scan(
		&state, &cost.Requests, &cost.Items, &cost.Tools, &cost.Turns); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != AttemptDispatched {
		return ErrFenced
	}
	now := utcNow()
	res, err := tx.ExecContext(ctx, `UPDATE round_attempts SET state='cancelled',error_code=?,
  finished_at=?,updated_at=? WHERE id=? AND round_id=? AND state='dispatched'`,
		AbandonedPreClaimCode, now, now, attemptID, roundID)
	if err != nil {
		return err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rounds SET requests_used=requests_used-?,
  items_used=items_used-?,tools_used=tools_used-?,turns_used=turns_used-?,
  revision=revision+1,updated_at=? WHERE id=?`,
		cost.Requests, cost.Items, cost.Tools, cost.Turns, now, roundID); err != nil {
		return err
	}
	if err := writeRoundAudit(ctx, tx, actor, "round.abandon_pre_claim", roundID); err != nil {
		return err
	}
	return tx.Commit()
}
