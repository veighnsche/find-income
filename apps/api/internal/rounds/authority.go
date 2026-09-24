// Authority implements researchcontract.Authority over the round ledger.
//
// Commissioning convention: a research run lists the research.* operations
// plus the fixed resource "research" (store.ResearchAuthorityResource) in its
// scope, and delegates its agent. Reservations bind (idempotencyKey,
// payloadHash) with no credential material anywhere in the replay identity,
// so credential rotation can never turn a legitimate replay into a new
// request or a conflict.
package rounds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
	"github.com/veighnsche/find-income-dashboard/api/internal/store"
)

var _ researchcontract.Authority = (*Authority)(nil)

// Authority binds one actor to run-authority decisions. The actor is the
// authenticated caller (owner administrator or delegated agent); permission
// checks test it against the round scope, and reservations charge under it.
type Authority struct {
	db    *store.Store
	actor store.Actor

	mu          sync.Mutex
	credentials map[string]int64
}

// NewAuthority constructs the Authority for one authenticated actor.
func NewAuthority(db *store.Store, actor store.Actor) (*Authority, error) {
	if db == nil {
		return nil, errors.New("rounds: authority store required")
	}
	if strings.TrimSpace(actor.Kind) == "" || strings.TrimSpace(actor.ID) == "" {
		return nil, errors.New("rounds: authority actor required")
	}
	return &Authority{db: db, actor: actor, credentials: map[string]int64{}}, nil
}

// RotateCredentials advances the advisory credential-version reference for a
// run and returns it. Rotation never affects replay identity: reservations
// bind no credential material, so same key + same payload still replays after
// any number of rotations. The version is operational state only; correctness
// never depends on it, so it is deliberately not part of the durable ledger.
func (a *Authority) RotateCredentials(runID string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.credentials[runID]++
	return a.credentials[runID]
}

// CredentialVersion returns the current advisory credential version for a run.
func (a *Authority) CredentialVersion(runID string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.credentials[runID]
}

// Check validates run/generation/liveness/deadline/permission/allowance and
// reports WHICH failed plus what remains allowed.
func (a *Authority) Check(ctx context.Context, in researchcontract.CheckInput) error {
	var checkErr error
	if err := a.db.Read(ctx, func(r store.Reader) error {
		_, checkErr = store.CheckRoundAuthorityTx(ctx, r, in.RunID, in.Generation, in.Permission, a.actor, in.Now)
		return nil
	}); err != nil {
		return err
	}
	return checkErr
}

// Reserve takes a pre-dispatch hold for a research.* operation or a dynamic
// Jev assessment (jevassess holds its pre-provider-call reservation here;
// saves charge in-transaction, which a network call cannot do). The replay
// identity is (idempotencyKey, payloadHash) carried on the attempt's request
// key and cursor slot with no bound capability, so same key + same payload
// replays the reservation without double-charging while same key + different
// payload conflicts.
func (a *Authority) Reserve(ctx context.Context, runID, operation, idempotencyKey, payloadHash string) (researchcontract.Reservation, error) {
	if operation != store.RoundJevAssess && !store.IsResearchOperation(operation) {
		if _, ok := store.RoundOperationCost(operation); !ok {
			return researchcontract.Reservation{}, researchcontract.NewError(
				researchcontract.OutcomeInvalid, "operation", "unknown operation "+operation)
		}
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "operation",
			"authority serves research.* operations only; "+operation+" reserves through the owning service")
	}
	if idempotencyKey == "" || len(idempotencyKey) > 200 || strings.TrimSpace(idempotencyKey) != idempotencyKey {
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "idempotencyKey", "idempotency key must be 1..200 chars without surrounding space")
	}
	if payloadHash == "" || len(payloadHash) > 256 {
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "payloadHash", "payload hash must be 1..256 chars")
	}
	cost, _ := store.RoundOperationCost(operation)
	attempt, _, err := a.db.ReserveRoundAttempt(ctx, a.actor, runID, store.RoundAttemptInput{
		RequestKey: idempotencyKey, Operation: operation,
		ResourceID: store.ResearchAuthorityResource, Cost: cost, CursorAttemptID: payloadHash,
	})
	if err == nil {
		return toReservation(attempt, idempotencyKey, payloadHash), nil
	}
	switch {
	case errors.Is(err, store.ErrRoundIdempotencyConflict):
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeConflict, "idempotencyKey",
			fmt.Sprintf("key %q already reserved with a different payload or operation", idempotencyKey))
	case errors.Is(err, store.ErrAllowance):
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeBudgetExhausted, string(researchcontract.AuthorityAllowance),
			fmt.Sprintf("allowance exhausted for %s (%s)", operation, a.remainingDetail(ctx, runID)))
	case errors.Is(err, store.ErrUncertain):
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeUncertain, "idempotencyKey",
			fmt.Sprintf("key %q is held by an uncertain dispatch; reconcile it before re-reserving", idempotencyKey))
	case errors.Is(err, store.ErrInvalid):
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeInvalid, "operation", "invalid reservation: "+err.Error())
	case errors.Is(err, store.ErrNotFound):
		return researchcontract.Reservation{}, researchcontract.NewError(
			researchcontract.OutcomeNotFound, string(researchcontract.AuthorityRun), "unknown run "+runID)
	case errors.Is(err, store.ErrFenced):
		return researchcontract.Reservation{}, a.fencedReserveError(ctx, runID, idempotencyKey, operation)
	default:
		return researchcontract.Reservation{}, err
	}
}

