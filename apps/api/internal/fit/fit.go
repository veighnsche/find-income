// Package fit contains deterministic, provider-independent qualification rules.
// Evidence interpretation and provenance validation happen before these inputs
// are constructed; a title or a model suggestion is never confirmation.
package fit

import (
	"errors"
	"fmt"
	"math"
	"math/big"
)

var ErrInvalid = errors.New("invalid fit input")

type State string

const (
	Match    State = "match"
	Mismatch State = "mismatch"
	Unknown  State = "unknown"
)

type Finding string

const (
	ConfirmedMatch    Finding = "confirmed_match"
	ConfirmedMismatch Finding = "confirmed_mismatch"
	Missing           Finding = "missing"
	Conflicting       Finding = "conflicting"
	Ambiguous         Finding = "ambiguous"
	MentionOnly       Finding = "mention_only"
)

type Authority string

const (
	Employer         Authority = "employer"
	PublishedVacancy Authority = "published_vacancy"
	Recruiter        Authority = "recruiter"
	OwnerVerified    Authority = "owner_verified"
	UserInference    Authority = "user_inference"
	AgentInference   Authority = "agent_inference"
)

// CriterionEvidence describes a reviewed source claim about role terms.
// Published text can establish an explicit exclusion, but favorable claims
// require direct employer/recruiter confirmation before qualification.
type CriterionEvidence struct {
	Finding   Finding
	Authority Authority
}

type Criteria struct {
	BackendPlatform    CriterionEvidence
	NoFrontendDuties   CriterionEvidence
	NoPHPFocusedDuties CriterionEvidence
	HoursAvailable     CriterionEvidence
	LocationWorkable   CriterionEvidence
}

type Policy struct {
	TargetHours             int64
	MinMonthlyBaseCents     int64
	SalaryCurrency          string
	PreferredLocation       string
	RequireBackendPlatform  bool
	ExcludeFrontendDuties   bool
	ExcludePHPFocusedDuties bool
}

func DefaultPolicy() Policy {
	return Policy{TargetHours: 32, MinMonthlyBaseCents: 450000,
		SalaryCurrency: "EUR", PreferredLocation: "Amsterdam",
		RequireBackendPlatform: true, ExcludeFrontendDuties: true,
		ExcludePHPFocusedDuties: true}
}

func (p Policy) validate() error {
	if p.TargetHours < 1 || p.TargetHours > 168 || p.MinMonthlyBaseCents < 0 || p.SalaryCurrency != "EUR" {
		return fmt.Errorf("%w: unsupported policy hours or salary basis", ErrInvalid)
	}
	return nil
}

type OpportunityKind string

const (
	Employment OpportunityKind = "employment"
	Project    OpportunityKind = "project"
)

type PayPeriod string

const (
	Monthly       PayPeriod = "month"
	Annual        PayPeriod = "year"
	Hourly        PayPeriod = "hour"
	ProjectRate   PayPeriod = "project"
	UnknownPeriod PayPeriod = "unknown"
)

type PayBasis string

const (
	Base         PayBasis = "base"
	Inclusive    PayBasis = "inclusive"
	UnknownBasis PayBasis = "unknown"
)

// Money is in the smallest currency unit. MinCents is nil for unadvertised
// pay; MaxCents nil means a single advertised amount. ReferenceHours is the
// weekly schedule to which a monthly amount applies. ActualMonthlyBaseCents
// is a distinct confirmed offer, never filled by proration.
type Compensation struct {
	Kind                   OpportunityKind
	Currency               string
	Period                 PayPeriod
	Basis                  PayBasis
	MinCents               *int64
	MaxCents               *int64
	ReferenceHours         *int64
	ActualMonthlyBaseCents *int64
	ActualHours            *int64
	ActualConfirmed        bool
	ActualSource           Authority
	// SourcedActual is a distinct confirmed offer. The repository validates
	// its exact source before constructing this pure-rule input.
	SourcedActual        *ActualPay
	ActualPayConflicting bool
}

type ActualPay struct {
	Currency    string
	Period      PayPeriod
	Basis       PayBasis
	AmountCents int64
	WeeklyHours int64
}

