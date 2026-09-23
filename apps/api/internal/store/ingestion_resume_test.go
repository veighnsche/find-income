package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func ingestedRecordInput(name string) IngestionRecordInput {
	return IngestionRecordInput{NewCompany: &CompanyInput{Name: name}, Opportunity: OpportunityInput{
		Title: "Backend Engineer", Kind: "employment", Compensation: AdvertisedCompensation{
			Currency: "unknown", Period: "unknown", Basis: "unknown"}}}
}

func TestFailedPostSaveProcessingRetriesSameRecord(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	intake, _, err := s.SubmitIngestion(ctx, ownerActor(), IngestionInput{
		Origin: "owner", SourceURL: "https://jobs.example.test/backend/1",
		OriginalText: "Build Go services and maintain APIs.", IdempotencyKey: "partial-processing"})
	if err != nil {
		t.Fatal(err)
	}
	firstClaim := claimIngestionJob(t, s)
	opportunity, changeID, err := s.SaveIngestionOpportunity(ctx, firstClaim, ingestedRecordInput("Example Systems"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.Ingestion(ctx, intake.ID)
	if err != nil || read.Status != "processing" || read.SourceID == "" || read.RecordChangeID != changeID {
		t.Fatalf("saved record looked complete: %+v %v", read, err)
	}
	if applied, err := s.FailJob(ctx, firstClaim, JobFailure{Code: "model_incomplete", Message: "Codex turn failed"}, time.Now()); err != nil || !applied {
		t.Fatalf("fail after save: %v %v", applied, err)
	}
	read, err = s.Ingestion(ctx, intake.ID)
	if err != nil || read.Status != "failed" || read.OpportunityID != opportunity.ID || read.SourceID == "" {
		t.Fatalf("failed processing lost mapping: %+v %v", read, err)
	}
	sourceID := read.SourceID
	full := "Changed source must not overwrite saved mapping."
	if _, err := s.RetryIngestion(ctx, ownerActor(), intake.ID, &full); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retry changed saved source: %v", err)
	}
	retried, err := s.RetryIngestion(ctx, ownerActor(), intake.ID, nil)
	if err != nil || retried.JobID == firstClaim.ID || retried.Status != "pending" ||
		retried.OpportunityID != opportunity.ID || retried.SourceID != sourceID || retried.RecordChangeID != changeID {
		t.Fatalf("mapped retry: %+v %v", retried, err)
	}
	secondClaim := claimIngestionJob(t, s)
	if err := s.BeginIngestionDispatch(ctx, secondClaim); err != nil {
		t.Fatalf("retry dispatch: %v", err)
	}
	reused, repeatedChange, err := s.SaveIngestionOpportunity(ctx, secondClaim, ingestedRecordInput("Wrong Duplicate"))
	if err != nil || reused.ID != opportunity.ID || repeatedChange != changeID {
		t.Fatalf("retry duplicated saved record: %+v %q %v", reused, repeatedChange, err)
	}
	if err := s.CompleteIngestionProcessing(ctx, firstClaim); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale attempt completed processing: %v", err)
	}
	if err := s.CompleteIngestionProcessing(ctx, secondClaim); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.CompleteJob(ctx, secondClaim, JobResult{Ref: opportunity.ID}, time.Now()); err != nil || !applied {
		t.Fatalf("finish retry: %v %v", applied, err)
	}
	read, err = s.Ingestion(ctx, intake.ID)
	if err != nil || read.Status != "completed" || read.JobState != JobSucceeded {
		t.Fatalf("retry not completed: %+v %v", read, err)
	}
	var companies, opportunities, sources int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM companies`).Scan(&companies)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM opportunities`).Scan(&opportunities)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM evidence_sources`).Scan(&sources)
	if companies != 1 || opportunities != 1 || sources != 1 {
		t.Fatalf("retry duplicated records: companies=%d opportunities=%d sources=%d", companies, opportunities, sources)
	}
}

func TestSeparateIntakesReuseOnlyExactSource(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	base := IngestionInput{Origin: "owner", SourceURL: "https://jobs.example.test/backend/2",
		OriginalText: "Build durable Go APIs for the platform."}
	first := base
	first.IdempotencyKey = "same-source-a"
	intakeA, _, err := s.SubmitIngestion(ctx, ownerActor(), first)
	if err != nil {
		t.Fatal(err)
	}
	claimA := claimIngestionJob(t, s)
	if _, found, err := s.FindMatchingIngestionOpportunity(ctx, claimA); err != nil || found {
		t.Fatalf("unexpected existing match: %v %v", found, err)
	}
	opportunity, changeID, err := s.SaveIngestionOpportunity(ctx, claimA, ingestedRecordInput("Example Systems"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteIngestionProcessing(ctx, claimA); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.CompleteJob(ctx, claimA, JobResult{Ref: opportunity.ID}, time.Now()); err != nil || !applied {
		t.Fatalf("complete first: %v %v", applied, err)
	}
	second := base
	second.IdempotencyKey = "same-source-b"
	intakeB, _, err := s.SubmitIngestion(ctx, ownerActor(), second)
	if err != nil {
		t.Fatal(err)
	}
	claimB := claimIngestionJob(t, s)
	match, found, err := s.FindMatchingIngestionOpportunity(ctx, claimB)
	if err != nil || !found || match.OpportunityID != opportunity.ID || match.RecordChangeID != changeID {
		t.Fatalf("exact source lookup: %+v %v %v", match, found, err)
	}
	reused, reusedChange, err := s.SaveIngestionOpportunity(ctx, claimB, ingestedRecordInput("Wrong Duplicate"))
	if err != nil || reused.ID != opportunity.ID || reusedChange != changeID {
		t.Fatalf("atomic save did not reuse exact source: %+v %q %v", reused, reusedChange, err)
	}
	readA, _ := s.Ingestion(ctx, intakeA.ID)
	readB, _ := s.Ingestion(ctx, intakeB.ID)
	if readB.SourceID != readA.SourceID || readB.OrganisationJobID != readA.OrganisationJobID {
		t.Fatalf("identical source duplicated evidence or Jev job: a=%+v b=%+v", readA, readB)
	}
	if err := s.CompleteIngestionProcessing(ctx, claimB); err != nil {
		t.Fatal(err)
	}
	if applied, err := s.CompleteJob(ctx, claimB, JobResult{Ref: reused.ID}, time.Now()); err != nil || !applied {
		t.Fatalf("complete second: %v %v", applied, err)
	}
	changed := base
	changed.OriginalText = "This vacancy has materially different duties."
	changed.IdempotencyKey = "changed-source"
	_, _, err = s.SubmitIngestion(ctx, ownerActor(), changed)
	if err != nil {
		t.Fatal(err)
	}
	claimC := claimIngestionJob(t, s)
	if _, found, err := s.FindMatchingIngestionOpportunity(ctx, claimC); err != nil || found {
		t.Fatalf("different source merged: %v %v", found, err)
	}
	other, _, err := s.SaveIngestionOpportunity(ctx, claimC, ingestedRecordInput("Example Systems"))
	if err != nil || other.ID == opportunity.ID {
		t.Fatalf("different source reused original record: %+v %v", other, err)
	}
	var opportunities int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM opportunities`).Scan(&opportunities); err != nil || opportunities != 2 {
		t.Fatalf("unexpected records after source change: %d %v", opportunities, err)
	}
}
