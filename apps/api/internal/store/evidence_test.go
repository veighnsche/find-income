package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/veighnsche/find-income-dashboard/api/internal/fit"
)

func currentEvidenceVersion(t *testing.T, s *Store, opportunityID string) int64 {
	t.Helper()
	var version int64
	if err := s.db.QueryRowContext(context.Background(), `SELECT evidence_version FROM qualification_input_versions
  WHERE opportunity_id=?`, opportunityID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func currentInputVersions(t *testing.T, s *Store, opportunityID string) QualificationInputVersions {
	t.Helper()
	versions, err := s.QualificationInputVersions(context.Background(), opportunityID)
	if err != nil {
		t.Fatal(err)
	}
	return versions
}

func addStatementFixture(t *testing.T, s *Store, opportunityID, text string) EvidenceSource {
	t.Helper()
	source, _, err := s.AddEvidenceSource(context.Background(), ownerActor(), SourceInput{
		OpportunityID: opportunityID, ExpectedContextVersion: currentInputVersions(t, s, opportunityID).ContextVersion,
		Statement: &StatementInput{SpeakerAffiliation: "employer_representative",
			SpeakerName: "R. Example", SpeakerRole: "Hiring manager", SpeakerOrganisation: "Harbour Systems",
			Channel: "meeting", OccurredAt: "2026-09-23T10:00:00Z", OriginalText: text},
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func addClaimFixture(t *testing.T, s *Store, opportunityID string, source EvidenceSource,
	criterion, value, finding, excerpt string, hours *HoursAvailability,
	arrangement *WorkArrangement, ownerArrangementID *string, salary *ActualSalaryFacts) Evidence {
	t.Helper()
	start := strings.Index(source.OriginalText, excerpt)
	if start < 0 {
		t.Fatalf("excerpt %q not found in %q", excerpt, source.OriginalText)
	}
	item, _, err := s.AddEvidence(context.Background(), ownerActor(), EvidenceInput{
		OpportunityID: opportunityID, SourceID: source.ID, Criterion: criterion,
		Finding: finding, ObservedValue: value, SpanStart: start, SpanEnd: start + len(excerpt),
		Hours: hours, Arrangement: arrangement, OwnerWorkableForEvidenceID: ownerArrangementID,
		Salary: salary, ExpectedEvidenceVersion: currentEvidenceVersion(t, s, opportunityID),
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestSourcedQualificationConflictSupersessionAndPreferenceRefresh(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	input := fixtureOpportunity(company.ID)
	input.Compensation = AdvertisedCompensation{Currency: "USD", MinAmountCents: int64Ptr(10000000),
		Period: "year", Basis: "unknown"}
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), input)
	if err != nil {
		t.Fatal(err)
	}
	text := "Backend platform scope. No frontend duties. No PHP duties. 32 hours weekly. Remote Netherlands. EUR 4500 gross monthly base at 32 hours."
	source := addStatementFixture(t, s, opportunity.ID, text)
	addClaimFixture(t, s, opportunity.ID, source, "backend_platform", "backend_primary", "explicit_match",
		"Backend platform scope.", nil, nil, nil, nil)
	addClaimFixture(t, s, opportunity.ID, source, "no_frontend_duties", "frontend_not_required", "explicit_match",
		"No frontend duties.", nil, nil, nil, nil)
	addClaimFixture(t, s, opportunity.ID, source, "no_php_focused_duties", "php_not_required", "explicit_match",
		"No PHP duties.", nil, nil, nil, nil)
	addClaimFixture(t, s, opportunity.ID, source, "target_hours_available", "weekly_hours_available", "explicit_match",
		"32 hours weekly.", &HoursAvailability{MinWeekly: 32, MaxWeekly: 32, HardBounds: true}, nil, nil, nil)
	arrangement := addClaimFixture(t, s, opportunity.ID, source, "location_arrangement", "work_arrangement", "explicit_match",
		"Remote Netherlands.", nil, &WorkArrangement{Pattern: "remote", RemoteGeography: "Netherlands"}, nil, nil)
	addClaimFixture(t, s, opportunity.ID, source, "monthly_base_salary", "actual_pay_terms", "explicit_match",
		"EUR 4500 gross monthly base at 32 hours.", nil, nil, nil,
		&ActualSalaryFacts{Currency: "EUR", Period: "month", Basis: "base", AmountCents: 450000, ActualWeeklyHours: 32})
	versions := currentInputVersions(t, s, opportunity.ID)
	ownerSource, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{OpportunityID: opportunity.ID,
		ExpectedContextVersion: versions.ContextVersion,
		OwnerObservation: &OwnerInput{OccurredAt: "2026-09-23T11:00:00Z", OriginalText: "Remote Netherlands is workable.",
			ExpectedPreferencesVersion: versions.PreferencesVersion}})
	if err != nil {
		t.Fatal(err)
	}
	addClaimFixture(t, s, opportunity.ID, ownerSource, "location_workable", "workable", "explicit_match",
		"Remote Netherlands is workable.", nil, nil, &arrangement.ID, nil)
	view, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Status != "current" || view.Current == nil || view.Current.Overall != fit.Qualified ||
		!view.Current.Salary.ConfirmedActual {
		t.Fatalf("sourced qualification: %+v err=%v", view, err)
	}
	qualifiedID := view.Current.ID
	conflictSource := addStatementFixture(t, s, opportunity.ID, "EUR 4000 gross monthly base at 32 hours.")
	conflict := addClaimFixture(t, s, opportunity.ID, conflictSource, "monthly_base_salary", "actual_pay_terms", "explicit_match",
		conflictSource.OriginalText, nil, nil, nil,
		&ActualSalaryFacts{Currency: "EUR", Period: "month", Basis: "base", AmountCents: 400000, ActualWeeklyHours: 32})
	view, err = s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.NeedsRequalification ||
		!view.Current.Salary.Conflicting || view.Current.Salary.Estimate != nil {
		t.Fatalf("salary conflict: %+v err=%v", view, err)
	}
	repeated, _, err := s.EvaluateCurrent(ctx, ownerActor(), opportunity.ID)
	if err != nil || repeated.Overall != fit.NeedsRequalification || !repeated.Salary.Conflicting {
		t.Fatalf("repeated evaluation erased requalification: %+v err=%v", repeated, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE evidence SET observed_value='fabricated' WHERE id=?`, conflict.ID); err == nil {
		t.Fatal("immutable evidence updated")
	}
	correctionSource := addStatementFixture(t, s, opportunity.ID, "Correction: EUR 4500 gross monthly base at 32 hours.")
	view, err = s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.NeedsRequalification {
		t.Fatalf("unrelated source erased requalification: %+v err=%v", view, err)
	}
	_, _, err = s.SupersedeEvidence(ctx, ownerActor(), conflict.ID, EvidenceInput{
		OpportunityID: opportunity.ID, SourceID: correctionSource.ID,
		Criterion: "monthly_base_salary", Finding: "explicit_match", ObservedValue: "actual_pay_terms",
		SpanStart: 0, SpanEnd: len(correctionSource.OriginalText),
		Salary:                  &ActualSalaryFacts{Currency: "EUR", Period: "month", Basis: "base", AmountCents: 450000, ActualWeeklyHours: 32},
		ExpectedEvidenceVersion: currentEvidenceVersion(t, s, opportunity.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.Qualified {
		t.Fatalf("supersession did not resolve conflict: %+v err=%v", view, err)
	}
	var oldOverall string
	if err := s.db.QueryRowContext(ctx, `SELECT overall_state FROM qualification_evaluations WHERE id=?`, qualifiedID).Scan(&oldOverall); err != nil || oldOverall != "qualified" {
		t.Fatalf("old qualified decision changed: %q %v", oldOverall, err)
	}
	preferences, err := s.CurrentPreferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preferences.TargetHours = 36
	if _, _, err := s.UpdatePreferences(ctx, 1, preferences, ownerActor()); err != nil {
		t.Fatal(err)
	}
	stale, err := s.qualificationView(ctx, opportunity.ID)
	if err != nil || stale.Status != "outdated" || stale.Current != nil {
		t.Fatalf("preference change reused old qualified result: %+v err=%v", stale, err)
	}
	view, err = s.Qualification(ctx, opportunity.ID)
	if err != nil || view.Current == nil || view.Current.Overall != fit.Unsuitable ||
		view.Current.PreferencesVersion != 2 {
		t.Fatalf("target hours were not reevaluated: %+v err=%v", view, err)
	}
	stage := "researching"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: 1, Stage: &stage}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || unchanged.Current == nil || unchanged.Current.ID != view.Current.ID {
		t.Fatalf("stage edit invalidated fit: %+v err=%v", unchanged, err)
	}
}

func TestEvidenceContextRoundTripsAndSourceValidation(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	companyA := createFixtureCompany(t, s)
	companyB, _, err := s.CreateCompany(ctx, ownerActor(), CompanyInput{Name: "Other Company"})
	if err != nil {
		t.Fatal(err)
	}
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(companyA.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{OpportunityID: opportunity.ID,
		ExpectedContextVersion: currentInputVersions(t, s, opportunity.ID).ContextVersion,
		Statement:              &StatementInput{SpeakerAffiliation: "employer_representative", OriginalText: "Backend role"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty attribution accepted: %v", err)
	}
	source := addStatementFixture(t, s, opportunity.ID, "Backend scope confirmed.")
	claim := addClaimFixture(t, s, opportunity.ID, source, "backend_platform", "backend_primary", "explicit_match",
		"Backend scope confirmed.", nil, nil, nil, nil)
	before, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || before.Current == nil || len(before.Current.SourceClaimIDs) != 1 {
		t.Fatalf("initial claim: %+v err=%v", before, err)
	}
	preparedContext := currentInputVersions(t, s, opportunity.ID)
	preparedStatement := &StatementInput{SpeakerAffiliation: "employer_representative",
		SpeakerName: "R. Example", SpeakerRole: "Hiring manager", SpeakerOrganisation: "Harbour Systems",
		Channel: "meeting", OccurredAt: "2026-09-23T10:00:00Z", OriginalText: "Prepared before company reassignment."}
	companyID := companyB.ID
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: 1, CompanyID: &companyID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{OpportunityID: opportunity.ID,
		ExpectedContextVersion: preparedContext.ContextVersion, Statement: preparedStatement}); !errors.Is(err, ErrConflict) {
		t.Fatalf("prepared A statement silently rebound to B: %v", err)
	}
	companyID = companyA.ID
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: 2, CompanyID: &companyID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{OpportunityID: opportunity.ID,
		ExpectedContextVersion: preparedContext.ContextVersion, Statement: preparedStatement}); !errors.Is(err, ErrConflict) {
		t.Fatalf("A-B-A resurrected prepared statement: %v", err)
	}
	afterCompany, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || afterCompany.Current == nil || len(afterCompany.Current.SourceClaimIDs) != 0 {
		t.Fatalf("A-B-A resurrected claim: %+v err=%v", afterCompany, err)
	}
	if _, _, err := s.AddEvidence(ctx, ownerActor(), EvidenceInput{OpportunityID: opportunity.ID,
		SourceID: source.ID, Criterion: "backend_platform", Finding: "explicit_match", ObservedValue: "backend_primary",
		SpanStart: 0, SpanEnd: len(source.OriginalText), ExpectedEvidenceVersion: currentEvidenceVersion(t, s, opportunity.ID)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old context source reused: %v", err)
	}
	project := "project"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: 3, Kind: &project}); err != nil {
		t.Fatal(err)
	}
	employment := "employment"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: 4, Kind: &employment}); err != nil {
		t.Fatal(err)
	}
	afterKind, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || afterKind.Current == nil || len(afterKind.Current.SourceClaimIDs) != 0 {
		t.Fatalf("employment-project-employment resurrected claim: %+v err=%v", afterKind, err)
	}
	read, err := s.Evidence(ctx, claim.ID)
	if err != nil || read.SourceID != source.ID || read.ObservedValue != "backend_primary" {
		t.Fatalf("history lost: %+v err=%v", read, err)
	}
	// A stage/note edit leaves a newly recorded same-context source live.
	newSource := addStatementFixture(t, s, opportunity.ID, "Backend scope still confirmed.")
	newClaim := addClaimFixture(t, s, opportunity.ID, newSource, "backend_platform", "backend_primary", "explicit_match",
		newSource.OriginalText, nil, nil, nil, nil)
	notesContext := currentInputVersions(t, s, opportunity.ID)
	notes := "Call tomorrow"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), opportunity.ID, OpportunityPatch{ExpectedRevision: 5, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddEvidenceSource(ctx, ownerActor(), SourceInput{OpportunityID: opportunity.ID,
		ExpectedContextVersion: notesContext.ContextVersion, Statement: preparedStatement}); err != nil {
		t.Fatalf("notes edit invalidated same-context prepared statement: %v", err)
	}
	afterNotes, err := s.Qualification(ctx, opportunity.ID)
	if err != nil || afterNotes.Current == nil || len(afterNotes.Current.SourceClaimIDs) != 1 ||
		afterNotes.Current.SourceClaimIDs[0] != newClaim.ID {
		t.Fatalf("note edit invalidated claim: %+v err=%v", afterNotes, err)
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
	opportunity, _, err := first.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	source := addStatementFixture(t, first, opportunity.ID, "Backend scope. PHP team mentioned. ü")
	version := currentEvidenceVersion(t, first, opportunity.ID)
	bad := EvidenceInput{OpportunityID: opportunity.ID, SourceID: source.ID,
		Criterion: "backend_platform", Finding: "explicit_match", ObservedValue: "backend_primary",
		SpanStart: len(source.OriginalText) - 1, SpanEnd: len(source.OriginalText), ExpectedEvidenceVersion: version}
	if _, _, err := first.AddEvidence(ctx, ownerActor(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 boundary accepted: %v", err)
	}
	if currentEvidenceVersion(t, first, opportunity.ID) != version {
		t.Fatal("invalid excerpt advanced version")
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []*Store{first, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			_, _, err := s.AddEvidence(ctx, ownerActor(), EvidenceInput{OpportunityID: opportunity.ID,
				SourceID: source.ID, Criterion: "backend_platform", Finding: "explicit_match",
				ObservedValue: "backend_primary", SpanStart: 0, SpanEnd: len("Backend scope."),
				ExpectedEvidenceVersion: version})
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
			t.Fatalf("unexpected race result: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	var count int
	if err := first.db.QueryRowContext(ctx, `SELECT count(*) FROM evidence WHERE opportunity_id=?`, opportunity.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("race partial write: count=%d err=%v", count, err)
	}
}

func TestEvidenceAndAuditRollBackWhenReevaluationFails(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	opportunity, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	source := addStatementFixture(t, s, opportunity.ID, "Backend scope confirmed.")
	version := currentEvidenceVersion(t, s, opportunity.ID)
	var auditBefore int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes`).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	// Synthetic broken stored policy makes deterministic reevaluation fail
	// after the evidence INSERT. The entire mutation must roll back.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO preferences_versions
  (version,preferred_location,allow_remote,allow_hybrid,target_hours,min_monthly_base_cents,
   salary_currency,require_backend_platform,exclude_frontend_duties,exclude_php_focused,
   timezone,created_at,actor_kind,actor_id)
  SELECT 2,preferred_location,allow_remote,allow_hybrid,target_hours,min_monthly_base_cents,
   'USD',require_backend_platform,exclude_frontend_duties,exclude_php_focused,
   timezone,created_at,actor_kind,actor_id FROM preferences_versions WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE preferences_current SET version=2 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.AddEvidence(ctx, ownerActor(), EvidenceInput{OpportunityID: opportunity.ID,
		SourceID: source.ID, Criterion: "backend_platform", Finding: "explicit_match",
		ObservedValue: "backend_primary", SpanStart: 0, SpanEnd: len(source.OriginalText),
		ExpectedEvidenceVersion: version})
	if err == nil {
		t.Fatal("invalid policy unexpectedly evaluated")
	}
	if currentEvidenceVersion(t, s, opportunity.ID) != version {
		t.Fatal("failed evaluation advanced evidence version")
	}
	var evidenceCount, auditAfter int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM evidence WHERE opportunity_id=?`, opportunity.ID).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes`).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 0 || auditAfter != auditBefore {
		t.Fatalf("partial write after reevaluation failure: evidence=%d audit=%d->%d", evidenceCount, auditBefore, auditAfter)
	}
}

func TestMigrationPreservesLegacyBranchingEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+dir+"/jobseek.sqlite?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations
  (version INTEGER PRIMARY KEY, name TEXT NOT NULL, sha256 TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:6] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO companies(id,name,created_at,updated_at)
  VALUES ('legacy-company','Legacy','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,original_text,stage,created_at,updated_at)
  VALUES ('legacy-opportunity','legacy-company','Legacy role','employment','Original vacancy','new',
          '2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"legacy-parent", ""}, {"legacy-child-a", "legacy-parent"}, {"legacy-child-b", "legacy-parent"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO evidence
  (id,opportunity_id,criterion,observed_value,confirmation_state,source_kind,
   source_url,source_contact_text,observed_at,supersedes_id,created_at)
  VALUES (?,?, 'backend_platform','legacy claim',
   'confirmed','user','https://legacy.example/source','Legacy recruiter',
   '2026-01-01T00:00:00Z',?, '2026-01-01T00:00:00Z')`,
			pair[0], "legacy-opportunity", optionalText(pair[1])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO qualification_evaluations
  (id,opportunity_id,opportunity_revision,preferences_version,overall_state,criterion_results_json,created_at)
  VALUES ('legacy-evaluation','legacy-opportunity',1,1,'qualified','legacy non-JSON result',
          '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM evidence WHERE supersedes_id='legacy-parent'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy branch lost: count=%d err=%v", count, err)
	}
	view, err := s.Qualification(ctx, "legacy-opportunity")
	if err != nil || view.Current == nil || view.Current.Overall == fit.Qualified || len(view.Current.SourceClaimIDs) != 0 {
		t.Fatalf("legacy unverified claim qualified: %+v err=%v", view, err)
	}
	legacy, err := s.Evidence(ctx, "legacy-parent")
	if err != nil || !legacy.Legacy || legacy.HasSpan || legacy.SourceKind != "user" ||
		legacy.SourceURL != "https://legacy.example/source" || legacy.SourceContactText != "Legacy recruiter" {
		t.Fatalf("legacy provenance hidden or trusted: %+v err=%v", legacy, err)
	}
	history, err := s.ListQualificationHistory(ctx, "legacy-opportunity", "", 10)
	if err != nil || len(history.Items) != 2 || !history.Items[0].Legacy ||
		history.Items[0].LegacyCriteriaJSON != "legacy non-JSON result" {
		t.Fatalf("legacy evaluation lost or trusted: %+v err=%v", history, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE evidence SET confirmation_state='unknown' WHERE id='legacy-parent'`); err == nil {
		t.Fatal("legacy evidence became mutable")
	}
}
