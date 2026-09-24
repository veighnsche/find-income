package researchexecute

import (
	"errors"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// PreClaimRefusal marks an Execute error returned before any claim or dispatch
// (T24 F2/F3 fix): descriptor validation, the authority check, the authority
// reservation, or the memory claim refused, so nothing left this process, no
// lease is held, and no external work started. The supervisor releases its
// reserve hold on this signal instead of stranding the attempt
// dispatched/uncertain. Errors from dispatch, observation, lease release, or
// the fence (stopped/stale/uncertain, context cancellation) are never wrapped:
// those stay on the stop/reconcile path.
//
// The wrapper is transparent: Error reports the cause verbatim and Unwrap
// exposes it, so contract-error readers are unaffected.
type PreClaimRefusal struct {
	Err error
}

// Error reports the refusal cause verbatim.
func (e *PreClaimRefusal) Error() string {
	if e == nil || e.Err == nil {
		return "pre-claim refusal"
	}
	return e.Err.Error()
}

// Unwrap returns the refusal cause.
func (e *PreClaimRefusal) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RefusedPreClaim reports whether err carries a pre-claim refusal, returning
// the underlying cause for the caller to surface.
func RefusedPreClaim(err error) (error, bool) {
	var refusal *PreClaimRefusal
	if !errors.As(err, &refusal) || refusal == nil || refusal.Err == nil {
		return nil, false
	}
	return refusal.Err, true
}

// refusePreClaim wraps err as a pre-claim refusal when it is a terminal
// pre-dispatch contract refusal (invalid descriptor, exhausted budget,
// conflict, forbidden, unknown run). Fence and outcome errors (stopped,
// stale, uncertain), context errors, and non-contract failures pass through
// unwrapped: the fence owns those attempts and the stop/reconcile path
// settles them. Call only with errors from validation, authority check,
// authority reservation, or the memory claim — never past dispatch.
func refusePreClaim(err error) error {
	var cerr *researchcontract.Error
	if !errors.As(err, &cerr) {
		return err
	}
	switch cerr.Code {
	case researchcontract.OutcomeInvalid,
		researchcontract.OutcomeBudgetExhausted,
		researchcontract.OutcomeConflict,
		researchcontract.OutcomeForbidden,
		researchcontract.OutcomeNotFound:
		return &PreClaimRefusal{Err: err}
	}
	return err
}
