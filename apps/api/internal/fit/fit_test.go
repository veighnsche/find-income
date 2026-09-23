package fit

import "testing"

func testPolicy() Policy {
	return Policy{TargetHoursHundredths: 3150, MinMonthlyBaseCents: 450000, SalaryCurrency: "EUR",
		RoleCriteria: []RoleDefinition{{ID: "backend", Label: "Backend", Kind: "responsibility", Mode: "require"},
			{ID: "frontend", Label: "Frontend", Kind: "responsibility", Mode: "avoid"}}}
}

func TestAdvertisedEstimateUsesExactFractionalHours(t *testing.T) {
	minimum, reference := int64(560000), int64(3950)
	result, err := EvaluateSalary(testPolicy(), Compensation{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base,
		MinCents: &minimum, ReferenceHoursHundredths: &reference})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != Unknown || !result.Concern || result.Estimate == nil ||
		result.Estimate.MinDisplayCents != 446582 || result.Estimate.TargetHoursHundredths != 3150 {
		t.Fatalf("fractional advertised estimate: %+v", result)
	}
}

func TestActualAnnualBaseNeedsExplicitConversion(t *testing.T) {
	policy := testPolicy()
	base := Compensation{Kind: Employment, Currency: "EUR", Period: UnknownPeriod, Basis: UnknownBasis}
	base.SourcedActual = &ActualPay{Currency: "EUR", Period: Annual, Basis: Base, AmountCents: 5400000,
		WeeklyHoursHundredths: 3150, Source: Employer}
	unknown, err := EvaluateSalary(policy, base)
	if err != nil || unknown.State != Unknown || unknown.ConfirmedActual {
		t.Fatalf("unsourced conversion: %+v %v", unknown, err)
	}
	base.SourcedActual.AnnualConversion = "twelve_equal_monthly_base_payments"
	match, err := EvaluateSalary(policy, base)
	if err != nil || match.State != Match || !match.ConfirmedActual {
		t.Fatalf("annual match: %+v %v", match, err)
	}
	base.SourcedActual.AmountCents--
	below, err := EvaluateSalary(policy, base)
	if err != nil || below.State != Mismatch {
		t.Fatalf("annual floor: %+v %v", below, err)
	}
}

func TestPolicyCurrencyAndProjectEconomics(t *testing.T) {
	policy := testPolicy()
	policy.SalaryCurrency = "GBP"
	result, err := EvaluateSalary(policy, Compensation{Kind: Employment, Currency: "GBP", Period: Monthly, Basis: Base,
		SourcedActual: &ActualPay{Currency: "GBP", Period: Monthly, Basis: Base, AmountCents: 450000,
			WeeklyHoursHundredths: 3150, Source: Recruiter}})
	if err != nil || result.State != Match {
		t.Fatalf("GBP actual: %+v %v", result, err)
	}
	project, err := EvaluateSalary(policy, Compensation{Kind: Project, Currency: "GBP", Period: ProjectRate, Basis: UnknownBasis})
	if err != nil || project.State != Unknown || project.Applicable {
		t.Fatalf("project economics: %+v %v", project, err)
	}
}

func TestDynamicRoleModesAndRelevance(t *testing.T) {
	policy := testPolicy()
	base := Compensation{Kind: Employment, Currency: "EUR", Period: Monthly, Basis: Base,
		SourcedActual: &ActualPay{Currency: "EUR", Period: Monthly, Basis: Base, AmountCents: 450000,
			WeeklyHoursHundredths: 3150, Source: Employer}}
	criteria := Criteria{Roles: []RoleFact{
		{Definition: policy.RoleCriteria[0], Evidence: CriterionEvidence{Finding: ConfirmedMatch, Authority: PublishedVacancy}},
		{Definition: policy.RoleCriteria[1], Evidence: CriterionEvidence{Finding: MentionOnly, Authority: PublishedVacancy}},
	}, HoursAvailable: CriterionEvidence{Finding: ConfirmedMatch, Authority: PublishedVacancy},
		LocationWorkable: CriterionEvidence{Finding: ConfirmedMatch, Authority: OwnerVerified}}
	report, err := Evaluate(policy, base, criteria, false)
	if err != nil || report.Overall != Qualified || report.Criteria[1].State != Unknown || report.Criteria[1].Blocking {
		t.Fatalf("incidental avoided mention: %+v %v", report, err)
	}
	criteria.Roles[1].Evidence = CriterionEvidence{Finding: ConfirmedMatch, Authority: Employer}
	report, err = Evaluate(policy, base, criteria, false)
	if err != nil || report.Overall != Unsuitable || report.Criteria[1].State != Mismatch {
		t.Fatalf("explicit avoided duty: %+v %v", report, err)
	}
	criteria.Roles[1].Evidence = CriterionEvidence{Finding: ConfirmedMismatch, Authority: Employer}
	report, err = Evaluate(policy, base, criteria, false)
	if err != nil || report.Overall != Qualified || report.Criteria[1].State != Match {
		t.Fatalf("explicit absence: %+v %v", report, err)
	}
}