func (a *Authority) remainingDetail(ctx context.Context, runID string) string {
	ledger, err := a.Usage(ctx, runID)
	if err != nil {
		return "remaining unknown"
	}
	rem := researchcontract.Allowance{
		Requests: ledger.Enforced.Requests - ledger.Reserved.Requests - ledger.Observed.Requests,
		Items:    ledger.Enforced.Items - ledger.Reserved.Items - ledger.Observed.Items,
		Tools:    ledger.Enforced.Tools - ledger.Reserved.Tools - ledger.Observed.Tools,
		Turns:    ledger.Enforced.Turns - ledger.Reserved.Turns - ledger.Observed.Turns,
	}
	return fmt.Sprintf("remaining requests=%d items=%d tools=%d turns=%d",
		rem.Requests, rem.Items, rem.Tools, rem.Turns)
}

// fencedReserveError disambiguates a store fence into the contract outcome
// that actually applies: stopped round, stale reservation generation, or
// scope/delegation denial.
func (a *Authority) fencedReserveError(ctx context.Context, runID, idempotencyKey, operation string) error {
	round, err := a.db.Round(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return researchcontract.NewError(researchcontract.OutcomeNotFound,
			string(researchcontract.AuthorityRun), "unknown run "+runID)
	}
	if err != nil {
		return fmt.Errorf("reserve fenced: %w", store.ErrFenced)
	}
	if round.State != store.RoundRunning {
		return researchcontract.NewError(researchcontract.OutcomeStopped,
			string(researchcontract.AuthorityGeneration),
			fmt.Sprintf("round %s at generation %d; new reservations fenced", round.State, round.Generation))
	}
	if existing, err := a.db.RoundAttemptForRequest(ctx, runID, idempotencyKey); err == nil &&
		existing.Generation != round.Generation {
		return researchcontract.NewError(researchcontract.OutcomeStale,
			string(researchcontract.AuthorityGeneration),
			fmt.Sprintf("key %q reserved under generation %d, current is %d",
				idempotencyKey, existing.Generation, round.Generation))
	}
	return researchcontract.NewError(researchcontract.OutcomeForbidden,
		string(researchcontract.AuthorityPermission),
		fmt.Sprintf("%s %q may not reserve %s on this run: scope or delegation denies it",
			a.actor.Kind, a.actor.ID, operation))
}

func toReservation(attempt store.RoundAttempt, idempotencyKey, payloadHash string) researchcontract.Reservation {
	reservedAt, err := store.ParseAuthorityTime(attempt.CreatedAt)
	if err != nil {
		reservedAt = time.Now().UTC()
	}
	return researchcontract.Reservation{
		ID: attempt.ID, AttemptID: attempt.ID, Operation: attempt.Operation,
		RunID: attempt.RoundID, Generation: attempt.Generation,
		IdempotencyKey: idempotencyKey, PayloadHash: payloadHash, ReservedAt: reservedAt,
	}
}

// Release drops a pre-dispatch reservation and refunds its allowance.
func (a *Authority) Release(ctx context.Context, reservationID string) error {
	if reservationID == "" {
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "reservation", "reservation id required")
	}
	err := a.db.ReleaseRoundAttempt(ctx, a.actor, reservationID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return researchcontract.NewError(researchcontract.OutcomeNotFound, "reservation",
			"unknown reservation "+reservationID)
	case errors.Is(err, store.ErrFenced):
		state := "settled"
		if attempt, aerr := a.db.RoundAttempt(ctx, reservationID); aerr == nil {
			state = string(attempt.State)
		}
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "reservation",
			fmt.Sprintf("reservation already %s; only reserved (never dispatched) reservations release", state))
	case errors.Is(err, store.ErrInvalid):
		return researchcontract.NewError(researchcontract.OutcomeInvalid, "reservation", "invalid release: "+err.Error())
	default:
		return err
	}
}

// Usage returns the current enforced/reserved/observed/unknown ledger.
func (a *Authority) Usage(ctx context.Context, runID string) (researchcontract.UsageLedger, error) {
	var ledger researchcontract.UsageLedger
	var ledgerErr error
	if err := a.db.Read(ctx, func(r store.Reader) error {
		ledger, ledgerErr = store.RoundAuthorityLedgerTx(ctx, r, runID)
		return nil
	}); err != nil {
		return researchcontract.UsageLedger{}, err
	}
	if errors.Is(ledgerErr, store.ErrNotFound) {
		return researchcontract.UsageLedger{}, researchcontract.NewError(
			researchcontract.OutcomeNotFound, string(researchcontract.AuthorityRun), "unknown run "+runID)
	}
	if ledgerErr != nil {
		return researchcontract.UsageLedger{}, ledgerErr
	}
	return ledger, nil
}

// ReconciliationFor returns how an attempt was settled, or OutcomeNotFound
// when it was never reconciled.
func (a *Authority) ReconciliationFor(ctx context.Context, attemptID string) (researchcontract.Reconciliation, error) {
	var rec researchcontract.Reconciliation
	var recErr error
	if err := a.db.Read(ctx, func(r store.Reader) error {
		rec, recErr = store.RoundReconciliationTx(ctx, r, attemptID)
		return nil
	}); err != nil {
		return researchcontract.Reconciliation{}, err
	}
	if errors.Is(recErr, store.ErrNotFound) {
		return researchcontract.Reconciliation{}, researchcontract.NewError(
			researchcontract.OutcomeNotFound, "attempt", recErr.Error())
	}
	if recErr != nil {
		return researchcontract.Reconciliation{}, recErr
	}
	return rec, nil
}
