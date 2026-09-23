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

// CriterionEvidence is the aggregate of reviewed, quoted claims. For role
// criteria ConfirmedMatch means explicit relevant presence, and
// ConfirmedMismatch means explicit absence, independent of preference mode.
type CriterionEvidence struct {
	Finding     Finding
	Authority   Authority
	EvidenceIDs []string
}

type Criteria struct {
	Roles            []RoleFact
	HoursAvailable   CriterionEvidence
	LocationWorkable CriterionEvidence
}

type RoleDefinition struct {
	ID, Label, Description, Kind, Mode string
}

type RoleFact struct {
	Definition RoleDefinition
	Evidence   CriterionEvidence
}

type Policy struct {
	TargetHoursHundredths int64
	MinMonthlyBaseCents   int64
	SalaryCurrency        string
	PreferredLocation     string
	RoleCriteria          []RoleDefinition
}

func (p Policy) validate() error {
	if p.TargetHoursHundredths < 100 || p.TargetHoursHundredths > 16800 ||
		p.MinMonthlyBaseCents < 0 || !twoDecimalCurrency(p.SalaryCurrency) {
		return fmt.Errorf("%w: unsupported policy hours or salary basis", ErrInvalid)
	}
	return nil
}

func twoDecimalCurrency(value string) bool {
	switch value {
	case "EUR", "GBP", "USD", "CAD", "AUD", "CHF", "NZD":
		return true
	}
	return false
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

// Amounts use 1/100 currency units for the supported two-decimal currencies.
// An advertised estimate never substitutes for directly sourced actual pay.
type Compensation struct {
	Kind                     OpportunityKind
	Currency                 string
	Period                   PayPeriod
	Basis                    PayBasis
	MinCents                 *int64
	MaxCents                 *int64
	ReferenceHoursHundredths *int64
	AnnualConversion         string
	// SourcedActual is a distinct confirmed offer. The repository validates
	// its exact source before constructing this pure-rule input.
	SourcedActual        *ActualPay
	ActualPayConflicting bool
}

type ActualPay struct {
	Currency              string
	Period                PayPeriod
	Basis                 PayBasis
	AmountCents           int64
	WeeklyHoursHundredths int64
	AnnualConversion      string
	Source                Authority
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
	if c.ReferenceHoursHundredths != nil && (*c.ReferenceHoursHundredths < 100 ||
		*c.ReferenceHoursHundredths > 16800) {
		return fmt.Errorf("%w: invalid reference hours", ErrInvalid)
	}
	if c.SourcedActual != nil && (c.SourcedActual.AmountCents < 0 ||
		c.SourcedActual.WeeklyHoursHundredths < 100 ||
		c.SourcedActual.WeeklyHoursHundredths > 16800 ||
		(c.SourcedActual.Source != Employer && c.SourcedActual.Source != Recruiter)) {
		return fmt.Errorf("%w: invalid sourced actual pay", ErrInvalid)
	}
	if c.AnnualConversion != "" && c.AnnualConversion != "twelve_equal_monthly_base_payments" ||
		c.SourcedActual != nil && c.SourcedActual.AnnualConversion != "" &&
			c.SourcedActual.AnnualConversion != "twelve_equal_monthly_base_payments" {
		return fmt.Errorf("%w: invalid annual conversion", ErrInvalid)
	}
	return nil
}

func (c Compensation) effectiveReferenceHours() int64 {
	if c.ReferenceHoursHundredths != nil {
		return *c.ReferenceHoursHundredths
	}
	return 0
}

type Estimate struct {
	MinDisplayCents          int64
	MaxDisplayCents          int64
	TargetHoursHundredths    int64
	ReferenceHoursHundredths int64
	Currency                 string
	AnnualConversion         string
	// Display values are rounded half up to cents; qualification comparisons
	// use the unrounded rational amounts.
}

type SalaryResult struct {
	State                 State
	Reason                string
	Estimate              *Estimate
	ConfirmedActual       bool
	Conflicting           bool
	Applicable            bool
	Concern               bool
	Currency              string
	TargetHoursHundredths int64
	SourceBasis           string
	AnnualConversion      string
}

func EvaluateSalary(policy Policy, c Compensation) (SalaryResult, error) {
	if err := policy.validate(); err != nil {
		return SalaryResult{}, err
	}
	if err := c.validate(); err != nil {
		return SalaryResult{}, err
	}
	result := SalaryResult{State: Unknown, Currency: policy.SalaryCurrency,
		TargetHoursHundredths: policy.TargetHoursHundredths, Applicable: c.Kind == Employment}
	if c.Kind == Project {
		result.Reason = "Employee monthly base salary does not apply; project economics are unassessed."
		return result, nil
	}
	if c.ActualPayConflicting {
		result.Reason = "Different current direct salary terms need clarification."
		result.Conflicting = true
		return result, nil
	}
	if actual := c.SourcedActual; actual != nil {
		result.SourceBasis = string(actual.Source)
		result.AnnualConversion = actual.AnnualConversion
		if actual.Currency != policy.SalaryCurrency || actual.Basis != Base {
			result.Reason = "Actual offer currency or gross base basis does not match the configured monthly base objective."
			return result, nil
		}
		if actual.WeeklyHoursHundredths != policy.TargetHoursHundredths {
			result.Reason = "Actual offer is for different weekly hours; target-hours base pay needs a direct source."
			return result, nil
		}
		monthly := new(big.Rat).SetInt64(actual.AmountCents)
		if actual.Period == Annual && actual.AnnualConversion == "twelve_equal_monthly_base_payments" {
			monthly.Quo(monthly, new(big.Rat).SetInt64(12))
		} else if actual.Period != Monthly {
			result.Reason = "Actual pay period lacks an explicit monthly base conversion."
			return result, nil
		}
		result.ConfirmedActual = true
		if monthly.Cmp(new(big.Rat).SetInt64(policy.MinMonthlyBaseCents)) >= 0 {
			result.State = Match
			result.Reason = "Directly sourced actual gross monthly base meets the configured floor."
		} else {
			result.State = Mismatch
			result.Reason = "Directly sourced actual gross monthly base is below the configured floor."
		}
		return result, nil
	}
	if c.Currency != policy.SalaryCurrency || c.Basis != Base {
		result.Reason = "Advertised currency or gross base basis is not established for this objective."
		return result, nil
	}
	divisor := int64(1)
	if c.Period == Annual && c.AnnualConversion == "twelve_equal_monthly_base_payments" {
		divisor = 12
	} else if c.Period != Monthly {
		result.Reason = "Advertised pay period lacks an explicit monthly base conversion."
		return result, nil
	}
	referenceHours := c.effectiveReferenceHours()
	if c.MinCents == nil || referenceHours < 100 || referenceHours > 16800 {
		result.Reason = "Advertised base amount or reference hours are missing."
		return result, nil
	}
	max := c.MinCents
	if c.MaxCents != nil {
		max = c.MaxCents
	}
	low, lowDisplay, err := prorate(*c.MinCents, policy.TargetHoursHundredths, referenceHours, divisor)
	if err != nil {
		return SalaryResult{}, err
	}
	high, highDisplay, err := prorate(*max, policy.TargetHoursHundredths, referenceHours, divisor)
	if err != nil {
		return SalaryResult{}, err
	}
	result.Estimate = &Estimate{MinDisplayCents: lowDisplay, MaxDisplayCents: highDisplay,
		TargetHoursHundredths: policy.TargetHoursHundredths, ReferenceHoursHundredths: referenceHours,
		Currency: policy.SalaryCurrency, AnnualConversion: c.AnnualConversion}
	result.SourceBasis = string(PublishedVacancy)
	result.AnnualConversion = c.AnnualConversion
	result.Concern = high.Cmp(new(big.Rat).SetInt64(policy.MinMonthlyBaseCents)) < 0
	if result.Concern {
		result.Reason = "Advertised target-hours estimate is below the floor; actual offer terms remain unconfirmed."
	} else if low.Cmp(new(big.Rat).SetInt64(policy.MinMonthlyBaseCents)) < 0 {
		result.Reason = "Advertised range crosses the floor; actual offer terms remain unconfirmed."
	} else {
		result.Reason = "Advertised estimate reaches the floor; actual offer terms remain unconfirmed."
	}
	return result, nil
}

func prorate(cents, targetHours, referenceHours, periodDivisor int64) (*big.Rat, int64, error) {
	numerator := new(big.Int).Mul(big.NewInt(cents), big.NewInt(targetHours))
	denominator := new(big.Int).Mul(big.NewInt(referenceHours), big.NewInt(periodDivisor))
	exact := new(big.Rat).SetFrac(numerator, denominator)
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
	ID          string
	Label       string
	Description string
	Kind        string
	Mode        string
	State       State
	Reason      string
	Blocking    bool
	Relevant    bool
	SourceBasis string
	EvidenceIDs []string
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
	results := make([]CriterionResult, 0, len(facts.Roles)+3)
	for _, fact := range facts.Roles {
		results = append(results, evaluateRole(fact))
	}
	if compensation.Kind == Employment {
		results = append(results, evaluateFixedCriterion("target_hours_available", "Weekly hours available", facts.HoursAvailable, true))
	}
	results = append(results, evaluateFixedCriterion("location_workable", "Location and arrangement workable", facts.LocationWorkable, true))
	results = append(results, CriterionResult{Criterion: "monthly_base_salary", ID: "monthly_base_salary",
		Label: "Gross monthly base at target hours", Kind: "compensation", Mode: "require",
		State: salary.State, Reason: salary.Reason, Blocking: compensation.Kind == Employment,
		Relevant: salary.ConfirmedActual || salary.Estimate != nil, SourceBasis: salary.SourceBasis,
		Conflicting: salary.Conflicting})
	hasMismatch, hasUnknown, hasConflict := false, false, false
	for _, result := range results {
		if result.Blocking && result.State == Mismatch {
			hasMismatch = true
		}
		if result.Blocking && result.State == Unknown {
			hasUnknown = true
		}
		if result.Blocking && result.Conflicting {
			hasConflict = true
		}
	}
	overall := Qualified
	switch {
	case hasMismatch:
		overall = Unsuitable
	case previouslyQualified && hasConflict:
		overall = NeedsRequalification
	case hasUnknown || compensation.Kind == Project:
		overall = Unresolved
	}
	return Report{Overall: overall, Criteria: results, Salary: salary}, nil
}

func evaluateRole(fact RoleFact) CriterionResult {
	d := fact.Definition
	result := CriterionResult{Criterion: "role_criterion", ID: d.ID, Label: d.Label,
		Description: d.Description, Kind: d.Kind, Mode: d.Mode, State: Unknown,
		Blocking: d.Mode == "require", SourceBasis: string(fact.Evidence.Authority),
		EvidenceIDs: append([]string(nil), fact.Evidence.EvidenceIDs...)}
	if (fact.Evidence.Finding == ConfirmedMatch || fact.Evidence.Finding == ConfirmedMismatch) &&
		fact.Evidence.Authority != PublishedVacancy && fact.Evidence.Authority != Employer &&
		fact.Evidence.Authority != Recruiter {
		result.Reason = "An inference is a proposal, not a confirmed role fact."
		return result
	}
	switch fact.Evidence.Finding {
	case Conflicting, Ambiguous:
		result.Relevant, result.Conflicting = true, true
		result.Blocking = d.Mode != "prefer"
		result.Reason = "Relevant role evidence needs clarification."
	case MentionOnly:
		result.Reason = "Incidental mention does not establish an assigned role responsibility."
	case ConfirmedMatch:
		result.Relevant = true
		if d.Mode == "avoid" {
			result.State, result.Blocking = Mismatch, true
			result.Reason = "An explicit relevant responsibility conflicts with this avoid criterion."
		} else {
			result.State = Match
			result.Reason = "Explicit relevant presence is supported by a reviewed source."
		}
	case ConfirmedMismatch:
		result.Relevant = true
		if d.Mode == "avoid" {
			result.State = Match
			result.Reason = "A reviewed source explicitly says this responsibility is absent."
		} else {
			result.State = Mismatch
			result.Reason = "A reviewed source explicitly says this requirement is absent."
		}
	default:
		result.Reason = "No relevant reviewed role evidence is available."
	}
	return result
}

func evaluateFixedCriterion(name, label string, fact CriterionEvidence, blocking bool) CriterionResult {
	result := CriterionResult{Criterion: name, ID: name, Label: label, Kind: "working_terms",
		Mode: "require", State: Unknown, Blocking: blocking,
		SourceBasis: string(fact.Authority), EvidenceIDs: append([]string(nil), fact.EvidenceIDs...)}
	if fact.Finding == Conflicting {
		result.Relevant = true
		result.Conflicting = true
		result.Reason = "Sources conflict; the requirement needs clarification."
		return result
	}
	if fact.Finding == Ambiguous {
		result.Relevant = true
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
	if fact.Authority != PublishedVacancy && fact.Authority != Employer && fact.Authority != Recruiter &&
		!(name == "location_workable" && fact.Authority == OwnerVerified) {
		result.Reason = "An inference cannot confirm an employer requirement."
		return result
	}
	result.Relevant = true
	if fact.Finding == ConfirmedMatch {
		result.State = Match
		result.Reason = "This working term is supported by a reviewed source."
	} else {
		result.State = Mismatch
		result.Reason = "This working term conflicts with the configured objective."
	}
	return result
}