func (c Compensation) validate() error {
	if c.Kind != Employment && c.Kind != Project {
		return fmt.Errorf("%w: unknown opportunity kind", ErrInvalid)
	}
	switch c.Period {
	case Monthly, Annual, Hourly, ProjectRate, UnknownPeriod:
	default:
		return fmt.Errorf("%w: unknown pay period", ErrInvalid)
	}
	switch c.Basis {
	case Base, Inclusive, UnknownBasis:
	default:
		return fmt.Errorf("%w: unknown pay basis", ErrInvalid)
	}
	if c.MinCents != nil && *c.MinCents < 0 || c.MaxCents != nil && (c.MinCents == nil || *c.MaxCents < *c.MinCents) {
		return fmt.Errorf("%w: malformed compensation range", ErrInvalid)
	}
	if c.ReferenceHours != nil && (*c.ReferenceHours < 1 || *c.ReferenceHours > 168) ||
		c.ActualHours != nil && (*c.ActualHours < 1 || *c.ActualHours > 168) ||
		c.ActualMonthlyBaseCents != nil && *c.ActualMonthlyBaseCents < 0 {
		return fmt.Errorf("%w: invalid hours or actual amount", ErrInvalid)
	}
	if c.ActualConfirmed && (c.ActualHours == nil || c.ActualMonthlyBaseCents == nil) {
		return fmt.Errorf("%w: confirmed actual pay needs amount and hours", ErrInvalid)
	}
	if c.ActualConfirmed && c.ActualSource != Employer && c.ActualSource != Recruiter {
		return fmt.Errorf("%w: confirmed actual pay needs a direct employer or recruiter source", ErrInvalid)
	}
	if (c.ActualHours == nil) != (c.ActualMonthlyBaseCents == nil) {
		return fmt.Errorf("%w: actual amount and hours must occur together", ErrInvalid)
	}
	if c.SourcedActual != nil && (c.SourcedActual.AmountCents < 0 ||
		c.SourcedActual.WeeklyHours < 1 || c.SourcedActual.WeeklyHours > 168) {
		return fmt.Errorf("%w: invalid sourced actual pay", ErrInvalid)
	}
	return nil
}

type Estimate struct {
	MinDisplayCents int64
	MaxDisplayCents int64
	TargetHours     int64
	ReferenceHours  int64
	// Display values are rounded half up to cents; qualification comparisons
	// use the unrounded rational amounts.
}

type SalaryResult struct {
	State           State
	Reason          string
	Estimate        *Estimate
	ConfirmedActual bool
	Conflicting     bool
}

func EvaluateSalary(policy Policy, c Compensation) (SalaryResult, error) {
	if err := policy.validate(); err != nil {
		return SalaryResult{}, err
	}
	if err := c.validate(); err != nil {
		return SalaryResult{}, err
	}
	if c.Kind == Project {
		return SalaryResult{State: Unknown, Reason: "Project revenue or rates are separate from employee base salary."}, nil
	}
	if c.ActualPayConflicting {
		return SalaryResult{State: Unknown, Reason: "Current direct salary statements conflict; actual base pay needs clarification.", Conflicting: true}, nil
	}
	if actual := c.SourcedActual; actual != nil {
		if actual.Currency != policy.SalaryCurrency || actual.Period != Monthly || actual.Basis != Base {
			return SalaryResult{State: Unknown, Reason: "The sourced actual offer is not established as EUR gross monthly base pay."}, nil
		}
		if actual.WeeklyHours != policy.TargetHours {
			return SalaryResult{State: Unknown, Reason: "The sourced actual offer is for different weekly hours; actual target-hours base pay needs confirmation."}, nil
		}
		if actual.AmountCents >= policy.MinMonthlyBaseCents {
			return SalaryResult{State: Match, Reason: "Employer-confirmed gross monthly base at the actual target hours meets the floor.", ConfirmedActual: true}, nil
		}
		return SalaryResult{State: Mismatch, Reason: "Employer-confirmed gross monthly base at the actual target hours is below the floor.", ConfirmedActual: true}, nil
	}
	if c.Currency != policy.SalaryCurrency || c.Basis != Base {
		return SalaryResult{State: Unknown, Reason: "EUR monthly base salary is not established; currency or pay basis needs clarification."}, nil
	}
	if c.ActualConfirmed && c.ActualHours != nil && *c.ActualHours == policy.TargetHours {
		if *c.ActualMonthlyBaseCents >= policy.MinMonthlyBaseCents {
			return SalaryResult{State: Match, Reason: "Employer-confirmed gross monthly base at the actual target hours meets the floor.", ConfirmedActual: true}, nil
		}
		return SalaryResult{State: Mismatch, Reason: "Employer-confirmed gross monthly base at the actual target hours is below the floor.", ConfirmedActual: true}, nil
	}
	if c.Period != Monthly || c.MinCents == nil || c.ReferenceHours == nil {
		return SalaryResult{State: Unknown, Reason: "Monthly base amount or its reference hours are missing; no 32-hour estimate is possible."}, nil
	}
	max := c.MinCents
	if c.MaxCents != nil {
		max = c.MaxCents
	}
	low, lowDisplay, err := prorate(*c.MinCents, policy.TargetHours, *c.ReferenceHours)
	if err != nil {
		return SalaryResult{}, err
	}
	high, highDisplay, err := prorate(*max, policy.TargetHours, *c.ReferenceHours)
	if err != nil {
		return SalaryResult{}, err
	}
	estimate := &Estimate{MinDisplayCents: lowDisplay, MaxDisplayCents: highDisplay,
		TargetHours: policy.TargetHours, ReferenceHours: *c.ReferenceHours}
	floor := new(big.Rat).SetInt64(policy.MinMonthlyBaseCents)
	if high.Cmp(floor) < 0 {
		return SalaryResult{State: Mismatch, Reason: "The known-hours monthly base estimate is entirely below the floor; the estimate is not an employer-confirmed offer.", Estimate: estimate}, nil
	}
	if low.Cmp(floor) < 0 {
		return SalaryResult{State: Unknown, Reason: "The advertised range crosses the floor; an achievable base at actual hours needs confirmation.", Estimate: estimate}, nil
	}
	return SalaryResult{State: Unknown, Reason: "The estimate reaches the floor, but actual-hours monthly base pay is not yet employer-confirmed.", Estimate: estimate}, nil
}

