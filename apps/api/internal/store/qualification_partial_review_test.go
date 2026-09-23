package store

import (
	"context"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
)

func TestCurrentAmbiguousEmployerTermsRemainUnresolved(t *testing.T) {
	for _, criterion := range []string{"target_hours_available", "location_arrangement", "monthly_base_salary"} {
		t.Run(criterion, func(t *testing.T) {
			ctx := context.Background()
			s := openJobTestStore(t)
			company := createFixtureCompany(t, s)
			o, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
			if err != nil {
				t.Fatal(err)
			}
			source := addStatementFixture(t, s, o.ID, "Backend work. Remote across Netherlands. 32 hours. EUR 4500 gross base monthly.")
			addEvidenceFixture(t, s, source, "Backend work.", EvidenceInput{Criterion: "role_criterion", CriterionID: "backend-platform", Presence: "explicit_presence", ObservedValue: "Assigned backend work"})
			arrangement := addEvidenceFixture(t, s, source, "Remote across Netherlands.", EvidenceInput{Criterion: "location_arrangement", Finding: "explicit_match", ObservedValue: "remote", Arrangement: &WorkArrangement{Pattern: "remote", RemoteGeography: "Netherlands"}})
			owner := ownerSourceFixture(t, s, o.ID, "This arrangement works for me.")
			addEvidenceFixture(t, s, owner, owner.OriginalText, EvidenceInput{Criterion: "location_workable", Finding: "explicit_match", ObservedValue: "workable", OwnerWorkableForEvidenceID: &arrangement.ID})
			addEvidenceFixture(t, s, source, "32 hours.", EvidenceInput{Criterion: "target_hours_available", Finding: "explicit_match", ObservedValue: "32 hours", Hours: &HoursAvailability{MinHundredths: 3200, MaxHundredths: 3200, HardBounds: true}})
			addEvidenceFixture(t, s, source, "EUR 4500 gross base monthly.", EvidenceInput{Criterion: "monthly_base_salary", Finding: "explicit_match", ObservedValue: "actual base", Salary: &ActualSalaryFacts{Currency: "EUR", Period: "month", Basis: "base", AmountCents: 450000, ActualWeeklyHoursHundredths: 3200}})
			view, err := s.Qualification(ctx, o.ID)
			if err != nil || view.Current == nil || view.Current.Overall != fit.Qualified {
				t.Fatalf("baseline: %+v %v", view, err)
			}
			partial := addStatementFixture(t, s, o.ID, "These terms are now uncertain and need clarification.")
			addEvidenceFixture(t, s, partial, partial.OriginalText, EvidenceInput{Criterion: criterion, Finding: "ambiguous", ObservedValue: "Employer explicitly says current terms are uncertain"})
			view, err = s.Qualification(ctx, o.ID)
			if err != nil || view.Current == nil {
				t.Fatalf("read: %+v %v", view, err)
			}
			if view.Current.Overall != fit.NeedsRequalification {
				t.Fatalf("current ambiguous %s did not require requalification: %+v", criterion, view.Current)
			}
			if criterion == "monthly_base_salary" {
				if view.Current.Salary.State != fit.Unknown || !view.Current.Salary.Conflicting || view.Current.Salary.ConfirmedActual {
					t.Fatalf("ambiguous salary fell back to prior actual or advertised estimate: %+v", view.Current.Salary)
				}
			} else {
				found := false
				for _, result := range view.Current.Criteria {
					if result.ID == map[string]string{"target_hours_available": "target_hours_available", "location_arrangement": "location_workable"}[criterion] {
						found = result.State == fit.Unknown && result.Conflicting
					}
				}
				if !found {
					t.Fatalf("ambiguous %s did not stay visible: %+v", criterion, view.Current.Criteria)
				}
			}
		})
	}
}
