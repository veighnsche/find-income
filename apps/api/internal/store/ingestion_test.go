package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func claimIngestionJob(t *testing.T, s *Store) Job {
	t.Helper()
	job, found, err := s.ClaimNextJob(context.Background(), "ingestion-test", []string{IngestionJobKind}, time.Minute, time.Now().UTC())
	if err != nil || !found {
		t.Fatalf("claim ingestion: %+v found=%v err=%v", job, found, err)
	}
	return job
}

func TestIngestionPersistsSourceAndRequiresTrustedRecordMapping(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	actor := ownerActor()
	input := IngestionInput{Origin: "owner", OriginalText: "Full pasted backend vacancy at 32 hours.", IdempotencyKey: "paste-1"}
	item, created, err := s.SubmitIngestion(ctx, actor, input)
	if err != nil || !created || item.Status != "pending" || item.OriginalText != input.OriginalText {
		t.Fatalf("submit: %+v created=%v err=%v", item, created, err)
	}
	duplicate, created, err := s.SubmitIngestion(ctx, actor, input)
	if err != nil || created || duplicate.ID != item.ID || duplicate.JobID != item.JobID {
		t.Fatalf("idempotent repeat: %+v created=%v err=%v", duplicate, created, err)
	}
	changed := input
	changed.OriginalText = "Changed text"
	if _, _, err := s.SubmitIngestion(ctx, actor, changed); !errors.Is(err, ErrJobIdempotencyConflict) {
		t.Fatalf("key reused with other content: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	read, err := s.Ingestion(ctx, item.ID)
	if err != nil || read.OriginalText != input.OriginalText || read.Status != "pending" {
		t.Fatalf("restart lost source: %+v %v", read, err)
	}
	page, err := s.ListIngestions(ctx, "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != item.ID {
		t.Fatalf("list: %+v %v", page, err)
	}
	claim := claimIngestionJob(t, s)
	if err := s.BeginIngestionDispatch(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginIngestionDispatch(ctx, claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate dispatch: %v", err)
	}
	if err := s.BindIngestionThread(ctx, claim, "thread-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindIngestionTurn(ctx, claim, "thread-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindIngestionThread(ctx, claim, "other-thread"); !errors.Is(err, ErrConflict) {
		t.Fatalf("thread rebound: %v", err)
	}
	company := createFixtureCompany(t, s)
	inputRecord := fixtureOpportunity(company.ID)
	inputRecord.OriginalText = "Unrelated source"
	other, otherChange, err := s.CreateOpportunity(ctx, actor, inputRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIngestionResult(ctx, claim, other.ID, otherChange); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrelated source mapped: %v", err)
	}
	inputRecord.Title = "Correct backend vacancy"
	inputRecord.OriginalText = input.OriginalText
	opportunity, changeID, err := s.CreateOpportunity(ctx, actor, inputRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIngestionResult(ctx, claim, opportunity.ID, changeID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIngestionResult(ctx, claim, opportunity.ID, changeID); err != nil {
		t.Fatalf("identical mapping: %v", err)
	}
	if err := s.RecordIngestionResult(ctx, claim, other.ID, otherChange); !errors.Is(err, ErrConflict) {
		t.Fatalf("result rebound: %v", err)
	}
	if applied, err := s.CompleteJob(ctx, claim, JobResult{Ref: opportunity.ID}, time.Now().UTC()); err != nil || !applied {
		t.Fatalf("job completion: %v %v", applied, err)
	}
	read, err = s.Ingestion(ctx, item.ID)
	if err != nil || read.Status != "completed" || read.OpportunityID != opportunity.ID || read.RecordChangeID != changeID || read.CodexThreadID != "thread-1" {
		t.Fatalf("completed mapping: %+v %v", read, err)
	}
}

func TestURLOnlyNeedsTextAndExplicitRetry(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	input := IngestionInput{Origin: "owner", SourceURL: "https://jobs.example.test/role/1", IdempotencyKey: "url-1"}
	item, _, err := s.SubmitIngestion(ctx, ownerActor(), input)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimIngestionJob(t, s)
	if err := s.MarkIngestionNeedsText(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.CompleteJob(ctx, claim, JobResult{Ref: "needs_text"}, time.Now().UTC()); err != nil || !applied {
		t.Fatalf("complete needs_text job: %v %v", applied, err)
	}
	read, err := s.Ingestion(ctx, item.ID)
	if err != nil || read.Status != "needs_text" || read.SourceURL != input.SourceURL {
		t.Fatalf("needs text: %+v %v", read, err)
	}
	if _, err := s.RetryIngestion(ctx, ownerActor(), item.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty retry: %v", err)
	}
	full := "Complete vacancy content supplied later."
	retried, err := s.RetryIngestion(ctx, ownerActor(), item.ID, &full)
	if err != nil || retried.Status != "pending" || retried.OriginalText != full || retried.SourceURL != input.SourceURL || retried.AttemptsStarted != 2 || retried.JobID == item.JobID {
		t.Fatalf("retry: %+v %v", retried, err)
	}
	if err := s.BindIngestionThread(ctx, claim, "stale-thread"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old lease rebound retry: %v", err)
	}
}

func TestConcurrentCollectorSubmissionUsesOneDurableJob(t *testing.T) {
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
	actor := Actor{Kind: "system", ID: "collector-greenhouse"}
	input := IngestionInput{Origin: "collector", ConnectorID: "greenhouse", ExternalID: "123", SourceURL: "https://jobs.example.test/123", OriginalText: "Full vacancy", IdempotencyKey: "greenhouse:123:digest"}
	start := make(chan struct{})
	out := make(chan struct {
		item    IngestionRequest
		created bool
		err     error
	}, 2)
	var wg sync.WaitGroup
	for _, s := range []*Store{first, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			item, created, err := s.SubmitIngestion(ctx, actor, input)
			out <- struct {
				item    IngestionRequest
				created bool
				err     error
			}{item, created, err}
		}(s)
	}
	close(start)
	wg.Wait()
	close(out)
	createdCount := 0
	ids := map[string]bool{}
	for result := range out {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		}
		ids[result.item.ID] = true
	}
	if createdCount != 1 || len(ids) != 1 {
		t.Fatalf("created=%d ids=%v", createdCount, ids)
	}
	var count int
	if err := first.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind=?`, IngestionJobKind).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate jobs: %d %v", count, err)
	}
}

func TestSaveIngestionOpportunityIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	text := "Complete vacancy text with backend services and 32 weekly hours."
	intake, _, err := s.SubmitIngestion(ctx, ownerActor(), IngestionInput{Origin: "owner", OriginalText: text, IdempotencyKey: "save-atomic"})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimIngestionJob(t, s)
	input := IngestionRecordInput{NewCompany: &CompanyInput{Name: "Example Systems"},
		Opportunity: OpportunityInput{Title: "Backend engineer", Kind: "employment", Stage: "applied",
			OriginalText: "model changed vacancy text", Notes: "model note", WorkPattern: "remote",
			Compensation: AdvertisedCompensation{Currency: "unknown", Period: "unknown", Basis: "unknown"}}}
	invalid := input
	invalid.Opportunity.Title = ""
	if _, _, err := s.SaveIngestionOpportunity(ctx, claim, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad extraction accepted: %v", err)
	}
	var companies, opportunities int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM companies`).Scan(&companies); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM opportunities`).Scan(&opportunities); err != nil {
		t.Fatal(err)
	}
	if companies != 0 || opportunities != 0 {
		t.Fatalf("partial extraction: companies=%d opportunities=%d", companies, opportunities)
	}
	opportunity, changeID, err := s.SaveIngestionOpportunity(ctx, claim, input)
	if err != nil || opportunity.Stage != "discovered" || opportunity.OriginalText != text || opportunity.Notes != "" || changeID == "" {
		t.Fatalf("save: %+v change=%q err=%v", opportunity, changeID, err)
	}
	repeated, repeatedChange, err := s.SaveIngestionOpportunity(ctx, claim, input)
	if err != nil || repeated.ID != opportunity.ID || repeatedChange != changeID {
		t.Fatalf("idempotent save: %+v change=%q err=%v", repeated, repeatedChange, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM companies`).Scan(&companies); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM opportunities`).Scan(&opportunities); err != nil {
		t.Fatal(err)
	}
	if companies != 1 || opportunities != 1 {
		t.Fatalf("duplicate extraction: companies=%d opportunities=%d", companies, opportunities)
	}
	read, err := s.Ingestion(ctx, intake.ID)
	if err != nil || read.Status != "completed" || read.OpportunityID != opportunity.ID || read.RecordChangeID != changeID {
		t.Fatalf("mapped intake: %+v %v", read, err)
	}
}

func TestURLFetchTextIsPreservedOnAtomicSave(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	url := "https://jobs.example.test/roles/42"
	intake, _, err := s.SubmitIngestion(ctx, ownerActor(), IngestionInput{Origin: "owner", SourceURL: url, IdempotencyKey: "url-fetch"})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimIngestionJob(t, s)
	full := "The complete fetched backend vacancy at 32 hours."
	if err := s.AttachIngestionText(ctx, claim, full); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachIngestionText(ctx, claim, "Different content"); !errors.Is(err, ErrConflict) {
		t.Fatalf("source overwritten: %v", err)
	}
	opportunity, changeID, err := s.SaveIngestionOpportunity(ctx, claim, IngestionRecordInput{
		NewCompany: &CompanyInput{Name: "Site Employer"}, Opportunity: OpportunityInput{Title: "Platform role", Kind: "employment",
			Compensation: AdvertisedCompensation{Currency: "unknown", Period: "unknown", Basis: "unknown"}}})
	if err != nil || opportunity.SourceURL != url || opportunity.OriginalText != full {
		t.Fatalf("URL save: %+v %v", opportunity, err)
	}
	read, err := s.Ingestion(ctx, intake.ID)
	if err != nil || read.OriginalText != full || read.RecordChangeID != changeID {
		t.Fatalf("fetched source lost: %+v %v", read, err)
	}
}