func prorate(cents, targetHours, referenceHours int64) (*big.Rat, int64, error) {
	numerator := new(big.Int).Mul(big.NewInt(cents), big.NewInt(targetHours))
	exact := new(big.Rat).SetFrac(numerator, big.NewInt(referenceHours))
	quotient, remainder := new(big.Int).QuoRem(exact.Num(), exact.Denom(), new(big.Int))
	if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(exact.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Int64() < 0 || quotient.Int64() > math.MaxInt64 {
		return nil, 0, fmt.Errorf("%w: prorated amount exceeds supported money range", ErrInvalid)
	}
	return exact, quotient.Int64(), nil
}

type CriterionResult struct {
	Criterion   string
	State       State
	Reason      string
	Conflicting bool
}

type OverallState string

const (
	Qualified            OverallState = "qualified"
	Unsuitable           OverallState = "unsuitable"
	Unresolved           OverallState = "unresolved"
	NeedsRequalification OverallState = "needs_requalification"
)

type Report struct {
	Overall  OverallState
	Criteria []CriterionResult
	Salary   SalaryResult
}

func Evaluate(policy Policy, compensation Compensation, facts Criteria, previouslyQualified bool) (Report, error) {
	salary, err := EvaluateSalary(policy, compensation)
	if err != nil {
		return Report{}, err
	}
	results := make([]CriterionResult, 0, 6)
	if policy.RequireBackendPlatform {
		results = append(results, evaluateCriterion("backend_platform", facts.BackendPlatform))
	}
	if policy.ExcludeFrontendDuties {
		results = append(results, evaluateCriterion("no_frontend_duties", facts.NoFrontendDuties))
	}
	if policy.ExcludePHPFocusedDuties {
		results = append(results, evaluateCriterion("no_php_focused_duties", facts.NoPHPFocusedDuties))
	}
	results = append(results, evaluateCriterion("target_hours_available", facts.HoursAvailable))
	results = append(results, evaluateCriterion("location_workable", facts.LocationWorkable))
	results = append(results, CriterionResult{Criterion: "monthly_base_salary", State: salary.State, Reason: salary.Reason,
		Conflicting: salary.Conflicting})
	hasMismatch, hasUnknown, hasConflict := false, false, false
	for _, result := range results {
		if result.State == Mismatch {
			hasMismatch = true
		}
		if result.State == Unknown {
			hasUnknown = true
		}
		if result.Conflicting {
			hasConflict = true
		}
	}
	overall := Qualified
	switch {
	case hasMismatch:
		overall = Unsuitable
	case previouslyQualified && hasConflict:
		overall = NeedsRequalification
	case hasUnknown:
		overall = Unresolved
	}
	return Report{Overall: overall, Criteria: results, Salary: salary}, nil
}

func evaluateCriterion(name string, fact CriterionEvidence) CriterionResult {
	result := CriterionResult{Criterion: name, State: Unknown}
	if fact.Finding == Conflicting {
		result.Conflicting = true
		result.Reason = "Sources conflict; the requirement needs clarification."
		return result
	}
	if fact.Finding == Ambiguous {
		result.Conflicting = true
		result.Reason = "Material ambiguity in an active source needs clarification."
		return result
	}
	if fact.Finding == MentionOnly {
		result.Reason = "A stack or team mention does not establish an assigned responsibility."
		return result
	}
	if fact.Finding != ConfirmedMatch && fact.Finding != ConfirmedMismatch {
		result.Reason = "The requirement has not been confirmed."
		return result
	}
	if fact.Authority == PublishedVacancy {
		if fact.Finding == ConfirmedMismatch {
			result.State = Mismatch
			result.Reason = "An explicit published role requirement conflicts with this criterion."
		} else {
			result.Reason = "A favorable published claim needs direct confirmation before qualification."
		}
		return result
	}
	if fact.Authority != Employer && fact.Authority != Recruiter &&
		!(name == "location_workable" && fact.Authority == OwnerVerified) {
		result.Reason = "An inference cannot confirm an employer requirement."
		return result
	}
	if fact.Finding == ConfirmedMatch {
		result.State = Match
		result.Reason = "The requirement is supported by a stated role term."
	} else {
		result.State = Mismatch
		result.Reason = "A stated role term conflicts with this requirement."
	}
	return result
}
