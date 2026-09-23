package fit

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func int64ptr(n int64) *int64 { return &n }

func monthly(min int64, max *int64, hours *int64) Compensation {
	return Compensation{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base,
		MinCents: int64ptr(min), MaxCents: max, ReferenceHours: hours}
}

func TestSalaryBoundaries(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		name      string
		input     Compensation
		state     State
		low, high int64
		estimate  bool
		confirmed bool
	}{
		{"6000 at 40 remains unconfirmed", monthly(600000, nil, int64ptr(40)), Unknown, 480000, 480000, true, false},
		{"5000 at 40 is below floor", monthly(500000, nil, int64ptr(40)), Mismatch, 400000, 400000, true, false},
		{"range crosses floor", monthly(500000, int64ptr(700000), int64ptr(40)), Unknown, 400000, 560000, true, false},
		{"range above floor needs actual offer", monthly(600000, int64ptr(700000), int64ptr(40)), Unknown, 480000, 560000, true, false},
		{"missing reference hours", monthly(600000, nil, nil), Unknown, 0, 0, false, false},
		{"unknown pay basis", Compensation{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: UnknownBasis, MinCents: int64ptr(600000), ReferenceHours: int64ptr(40)}, Unknown, 0, 0, false, false},
		{"annual basis", Compensation{Kind: Employment, Currency: "EUR", Period: Annual, Basis: Base, MinCents: int64ptr(7200000), ReferenceHours: int64ptr(40)}, Unknown, 0, 0, false, false},
		{"non EUR", Compensation{Kind: Employment, Currency: "USD", Period: Monthly, Basis: Base, MinCents: int64ptr(600000), ReferenceHours: int64ptr(40)}, Unknown, 0, 0, false, false},
		{"contract revenue separate", Compensation{Kind: Project, Currency: "EUR", Period: Hourly, Basis: Base, MinCents: int64ptr(10000), ReferenceHours: int64ptr(32)}, Unknown, 0, 0, false, false},
		{"exact confirmed 4500 at 32", Compensation{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base, ActualMonthlyBaseCents: int64ptr(450000), ActualHours: int64ptr(32), ActualConfirmed: true, ActualSource: Employer}, Match, 0, 0, false, true},
		{"confirmed actual below floor", Compensation{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base, ActualMonthlyBaseCents: int64ptr(449999), ActualHours: int64ptr(32), ActualConfirmed: true, ActualSource: Recruiter}, Mismatch, 0, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvaluateSalary(p, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.state || got.ConfirmedActual != tc.confirmed {
				t.Fatalf("state=%+v", got)
			}
			if (got.Estimate != nil) != tc.estimate {
				t.Fatalf("estimate=%+v", got.Estimate)
			}
			if tc.estimate && (got.Estimate.MinDisplayCents != tc.low || got.Estimate.MaxDisplayCents != tc.high) {
				t.Fatalf("estimate=%+v", got.Estimate)
			}
		})
	}
}

