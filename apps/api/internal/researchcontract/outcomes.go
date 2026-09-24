package researchcontract

import "fmt"

// Outcome is a shared tool/operation result code (T06 §8). Non-ok outcomes
// carry the relevant saved references and the permitted next action.
type Outcome string

const (
	OutcomeOK                Outcome = "ok"
	OutcomeReused            Outcome = "reused"
	OutcomeClaimedElsewhere  Outcome = "claimed_elsewhere"
	OutcomeStale             Outcome = "stale"
	OutcomeRevisionConflict  Outcome = "revision_conflict"
	OutcomeIdentityAmbiguous Outcome = "identity_ambiguous"
	OutcomeCaptureIncomplete Outcome = "capture_incomplete"
	OutcomeBudgetExhausted   Outcome = "budget_exhausted"
	OutcomeStopped           Outcome = "stopped"
	OutcomeRateLimited       Outcome = "rate_limited"
	OutcomeUncertain         Outcome = "outcome_uncertain"
	OutcomeInvalid           Outcome = "invalid"
	OutcomeConflict          Outcome = "conflict"
	OutcomeForbidden         Outcome = "forbidden"
	OutcomeNotFound          Outcome = "not_found"
)

// Valid reports whether o is a known outcome code.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeOK, OutcomeReused, OutcomeClaimedElsewhere, OutcomeStale,
		OutcomeRevisionConflict, OutcomeIdentityAmbiguous, OutcomeCaptureIncomplete,
		OutcomeBudgetExhausted, OutcomeStopped, OutcomeRateLimited,
		OutcomeUncertain, OutcomeInvalid, OutcomeConflict, OutcomeForbidden,
		OutcomeNotFound:
		return true
	}
	return false
}

// AuthorityField names which authority check failed. There is no generic
// authority error: failures say which of run/generation/deadline/allowance/
// permission failed and what remains allowed.
type AuthorityField string

const (
	AuthorityRun        AuthorityField = "run"
	AuthorityGeneration AuthorityField = "generation"
	AuthorityDeadline   AuthorityField = "deadline"
	AuthorityAllowance  AuthorityField = "allowance"
	AuthorityPermission AuthorityField = "permission"
)

// Error is a typed contract error. Code is always a valid Outcome.
type Error struct {
	Code    Outcome
	Field   string // optional: offending input field or failed AuthorityField
	Detail  string // actionable: what failed and what remains allowed
	Wrapped error  // optional underlying cause, never a credential or secret
}

// Error implements error.
func (e *Error) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("%s(%s): %s", e.Code, e.Field, e.Detail)
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Wrapped }

// NewError builds a typed contract error; code must be a valid Outcome.
func NewError(code Outcome, field, detail string) *Error {
	return &Error{Code: code, Field: field, Detail: detail}
}
