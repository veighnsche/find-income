package store

import (
	"context"
	"strings"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
)

func ownerSourceFixture(t *testing.T, s *Store, opportunityID, text string) EvidenceSource {
	t.Helper()
	v := currentInputVersions(t, s, opportunityID)
	source, _, err := s.AddEvidenceSource(context.Background(), ownerActor(), SourceInput{OpportunityID: opportunityID,
		ExpectedContextVersion: v.ContextVersion, OwnerObservation: &OwnerInput{OccurredAt: "2026-09-23T11:00:00Z",
			OriginalText: text, ExpectedPreferencesVersion: v.PreferencesVersion}})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestLocationAssessmentUsesRelevantSettingsFingerprint(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	o, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	source := addStatementFixture(t, s, o.ID, "Hybrid in Amsterdam, 2.5 office days each week.")
	days := int64(250)
	arrangement := addEvidenceFixture(t, s, source, source.OriginalText, EvidenceInput{Criterion: "location_arrangement", Finding: "explicit_match", ObservedValue: "hybrid arrangement", Arrangement: &WorkArrangement{Pattern: "hybrid", BaseLocation: "Amsterdam", OnsiteDaysHundredths: &days}})
	owner := ownerSourceFixture(t, s, o.ID, "This hybrid arrangement is workable for me.")
	addEvidenceFixture(t, s, owner, owner.OriginalText, EvidenceInput{Criterion: "location_workable", Finding: "explicit_match", ObservedValue: "workable", OwnerWorkableForEvidenceID: &arrangement.ID})
	view, err := s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || roleState(*view.Current, "location_workable") != fit.Match {
		t.Fatalf("initial location: %+v %v", view, err)
	}
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.TargetHoursHundredths = 3150
	p.RoleCriteria[0].Label = "Backend and platform"
	if _, _, err = s.UpdatePreferences(ctx, p.Version, p, ownerActor()); err != nil {
		t.Fatal(err)
	}
	view, err = s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || roleState(*view.Current, "location_workable") != fit.Match {
		t.Fatalf("unrelated edit invalidated location: %+v %v", view, err)
	}
	p, err = s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.PreferredLocation = "Utrecht"
	if _, _, err = s.UpdatePreferences(ctx, p.Version, p, ownerActor()); err != nil {
		t.Fatal(err)
	}
	view, err = s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || roleState(*view.Current, "location_workable") != fit.Unknown {
		t.Fatalf("location edit kept old assessment: %+v %v", view, err)
	}
}

func TestAuditedOfferOptionsKeepAdverseAlternativesVisible(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	o, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	statement := "Backend work. Remote across Netherlands. Either 32 hours at EUR 4500 base monthly or 40 hours at EUR 4000 base monthly."
	source := addStatementFixture(t, s, o.ID, statement)
	v := currentInputVersions(t, s, o.ID)
	declaration := "Either 32 hours at EUR 4500 base monthly or 40 hours at EUR 4000 base monthly"
	start := strings.Index(statement, declaration)
	set, _, err := s.CreateOfferOptionSet(ctx, ownerActor(), OfferOptionSetInput{OpportunityID: o.ID, SourceID: source.ID,
		ExpectedContextVersion: v.ContextVersion, ExpectedEvidenceVersion: v.EvidenceVersion,
		SpanStart: start, SpanEnd: start + len(declaration), Labels: []string{"32 hours", "40 hours"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Options) != 2 {
		t.Fatalf("options: %+v", set)
	}
	addEvidenceFixture(t, s, source, "Backend work.", EvidenceInput{Criterion: "role_criterion", CriterionID: "backend-platform", Presence: "explicit_presence", ObservedValue: "Assigned backend work"})
	arrangement := addEvidenceFixture(t, s, source, "Remote across Netherlands.", EvidenceInput{Criterion: "location_arrangement", Finding: "explicit_match", ObservedValue: "remote arrangement", Arrangement: &WorkArrangement{Pattern: "remote", RemoteGeography: "Netherlands"}})
	owner := ownerSourceFixture(t, s, o.ID, "Remote across Netherlands is workable.")
	addEvidenceFixture(t, s, owner, owner.OriginalText, EvidenceInput{Criterion: "location_workable", Finding: "explicit_match", ObservedValue: "workable", OwnerWorkableForEvidenceID: &arrangement.ID})
	addEvidenceFixture(t, s, source, "32 hours", EvidenceInput{Criterion: "target_hours_available", Finding: "explicit_match", ObservedValue: "32 hour option", Hours: &HoursAvailability{MinHundredths: 3200, MaxHundredths: 3200, HardBounds: true}, OfferOptionID: set.Options[0].ID})
	addEvidenceFixture(t, s, source, "40 hours", EvidenceInput{Criterion: "target_hours_available", Finding: "explicit_match", ObservedValue: "40 hour option", Hours: &HoursAvailability{MinHundredths: 4000, MaxHundredths: 4000, HardBounds: true}, OfferOptionID: set.Options[1].ID})
	addEvidenceFixture(t, s, source, "EUR 4500 base monthly", EvidenceInput{Criterion: "monthly_base_salary", Finding: "explicit_match", ObservedValue: "32 hour gross base", Salary: &ActualSalaryFacts{Currency: "EUR", Period: "month", Basis: "base", AmountCents: 450000, ActualWeeklyHoursHundredths: 3200}, OfferOptionID: set.Options[0].ID})
	addEvidenceFixture(t, s, source, "EUR 4000 base monthly", EvidenceInput{Criterion: "monthly_base_salary", Finding: "explicit_match", ObservedValue: "40 hour gross base", Salary: &ActualSalaryFacts{Currency: "EUR", Period: "month", Basis: "base", AmountCents: 400000, ActualWeeklyHoursHundredths: 4000}, OfferOptionID: set.Options[1].ID})
	view, err := s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.Qualified || view.Current.OptionSetStatus != "current" ||
		len(view.Current.OptionResults) != 2 || view.Current.OptionResults[0].Overall != fit.Qualified || view.Current.OptionResults[1].Overall != fit.Unsuitable {
		t.Fatalf("coherent alternatives: %+v %v", view, err)
	}
	adverse := addStatementFixture(t, s, o.ID, "Frontend UI implementation is assigned to this role.")
	addEvidenceFixture(t, s, adverse, adverse.OriginalText, EvidenceInput{Criterion: "role_criterion", CriterionID: "frontend-duties", Presence: "explicit_presence", ObservedValue: "Assigned frontend duty"})
	view, err = s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.Unsuitable ||
		view.Current.OptionResults[0].Overall != fit.Unsuitable || view.Current.OptionResults[1].Overall != fit.Unsuitable {
		t.Fatalf("common adverse duty hidden: %+v %v", view, err)
	}
	sets, err := s.CurrentOfferOptionSets(ctx, o.ID)
	if err != nil || len(sets) != 1 || len(sets[0].Options) != 2 {
		t.Fatalf("current set read: %+v %v", sets, err)
	}
	v = currentInputVersions(t, s, o.ID)
	_, _, err = s.CreateOfferOptionSet(ctx, ownerActor(), OfferOptionSetInput{OpportunityID: o.ID, SourceID: source.ID,
		ExpectedContextVersion: v.ContextVersion, ExpectedEvidenceVersion: v.EvidenceVersion,
		SpanStart: start, SpanEnd: start + len(declaration), Labels: []string{"32 hours", "40 hours"}})
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || view.Current.OptionSetStatus != "conflicting" || len(view.Current.OptionSetIDs) != 2 {
		t.Fatalf("multiple sets not flagged: %+v %v", view, err)
	}
}