func TestProrationRoundingAndInvalidInput(t *testing.T) {
	p := DefaultPolicy()
	p.TargetHours = 1
	got, err := EvaluateSalary(p, monthly(1, nil, int64ptr(2)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Estimate == nil || got.Estimate.MinDisplayCents != 1 {
		t.Fatalf("half-cent rounding: %+v", got)
	}
	// Display rounds to the floor, but the exact 449999.5-cent value is below it.
	p = DefaultPolicy()
	p.TargetHours = 20
	got, err = EvaluateSalary(p, monthly(899999, nil, int64ptr(40)))
	if err != nil || got.State != Mismatch || got.Estimate == nil || got.Estimate.MinDisplayCents != 450000 {
		t.Fatalf("rounded display affected qualification: %+v %v", got, err)
	}
	bad := []Compensation{
		monthly(-1, nil, int64ptr(40)),
		monthly(700000, int64ptr(500000), int64ptr(40)),
		monthly(500000, nil, int64ptr(0)),
		{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base, ActualConfirmed: true},
		{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base, ActualMonthlyBaseCents: int64ptr(450000), ActualHours: int64ptr(32), ActualConfirmed: true, ActualSource: PublishedVacancy},
		monthly(math.MaxInt64, nil, int64ptr(1)),
	}
	for i, c := range bad {
		if _, err := EvaluateSalary(DefaultPolicy(), c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad input %d: %v", i, err)
		}
	}
}

func TestResponsibilityEvidenceAndOverall(t *testing.T) {
	confirmed := CriterionEvidence{Finding: ConfirmedMatch, Authority: Employer}
	facts := Criteria{BackendPlatform: confirmed, NoFrontendDuties: confirmed,
		NoPHPFocusedDuties: confirmed, HoursAvailable: confirmed, LocationWorkable: confirmed}
	pay := Compensation{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base,
		ActualMonthlyBaseCents: int64ptr(450000), ActualHours: int64ptr(32), ActualConfirmed: true, ActualSource: Employer}
	report, err := Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Qualified {
		t.Fatalf("qualified: %+v %v", report, err)
	}

	facts.NoFrontendDuties = CriterionEvidence{Finding: ConfirmedMismatch, Authority: Employer}
	report, err = Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Unsuitable {
		t.Fatalf("assigned frontend duty: %+v %v", report, err)
	}
	facts.NoFrontendDuties = CriterionEvidence{Finding: MentionOnly, Authority: PublishedVacancy}
	report, err = Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Unresolved {
		t.Fatalf("incidental frontend mention: %+v %v", report, err)
	}
	if !strings.Contains(report.Criteria[1].Reason, "mention") {
		t.Fatalf("reason: %s", report.Criteria[1].Reason)
	}

	facts.NoFrontendDuties = confirmed
	facts.NoPHPFocusedDuties = CriterionEvidence{Finding: ConfirmedMismatch, Authority: Employer}
	report, err = Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Unsuitable {
		t.Fatalf("PHP obligation: %+v %v", report, err)
	}
	facts.NoPHPFocusedDuties = CriterionEvidence{Finding: ConfirmedMatch, Authority: AgentInference}
	report, err = Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Unresolved {
		t.Fatalf("agent inference: %+v %v", report, err)
	}
	facts.NoPHPFocusedDuties = CriterionEvidence{Finding: Conflicting, Authority: Recruiter}
	report, err = Evaluate(DefaultPolicy(), pay, facts, true)
	if err != nil || report.Overall != NeedsRequalification {
		t.Fatalf("new conflict: %+v %v", report, err)
	}
	facts.NoPHPFocusedDuties = confirmed
	facts.LocationWorkable = CriterionEvidence{Finding: ConfirmedMatch, Authority: OwnerVerified}
	report, err = Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Qualified {
		t.Fatalf("owner-confirmed workable location: %+v %v", report, err)
	}

	// Favorable vacancy copy cannot alone qualify a role, even when every
	// advertised criterion looks suitable and actual pay is separately confirmed.
	published := CriterionEvidence{Finding: ConfirmedMatch, Authority: PublishedVacancy}
	facts = Criteria{BackendPlatform: published, NoFrontendDuties: published,
		NoPHPFocusedDuties: published, HoursAvailable: published, LocationWorkable: published}
	report, err = Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Unresolved {
		t.Fatalf("favorable vacancy copy became qualified: %+v %v", report, err)
	}
	facts.NoFrontendDuties = CriterionEvidence{Finding: ConfirmedMismatch, Authority: PublishedVacancy}
	report, err = Evaluate(DefaultPolicy(), pay, facts, false)
	if err != nil || report.Overall != Unsuitable || report.Criteria[1].State != Mismatch {
		t.Fatalf("published explicit frontend duty: %+v %v", report, err)
	}
}

func TestSourcedActualPayIndependentOfAdvertisementAndConflict(t *testing.T) {
	policy := DefaultPolicy()
	advertised := Compensation{Kind: Employment, Currency: "USD", Period: Annual,
		Basis: UnknownBasis, MinCents: int64ptr(10000000),
		SourcedActual: &ActualPay{Currency: "EUR", Period: Monthly, Basis: Base,
			AmountCents: 450000, WeeklyHours: 32}}
	result, err := EvaluateSalary(policy, advertised)
	if err != nil || result.State != Match || !result.ConfirmedActual {
		t.Fatalf("direct EUR base masked by advertisement: %+v %v", result, err)
	}
	advertised.SourcedActual.WeeklyHours = 40
	result, err = EvaluateSalary(policy, advertised)
	if err != nil || result.State != Unknown || result.ConfirmedActual {
		t.Fatalf("wrong actual hours qualified: %+v %v", result, err)
	}
	advertised.SourcedActual.WeeklyHours = 32
	advertised.ActualPayConflicting = true
	result, err = EvaluateSalary(policy, advertised)
	if err != nil || result.State != Unknown || !result.Conflicting || result.Estimate != nil {
		t.Fatalf("conflicting direct pay chose a value: %+v %v", result, err)
	}
	confirmed := CriterionEvidence{Finding: ConfirmedMatch, Authority: Employer}
	facts := Criteria{confirmed, confirmed, confirmed, confirmed, confirmed}
	report, err := Evaluate(policy, advertised, facts, true)
	if err != nil || report.Overall != NeedsRequalification {
		t.Fatalf("prior qualified conflict: %+v %v", report, err)
	}
}

func TestMaterialAmbiguityNeedsRequalification(t *testing.T) {
	confirmed := CriterionEvidence{Finding: ConfirmedMatch, Authority: Employer}
	facts := Criteria{BackendPlatform: confirmed, NoFrontendDuties: confirmed,
		NoPHPFocusedDuties: confirmed, HoursAvailable: confirmed, LocationWorkable: confirmed}
	facts.NoFrontendDuties = CriterionEvidence{Finding: Ambiguous, Authority: UserInference}
	pay := Compensation{Kind: Employment, Currency: "unknown", Period: UnknownPeriod, Basis: UnknownBasis,
		SourcedActual: &ActualPay{Currency: "EUR", Period: Monthly, Basis: Base, AmountCents: 450000, WeeklyHours: 32}}
	report, err := Evaluate(DefaultPolicy(), pay, facts, true)
	if err != nil || report.Overall != NeedsRequalification || !report.Criteria[1].Conflicting ||
		report.Criteria[1].State != Unknown {
		t.Fatalf("material ambiguity after qualification: %+v %v", report, err)
	}
	facts.NoFrontendDuties = CriterionEvidence{Finding: ConfirmedMismatch, Authority: PublishedVacancy}
	report, err = Evaluate(DefaultPolicy(), pay, facts, true)
	if err != nil || report.Overall != Unsuitable {
		t.Fatalf("explicit exclusion hidden by prior qualification: %+v %v", report, err)
	}
}
