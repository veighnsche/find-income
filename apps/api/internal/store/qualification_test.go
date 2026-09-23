package store

import (
	"context"
	"errors"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
)

func criterionState(report Evaluation, criterion string) fit.State {
	for _, result := range report.Criteria {
		if result.Criterion == criterion {
			return result.State
		}
	}
	return ""
}

func TestPublishedClaimsIncidentalMentionsAndExplicitDuties(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	input := fixtureOpportunity(company.ID)
	input.OriginalText = "Backend services. Frontend team uses React. PHP legacy system elsewhere."
	opportunity, createChange, err := s.CreateOpportunity(ctx, ownerActor(), input)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{
		OpportunityID: opportunity.ID, VacancyChangeID: &createChange,
	})
	if err != nil {
		t.Fatal(err)
	}
	addClaimFixture(t, s, opportunity.ID, source, "backend_platform", "backend_primary", "explicit_match",
		"Backend services.", nil, nil, nil, nil)
	addClaimFixture(t, s, opportunity.ID, source, "no_frontend_duties", "frontend_mentioned", "mention_only",
		"Frontend team uses React.", nil, nil, nil, nil)
	addClaimFixture(t, s, opportunity.ID, source, "no_php_focused_duties", "php_mentioned", "mention_only",
		"PHP legacy system elsewhere.", nil, nil, nil, nil)
	view, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.Unresolved ||
		criterionState(*view.Current, "backend_platform") != fit.Unknown ||
		criterionState(*view.Current, "no_frontend_duties") != fit.Unknown ||
		criterionState(*view.Current, "no_php_focused_duties") != fit.Unknown {
		t.Fatalf("publication or incidental mention qualified: %+v err=%v", view, err)
	}
	changedText := "You must implement frontend UI and maintain PHP services."
	_, patchChange, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{
		ExpectedRevision: 1, OriginalText: &changedText,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.qualificationView(ctx, opportunity.ID)
	if err != nil || stale.Status != "outdated" || stale.Current != nil {
		t.Fatalf("source edit did not stale fit: %+v err=%v", stale, err)
	}
	if _, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{
		OpportunityID: opportunity.ID, VacancyChangeID: &createChange,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old vacancy snapshot recaptured: %v", err)
	}
	newSource, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{
		OpportunityID: opportunity.ID, VacancyChangeID: &patchChange,
	})
	if err != nil {
		t.Fatal(err)
	}
	addClaimFixture(t, s, opportunity.ID, newSource, "no_frontend_duties", "frontend_required", "explicit_mismatch",
		"implement frontend UI", nil, nil, nil, nil)
	addClaimFixture(t, s, opportunity.ID, newSource, "no_php_focused_duties", "php_required", "explicit_mismatch",
		"maintain PHP services", nil, nil, nil, nil)
	view, err = s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.Unsuitable ||
		criterionState(*view.Current, "no_frontend_duties") != fit.Mismatch ||
		criterionState(*view.Current, "no_php_focused_duties") != fit.Mismatch ||
		len(view.Current.SourceClaimIDs) != 2 {
		t.Fatalf("explicit published duty did not mismatch or old claims remained: %+v err=%v", view, err)
	}
	currentID := view.Current.ID
	notes := "Check fit later"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{
		ExpectedRevision: 2, Notes: &notes,
	}); err != nil {
		t.Fatal(err)
	}
	view, err = s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.ID != currentID {
		t.Fatalf("notes edit invalidated fit: %+v err=%v", view, err)
	}
}

