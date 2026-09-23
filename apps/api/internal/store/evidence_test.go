package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
)

func currentInputVersions(t *testing.T, s *Store, opportunityID string) QualificationInputVersions {
	t.Helper()
	v, err := s.QualificationInputVersions(context.Background(), opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func addStatementFixture(t *testing.T, s *Store, opportunityID, value string) EvidenceSource {
	t.Helper()
	v := currentInputVersions(t, s, opportunityID)
	source, _, err := s.AddEvidenceSource(context.Background(), ownerActor(), SourceInput{OpportunityID: opportunityID,
		ExpectedContextVersion: v.ContextVersion, Statement: &StatementInput{
			SpeakerAffiliation: "employer_representative", SpeakerName: "R. Example", SpeakerRole: "Hiring manager",
			SpeakerOrganisation: "Harbour Systems", Channel: "meeting", OccurredAt: "2026-09-23T10:00:00Z",
			OriginalText: value}})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func addEvidenceFixture(t *testing.T, s *Store, source EvidenceSource, excerpt string, input EvidenceInput) Evidence {
	t.Helper()
	start := strings.Index(source.OriginalText, excerpt)
	if start < 0 {
		t.Fatalf("quote %q absent from source", excerpt)
	}
	v := currentInputVersions(t, s, source.OpportunityID)
	input.OpportunityID, input.SourceID = source.OpportunityID, source.ID
	input.ExpectedEvidenceVersion, input.ExpectedPreferencesVersion = v.EvidenceVersion, v.PreferencesVersion
	input.SpanStart, input.SpanEnd = start, start+len(excerpt)
	claim, _, err := s.AddEvidence(context.Background(), ownerActor(), input)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func roleState(report Evaluation, id string) fit.State {
	for _, c := range report.Criteria {
		if c.ID == id {
			return c.State
		}
	}
	return ""
}

func TestGenericRoleEvidenceBindsDefinitionAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	o, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	source := addStatementFixture(t, s, o.ID, "Backend platform work. Frontend team mentioned only.")
	addEvidenceFixture(t, s, source, "Backend platform work.", EvidenceInput{Criterion: "role_criterion", CriterionID: "backend-platform", Presence: "explicit_presence", ObservedValue: "Assigned backend work"})
	addEvidenceFixture(t, s, source, "Frontend team mentioned only.", EvidenceInput{Criterion: "role_criterion", CriterionID: "frontend-duties", Presence: "mention_only", ObservedValue: "Incidental team mention"})
	view, err := s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || roleState(*view.Current, "backend-platform") != fit.Match || roleState(*view.Current, "frontend-duties") != fit.Unknown {
		t.Fatalf("role assessment: %+v %v", view, err)
	}
	adverse := addStatementFixture(t, s, o.ID, "Frontend UI implementation is assigned to this role.")
	claim := addEvidenceFixture(t, s, adverse, adverse.OriginalText, EvidenceInput{Criterion: "role_criterion", CriterionID: "frontend-duties", Presence: "explicit_presence", ObservedValue: "Frontend is assigned"})
	view, err = s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.Unsuitable || roleState(*view.Current, "frontend-duties") != fit.Mismatch {
		t.Fatalf("adverse duty: %+v %v", view, err)
	}
	oldEvaluationID := view.Current.ID
	p, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.RoleCriteria {
		if p.RoleCriteria[i].ID == "frontend-duties" {
			p.RoleCriteria[i].Description = "Frontend UI and design system implementation is assigned."
		}
	}
	if _, _, err = s.UpdatePreferences(ctx, p.Version, p, ownerActor()); err != nil {
		t.Fatal(err)
	}
	view, err = s.Qualification(ctx, o.ID)
	if err != nil || view.Current == nil || roleState(*view.Current, "frontend-duties") != fit.Unknown {
		t.Fatalf("old role claim survived meaning change: %+v %v", view, err)
	}
	read, err := s.Evidence(ctx, claim.ID)
	if err != nil || read.RoleDefinition == nil || read.RoleDefinition.Description != "Frontend UI implementation is an assigned responsibility in this role." {
		t.Fatalf("captured definition lost: %+v %v", read, err)
	}
	var oldOverall string
	if err := s.db.QueryRowContext(ctx, `SELECT overall_state FROM qualification_evaluations WHERE id=?`, oldEvaluationID).Scan(&oldOverall); err != nil || oldOverall != "unsuitable" {
		t.Fatalf("old decision changed: %q %v", oldOverall, err)
	}
}

func TestEvidenceExactExcerptAndConcurrentVersion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	company := createFixtureCompany(t, first)
	o, _, err := first.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	source := addStatementFixture(t, first, o.ID, "Backend work. ü")
	v := currentInputVersions(t, first, o.ID)
	input := EvidenceInput{OpportunityID: o.ID, SourceID: source.ID, Criterion: "role_criterion", CriterionID: "backend-platform", Presence: "explicit_presence", ObservedValue: "Assigned backend work", ExpectedEvidenceVersion: v.EvidenceVersion, ExpectedPreferencesVersion: v.PreferencesVersion, SpanStart: len(source.OriginalText) - 1, SpanEnd: len(source.OriginalText)}
	if _, _, err := first.AddEvidence(ctx, ownerActor(), input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("split UTF-8 quote accepted: %v", err)
	}
	input.SpanStart, input.SpanEnd = 0, len("Backend work.")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []*Store{first, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			_, _, err := s.AddEvidence(ctx, ownerActor(), input)
			results <- err
		}(s)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatalf("race: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
