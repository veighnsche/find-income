package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func int64Ptr(value int64) *int64 { return &value }
func intPtr(value int) *int       { return &value }

func TestAnnualConversionRequiresExactVacancyQuote(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	input := fixtureOpportunity(company.ID)
	quote := "twelve equal monthly base payments"
	input.OriginalText = "Annual gross base EUR 54,000 paid in " + quote + "."
	input.Compensation.Period = "year"
	input.Compensation.MinAmountCents = int64Ptr(5400000)
	input.Compensation.MaxAmountCents = nil
	input.Compensation.AnnualConversion = "twelve_equal_monthly_base_payments"
	if _, _, err := s.CreateOpportunity(ctx, ownerActor(), input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("conversion without source span: %v", err)
	}
	start := strings.Index(input.OriginalText, quote)
	input.Compensation.AnnualConversionSpanStart = intPtr(start)
	input.Compensation.AnnualConversionSpanEnd = intPtr(start + len(quote))
	o, _, err := s.CreateOpportunity(ctx, ownerActor(), input)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.Opportunity(ctx, o.ID)
	if err != nil || read.Compensation.AnnualConversionExcerpt != quote || len(read.Compensation.AnnualConversionSHA256) != 64 {
		t.Fatalf("conversion proof lost: %+v %v", read.Compensation, err)
	}
	changed := "Annual gross base EUR 54,000 includes an annual bonus."
	if _, _, err = s.PatchOpportunity(ctx, ownerActor(), o.ID, OpportunityPatch{ExpectedRevision: 1, OriginalText: &changed}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unattested changed source: %v", err)
	}
}

func fixtureOpportunity(companyID string) OpportunityInput {
	return OpportunityInput{
		CompanyID: companyID, Title: "Backend Engineer", Kind: "employment",
		SourceURL: "https://harbour.example/jobs/1", OriginalText: "Build Go services.",
		Notes: "First private tracking note",
		Stage: "new", WorkPattern: "hybrid", LocationText: "Amsterdam",
		PostedOn: "2026-09-20", DeadlineOn: "2026-10-20",
		Compensation: AdvertisedCompensation{
			Currency: "EUR", MinAmountCents: int64Ptr(600000), MaxAmountCents: int64Ptr(700000),
			Period: "month", ReferenceHoursHundredths: int64Ptr(4000), Basis: "base", BenefitsText: "Training budget",
		},
	}
}