func TestCurrentPointerGuardAndNoNeedlessReadWrite(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	first, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	secondInput := fixtureOpportunity(company.ID)
	secondInput.Title = "Other role"
	second, _, err := s.CreateOpportunity(ctx, ownerActor(), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	firstView, err := s.Qualification(ctx, first.ID)
	if err != nil || firstView.Current == nil {
		t.Fatalf("first fit: %+v %v", firstView, err)
	}
	secondView, err := s.Qualification(ctx, second.ID)
	if err != nil || secondView.Current == nil {
		t.Fatalf("second fit: %+v %v", secondView, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE qualification_current SET evaluation_id=? WHERE opportunity_id=?`,
		secondView.Current.ID, first.ID); err == nil {
		t.Fatal("cross-opportunity current pointer accepted")
	}
	var countBefore int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM qualification_evaluations WHERE opportunity_id=?`, first.ID).Scan(&countBefore); err != nil {
		t.Fatal(err)
	}
	again, err := s.Qualification(ctx, first.ID)
	if err != nil || again.Current == nil || again.Current.ID != firstView.Current.ID {
		t.Fatalf("current read changed evaluation: %+v %v", again, err)
	}
	var countAfter int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM qualification_evaluations WHERE opportunity_id=?`, first.ID).Scan(&countAfter); err != nil || countAfter != countBefore {
		t.Fatalf("current read wrote duplicate: %d -> %d %v", countBefore, countAfter, err)
	}
}

func TestOwnerWorkabilityBoundToPreferenceVersionAndPendingRefresh(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	statement := addStatementFixture(t, s, opportunity.ID, "Remote across the Netherlands is allowed.")
	arrangement := addClaimFixture(t, s, opportunity.ID, statement,
		"location_arrangement", "work_arrangement", "explicit_match", statement.OriginalText,
		nil, &WorkArrangement{Pattern: "remote", RemoteGeography: "Netherlands"}, nil, nil)
	owner, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{OpportunityID: opportunity.ID,
		OwnerObservation: &OwnerInput{OccurredAt: "2026-09-23T12:00:00Z", OriginalText: "That remote setup is workable."}})
	if err != nil {
		t.Fatal(err)
	}
	addClaimFixture(t, s, opportunity.ID, owner, "location_workable", "workable", "explicit_match",
		owner.OriginalText, nil, nil, &arrangement.ID, nil)
	view, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || criterionState(*view.Current, "location_workable") != fit.Match {
		t.Fatalf("owner arrangement binding: %+v %v", view, err)
	}
	preferences, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preferences.PreferredLocation = "Rotterdam"
	if _, _, err := s.UpdatePreferences(ctx, preferences.Version, preferences, ownerActor()); err != nil {
		t.Fatal(err)
	}
	stale, err := s.qualificationView(ctx, opportunity.ID)
	if err != nil || stale.Status != "outdated" || stale.Current != nil {
		t.Fatalf("preference stale: %+v %v", stale, err)
	}
	processed, err := s.ReevaluatePending(ctx, 10)
	if err != nil || processed < 1 {
		t.Fatalf("pending refresh: %d %v", processed, err)
	}
	view, err = s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.PreferencesVersion != 2 ||
		criterionState(*view.Current, "location_workable") != fit.Unknown {
		t.Fatalf("old owner workability carried across preference: %+v %v", view, err)
	}
	_, _, err = s.AddEvidence(ctx, ownerActor(), EvidenceInput{OpportunityID: opportunity.ID, SourceID: owner.ID,
		Criterion: "location_workable", Finding: "explicit_match", ObservedValue: "workable",
		SpanStart: 0, SpanEnd: len(owner.OriginalText), OwnerWorkableForEvidenceID: &arrangement.ID,
		ExpectedEvidenceVersion: currentEvidenceVersion(t, s, opportunity.ID)})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("old owner statement reused after preference change: %v", err)
	}
}

func TestEvidenceAndEvaluationSurviveRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	source := addStatementFixture(t, s, opportunity.ID, "Backend platform scope confirmed.")
	claim := addClaimFixture(t, s, opportunity.ID, source, "backend_platform", "backend_primary",
		"explicit_match", source.OriginalText, nil, nil, nil, nil)
	before, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || before.Current == nil {
		t.Fatalf("before restart: %+v %v", before, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || after.Current == nil || after.Current.ID != before.Current.ID ||
		len(after.Current.SourceClaimIDs) != 1 || after.Current.SourceClaimIDs[0] != claim.ID {
		t.Fatalf("restart changed evaluation: %+v %v", after, err)
	}
	read, err := s.EvidenceSource(ctx, source.ID)
	if err != nil || read.OriginalText != source.OriginalText || read.ContentSHA256 != source.ContentSHA256 {
		t.Fatalf("restart changed source: %+v %v", read, err)
	}
	page, err := s.ListEvidence(ctx, opportunity.ID, true, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != claim.ID {
		t.Fatalf("evidence list: %+v %v", page, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE evidence_sources SET original_text='changed' WHERE id=?`, source.ID); err == nil {
		t.Fatal("immutable source updated")
	}
}