func createFixtureCompany(t *testing.T, s *Store) Company {
	t.Helper()
	company, _, err := s.CreateCompany(context.Background(), ownerActor(), CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	return company
}

func snapshotFields(t *testing.T, change RecordChange) map[string]any {
	t.Helper()
	if change.SnapshotState != "captured" || len(change.Snapshot) == 0 {
		t.Fatalf("missing captured snapshot: %+v", change)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(change.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestOpportunityRoundTripHistoryArchiveAndReferences(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	input := fixtureOpportunity(company.ID)
	created, createChange, err := s.CreateOpportunity(ctx, ownerActor(), input)
	if err != nil || createChange == "" || created.Revision != 1 {
		t.Fatalf("create: %+v change=%q err=%v", created, createChange, err)
	}
	read, err := s.Opportunity(ctx, created.ID)
	if err != nil || read.OriginalText != input.OriginalText || read.SourceURL != input.SourceURL ||
		read.Notes != input.Notes ||
		read.Compensation.MinAmountCents == nil || *read.Compensation.MinAmountCents != 600000 ||
		read.Compensation.ReferenceHoursHundredths == nil || *read.Compensation.ReferenceHoursHundredths != 4000 {
		t.Fatalf("roundtrip: %+v err=%v", read, err)
	}
	initialFit, err := s.Qualification(ctx, created.ID)
	if err != nil || initialFit.Current == nil || initialFit.Current.Salary.ConfirmedActual {
		t.Fatalf("advertisement became confirmed pay: %+v %v", initialFit, err)
	}
	firstEvent, err := s.RecordChange(ctx, createChange)
	if err != nil || firstEvent.Actor.ID != "owner" || firstEvent.RevisionAfter == nil || *firstEvent.RevisionAfter != 1 {
		t.Fatalf("create event: %+v err=%v", firstEvent, err)
	}
	firstSnapshot := snapshotFields(t, firstEvent)
	if firstSnapshot["originalText"] != input.OriginalText || firstSnapshot["sourceUrl"] != input.SourceURL ||
		firstSnapshot["notes"] != input.Notes {
		t.Fatalf("original source not captured: %+v", firstSnapshot)
	}
	comp := input.Compensation
	comp.MinAmountCents = int64Ptr(620000)
	newText := "Build Go services; on-call is mentioned."
	newURL := "https://harbour.example/jobs/1?revision=2"
	updated, patchChange, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{
		ExpectedRevision: 1, OriginalText: &newText, SourceURL: &newURL, Compensation: &comp,
	})
	if err != nil || updated.Revision != 2 || updated.OriginalText != newText {
		t.Fatalf("patch: %+v change=%q err=%v", updated, patchChange, err)
	}
	secondEvent, err := s.RecordChange(ctx, patchChange)
	if err != nil || secondEvent.RevisionBefore == nil || *secondEvent.RevisionBefore != 1 ||
		secondEvent.RevisionAfter == nil || *secondEvent.RevisionAfter != 2 {
		t.Fatalf("patch event: %+v err=%v", secondEvent, err)
	}
	secondSnapshot := snapshotFields(t, secondEvent)
	if secondSnapshot["originalText"] != newText || secondSnapshot["sourceUrl"] != newURL {
		t.Fatalf("revised source not captured: %+v", secondSnapshot)
	}
	oldEvent, err := s.RecordChange(ctx, createChange)
	if err != nil || snapshotFields(t, oldEvent)["originalText"] != input.OriginalText ||
		snapshotFields(t, oldEvent)["notes"] != input.Notes {
		t.Fatalf("old source mutated: %+v err=%v", oldEvent, err)
	}
	// Clearing tracking notes is a separate edit; it must not rewrite the
	// original vacancy source or previous revision's note snapshot.
	clearedNotes := ""
	cleared, clearChange, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{
		ExpectedRevision: 2, Notes: &clearedNotes,
	})
	if err != nil || cleared.Revision != 3 || cleared.Notes != "" ||
		cleared.OriginalText != newText || cleared.SourceURL != newURL {
		t.Fatalf("clear notes changed source: %+v err=%v", cleared, err)
	}
	clearEvent, err := s.RecordChange(ctx, clearChange)
	if err != nil || snapshotFields(t, clearEvent)["notes"] != "" ||
		snapshotFields(t, clearEvent)["originalText"] != newText {
		t.Fatalf("clear note snapshot: %+v err=%v", clearEvent, err)
	}
	if oldEvent, err = s.RecordChange(ctx, createChange); err != nil ||
		snapshotFields(t, oldEvent)["notes"] != input.Notes {
		t.Fatalf("old note snapshot mutated: %+v err=%v", oldEvent, err)
	}
	archived, archiveChange, err := s.ArchiveOpportunity(ctx, ownerActor(), created.ID, 3)
	if err != nil || archived.Revision != 4 || archived.ArchivedAt == "" {
		t.Fatalf("archive: %+v err=%v", archived, err)
	}
	archiveEvent, err := s.RecordChange(ctx, archiveChange)
	if err != nil || snapshotFields(t, archiveEvent)["archivedAt"] == nil {
		t.Fatalf("archive snapshot: %+v err=%v", archiveEvent, err)
	}
	if _, _, err := s.ArchiveCompany(ctx, ownerActor(), company.ID, company.Revision); err != nil {
		t.Fatal(err)
	}
	read, err = s.Opportunity(ctx, created.ID)
	if err != nil || read.CompanyID != company.ID || read.ArchivedAt == "" || read.OriginalText != newText {
		t.Fatalf("archive lost reference/source: %+v err=%v", read, err)
	}
	active, err := s.ListOpportunities(ctx, OpportunityListOptions{})
	if err != nil || len(active.Items) != 0 {
		t.Fatalf("archived opportunity in active list: %+v err=%v", active, err)
	}
	all, err := s.ListOpportunities(ctx, OpportunityListOptions{IncludeArchived: true})
	if err != nil || len(all.Items) != 1 || all.Items[0].ID != created.ID {
		t.Fatalf("archived opportunity absent: %+v err=%v", all, err)
	}
}

func TestOpportunityInvalidPatchRollsBackWithoutPhantomChange(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	created, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM record_changes").Scan(&before); err != nil {
		t.Fatal(err)
	}
	badDate := "2026-02-30"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{
		ExpectedRevision: 1, PostedOn: &badDate,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid date accepted: %v", err)
	}
	longNotes := strings.Repeat("x", 10001)
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{
		ExpectedRevision: 1, Notes: &longNotes,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded notes accepted: %v", err)
	}
	badComp := fixtureOpportunity(company.ID).Compensation
	badComp.MaxAmountCents = int64Ptr(500000)
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{
		ExpectedRevision: 1, Compensation: &badComp,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("inverted salary accepted: %v", err)
	}
	missingCompany := "missing-company"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{
		ExpectedRevision: 1, CompanyID: &missingCompany,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing company patch: %v", err)
	}
	if _, _, err := s.CreateOpportunity(ctx, ownerActor(), OpportunityInput{
		CompanyID: company.ID, Title: "No source", Kind: "employment", Stage: "new",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source-less opportunity accepted: %v", err)
	}
	current, err := s.Opportunity(ctx, created.ID)
	if err != nil || current.Revision != 1 || current.Compensation.MaxAmountCents == nil ||
		*current.Compensation.MaxAmountCents != 700000 {
		t.Fatalf("partial patch persisted: %+v err=%v", current, err)
	}
	var after int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM record_changes").Scan(&after); err != nil || after != before {
		t.Fatalf("phantom change: before=%d after=%d err=%v", before, after, err)
	}
}

func TestOpportunityConcurrentWritersGetOneConflict(t *testing.T) {
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
	created, _, err := first.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index, s := range []*Store{first, second} {
		wg.Add(1)
		go func(index int, s *Store) {
			defer wg.Done()
			<-start
			title := fmt.Sprintf("Backend Engineer %d", index)
			_, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{
				ExpectedRevision: 1, Title: &title,
			})
			results <- err
		}(index, s)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	current, err := first.Opportunity(ctx, created.ID)
	if err != nil || current.Revision != 2 {
		t.Fatalf("concurrent revision: %+v err=%v", current, err)
	}
	var eventCount int
	if err := first.db.QueryRowContext(ctx, `SELECT count(*) FROM record_changes WHERE entity_kind='opportunity' AND entity_id=?`, created.ID).Scan(&eventCount); err != nil || eventCount != 2 {
		t.Fatalf("concurrent event count=%d err=%v", eventCount, err)
	}
}

func TestOpportunityListTieAndDuplicateWarnings(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	var records []Opportunity
	for index := 0; index < 3; index++ {
		input := fixtureOpportunity(company.ID)
		input.Title = fmt.Sprintf("Backend Engineer %d", index)
		input.SourceURL = fmt.Sprintf("https://harbour.example/jobs/%d", index)
		record, _, err := s.CreateOpportunity(ctx, ownerActor(), input)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	// Synthetic fixture tie: the stable ID tie-breaker prevents skipped rows.
	if _, err := s.db.ExecContext(ctx, `UPDATE opportunities SET created_at='2026-09-23T00:00:00.000000000Z'`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	firstCursor := ""
	for {
		page, err := s.ListOpportunities(ctx, OpportunityListOptions{Cursor: cursor, Limit: 1, CompanyID: company.ID})
		if err != nil || len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatalf("tied page: %+v err=%v", page, err)
		}
		seen[page.Items[0].ID] = true
		if page.NextCursor == "" {
			break
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		cursor = page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("tied list skipped records: %+v", seen)
	}
	if _, err := s.ListOpportunities(ctx, OpportunityListOptions{Cursor: firstCursor, Limit: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed opportunity filter accepted: %v", err)
	}
	input := fixtureOpportunity(company.ID)
	input.SourceURL = "https://harbour.example/jobs/0"
	input.Title = "Different title"
	duplicates, err := s.LikelyDuplicateOpportunities(ctx, input, "")
	if err != nil || len(duplicates) != 1 || duplicates[0].Reason != "same_source_url" {
		t.Fatalf("source duplicate warning: %+v err=%v", duplicates, err)
	}
	input.SourceURL = "https://other.example/jobs/9"
	input.Title = "backend engineer 1"
	duplicates, err = s.LikelyDuplicateOpportunities(ctx, input, "")
	if err != nil || len(duplicates) != 1 || duplicates[0].Reason != "same_company_title" {
		t.Fatalf("title duplicate warning: %+v err=%v", duplicates, err)
	}
	for _, record := range records {
		if _, err := s.Opportunity(ctx, record.ID); err != nil {
			t.Fatalf("warning merged/deleted record %s: %v", record.ID, err)
		}
	}
}

func TestChangedSinceWatermarkHistoryVacuumAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	company := createFixtureCompany(t, s)
	created, _, err := s.CreateOpportunity(ctx, ownerActor(), fixtureOpportunity(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	firstText := "First changed source"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{ExpectedRevision: 1, OriginalText: &firstText}); err != nil {
		t.Fatal(err)
	}
	page1, err := s.ListRecordChanges(ctx, RecordChangeOptions{Limit: 1, EntityKind: "opportunity"})
	if err != nil || len(page1.Items) != 1 || page1.NextCursor == "" || page1.Watermark < page1.Items[0].Sequence {
		t.Fatalf("change page1: %+v err=%v", page1, err)
	}
	if page1.Items[0].RevisionAfter == nil || *page1.Items[0].RevisionAfter != 1 ||
		snapshotFields(t, page1.Items[0])["originalText"] != created.OriginalText {
		t.Fatalf("first event snapshot/revision: %+v", page1.Items[0])
	}
	secondText := "Second changed source after page one"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{ExpectedRevision: 2, OriginalText: &secondText}); err != nil {
		t.Fatal(err)
	}
	page2, err := s.ListRecordChanges(ctx, RecordChangeOptions{Cursor: page1.NextCursor, Limit: 1, EntityKind: "opportunity"})
	if err != nil || len(page2.Items) != 1 || page2.NextCursor != "" || page2.Watermark != page1.Watermark ||
		page2.Items[0].RevisionAfter == nil || *page2.Items[0].RevisionAfter != 2 ||
		snapshotFields(t, page2.Items[0])["originalText"] != firstText {
		t.Fatalf("frozen batch page2: %+v err=%v", page2, err)
	}
	current, err := s.Opportunity(ctx, created.ID)
	if err != nil || current.Revision != 3 || current.OriginalText != secondText {
		t.Fatalf("current record confused with event snapshot: %+v err=%v", current, err)
	}
	nextPoll, err := s.ListRecordChanges(ctx, RecordChangeOptions{After: page2.Watermark, EntityKind: "opportunity"})
	if err != nil || len(nextPoll.Items) != 1 || nextPoll.Items[0].RevisionAfter == nil ||
		*nextPoll.Items[0].RevisionAfter != 3 || snapshotFields(t, nextPoll.Items[0])["originalText"] != secondText {
		t.Fatalf("next poll missed later edit: %+v err=%v", nextPoll, err)
	}
	if _, err := s.ListRecordChanges(ctx, RecordChangeOptions{Cursor: page1.NextCursor, EntityKind: "company"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor filter switch accepted: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		t.Fatal(err)
	}
	stage := "researching"
	if _, _, err := s.PatchOpportunity(ctx, ownerActor(), created.ID, OpportunityPatch{ExpectedRevision: 3, Stage: &stage}); err != nil {
		t.Fatal(err)
	}
	afterVacuum, err := s.ListRecordChanges(ctx, RecordChangeOptions{After: nextPoll.Watermark, EntityKind: "opportunity"})
	if err != nil || len(afterVacuum.Items) != 1 || afterVacuum.Items[0].Sequence <= nextPoll.Watermark {
		t.Fatalf("cursor failed after VACUUM/restart: %+v err=%v", afterVacuum, err)
	}
}

func TestSameTimeAuditEventsHaveDistinctSequences(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	company := createFixtureCompany(t, s)
	// Synthetic audit ties exercise the sequence independently of timestamps.
	for index := 0; index < 2; index++ {
		_, err := s.db.ExecContext(ctx, `INSERT INTO audit_changes
  (id,actor_kind,actor_id,operation,entity_kind,entity_id,revision_before,revision_after,occurred_at)
  VALUES (?,?,?,?,?,?,1,1,'2026-09-23T00:00:00Z')`,
			fmt.Sprintf("tie-%d", index), "system", "fixture", "company.synthetic", "company", company.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListRecordChanges(ctx, RecordChangeOptions{EntityKind: "company", Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("tie page: %+v err=%v", page, err)
	}
	seen := map[int64]bool{page.Items[0].Sequence: true}
	cursor := page.NextCursor
	for cursor != "" {
		page, err = s.ListRecordChanges(ctx, RecordChangeOptions{EntityKind: "company", Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range page.Items {
			if seen[change.Sequence] {
				t.Fatalf("duplicate sequence %d", change.Sequence)
			}
			seen[change.Sequence] = true
		}
		cursor = page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("missed tied events: %+v", seen)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM record_changes WHERE sequence=?", page.Items[0].Sequence); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("change history was mutable: %v", err)
	}
}

// D2: recollecting the same verified vacancy (same source URL) keeps ONE
// identity: the second create reconciles to the first row unchanged, the
// saved owner decision stays linked, and the list shows one job. A
// genuinely different vacancy (different URL) stays distinct even when
// the title and company resemble the first.
func TestCreateOpportunityReconcilesVerifiedSource(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()

	firstCompany := createFixtureCompany(t, s)
	firstInput := fixtureOpportunity(firstCompany.ID)
	first, firstChange, err := s.CreateOpportunity(ctx, owner, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if firstChange == "" {
		t.Fatal("fresh create returned no change id")
	}
	if _, _, err := s.SetOwnerOpportunityDecision(ctx, owner, first.ID, OwnerDecisionInput{
		RequestKey: "d2-select", ExpectedOpportunityRevision: first.Revision,
		ExpectedDecisionRevision: 0, Decision: "selected"}); err != nil {
		t.Fatal(err)
	}

	// Find more recollects the same verified vacancy under another company
	// row and a retitled sighting: the identity reconciles anyway.
	secondCompany, _, err := s.CreateCompany(ctx, owner, CompanyInput{Name: "Harbour Systems"})
	if err != nil {
		t.Fatal(err)
	}
	secondInput := fixtureOpportunity(secondCompany.ID)
	secondInput.Title = "Backend Engineer (refreshed title)"
	second, secondChange, err := s.CreateOpportunity(ctx, owner, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || secondChange != "" {
		t.Fatalf("resighting forked the identity: %q change %q, want %q with no new write", second.ID, secondChange, first.ID)
	}
	if second.Title != first.Title || second.Revision != first.Revision {
		t.Fatalf("reconcile rewrote the record: %+v vs %+v", second, first)
	}
	decision, err := s.OwnerOpportunityDecision(ctx, first.ID)
	if err != nil || decision.Decision != "selected" {
		t.Fatalf("owner decision lost across recollection: %+v err=%v", decision, err)
	}
	resolved, err := s.OpportunityBySourceURL(ctx, firstInput.SourceURL)
	if err != nil || resolved.ID != first.ID {
		t.Fatalf("source lookup: %+v err=%v", resolved, err)
	}
	page, err := s.ListOpportunities(ctx, OpportunityListOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("list shows %d jobs, want the one recollected identity", len(page.Items))
	}

	// A different vacancy stays distinct despite resembling title/company.
	otherInput := fixtureOpportunity(secondCompany.ID)
	otherInput.SourceURL = "https://harbour.example/jobs/2"
	otherInput.Title = "backend engineer"
	other, _, err := s.CreateOpportunity(ctx, owner, otherInput)
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("different vacancy merged into the first identity")
	}

	// URL matching is case-insensitive (mirroring the duplicate warning);
	// an empty URL carries no verified identity and never reconciles.
	cased, _, err := s.CreateOpportunity(ctx, owner, OpportunityInput{
		CompanyID: secondCompany.ID, Title: "Cased", Kind: "employment",
		SourceURL: "HTTPS://HARBOUR.EXAMPLE/JOBS/1", Stage: "new",
	})
	if err != nil || cased.ID != first.ID {
		t.Fatalf("case-variant URL forked: %+v err=%v", cased, err)
	}
	textOnly := func(title string) OpportunityInput {
		return OpportunityInput{CompanyID: secondCompany.ID, Title: title, Kind: "employment",
			OriginalText: "Same pasted text.", Stage: "new"}
	}
	textFirst, _, err := s.CreateOpportunity(ctx, owner, textOnly("Text A"))
	if err != nil {
		t.Fatal(err)
	}
	textSecond, _, err := s.CreateOpportunity(ctx, owner, textOnly("Text B"))
	if err != nil {
		t.Fatal(err)
	}
	if textFirst.ID == textSecond.ID {
		t.Fatal("URL-less rows merged without a verified source identity")
	}
	if _, err := s.OpportunityBySourceURL(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty source lookup: got %v, want ErrNotFound", err)
	}
	if _, err := s.OpportunityBySourceURL(ctx, "https://harbour.example/jobs/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown source lookup: got %v, want ErrNotFound", err)
	}
}
