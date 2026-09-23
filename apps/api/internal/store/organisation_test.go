package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOrganisationCategoriesQueueCompletedIntakesAndCheckVersion(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	defaultSet, err := s.CurrentOrganisationCategories(ctx)
	if err != nil || defaultSet.Version != 1 || len(defaultSet.Categories) == 0 {
		t.Fatalf("default organisation categories: %+v %v", defaultSet, err)
	}
	intake, _, err := s.SubmitIngestion(ctx, owner, IngestionInput{
		Origin: "owner", OriginalText: "Build Go platform services.", IdempotencyKey: "category-replay"})
	if err != nil {
		t.Fatal(err)
	}
	claim := claimIngestionJob(t, s)
	opportunity, _, err := s.SaveIngestionOpportunity(ctx, claim, IngestionRecordInput{
		NewCompany: &CompanyInput{Name: "Example Systems"}, Opportunity: OpportunityInput{
			Title: "Platform Engineer", Kind: "employment", Compensation: AdvertisedCompensation{
				Currency: "unknown", Period: "unknown", Basis: "unknown"}}})
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := s.CompleteJob(ctx, claim, JobResult{Ref: opportunity.ID}, time.Now()); err != nil || !applied {
		t.Fatalf("complete: %v %v", applied, err)
	}
	read, err := s.Ingestion(ctx, intake.ID)
	if err != nil || read.OrganisationJobID == "" {
		t.Fatalf("default categories did not queue a job: %+v %v", read, err)
	}
	firstJobID := read.OrganisationJobID
	set, err := s.UpdateOrganisationCategories(ctx, 1, []OrganisationCategory{
		{ID: "platform", Description: "Platform engineering opportunities."}}, owner)
	if err != nil || set.Version != 2 {
		t.Fatalf("configure: %+v %v", set, err)
	}
	read, err = s.Ingestion(ctx, intake.ID)
	if err != nil || read.OrganisationJobID == "" || read.OrganisationJobID == firstJobID {
		t.Fatalf("category edit did not requeue: %+v %v", read, err)
	}
	firstJobID = read.OrganisationJobID
	if _, err = s.UpdateOrganisationCategories(ctx, 1, nil, owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner version accepted: %v", err)
	}
	set, err = s.UpdateOrganisationCategories(ctx, 2, []OrganisationCategory{
		{ID: "backend", Description: "Backend engineering opportunities."}}, owner)
	if err != nil || set.Version != 3 {
		t.Fatalf("revise: %+v %v", set, err)
	}
	read, err = s.Ingestion(ctx, intake.ID)
	if err != nil || read.OrganisationJobID == firstJobID {
		t.Fatalf("category revision did not replace job: %+v %v", read, err)
	}
	queued, found, err := s.ClaimNextJob(ctx, "category-test", []string{OrganisationJobKind}, time.Minute, time.Now())
	if err != nil || !found {
		t.Fatalf("claim: %v %v", found, err)
	}
	if _, err := s.OrganisationSnapshotForJob(ctx, queued); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded job used current categories: %v", err)
	}
	current, err := s.CurrentOrganisationCategories(ctx)
	if err != nil || current.Version != 3 || current.Categories[0].ID != "backend" {
		t.Fatalf("current category set: %+v %v", current, err)
	}
}

func TestDistinctIntakesReuseMatchingOrganisationJob(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	company := createFixtureCompany(t, s)
	input := fixtureOpportunity(company.ID)
	input.OriginalText = "Build Go platform services."
	opportunity, changeID, err := s.CreateOpportunity(ctx, owner, input)
	if err != nil {
		t.Fatal(err)
	}
	var firstJobID, firstSourceID string
	for _, key := range []string{"first-intake", "second-intake"} {
		intake, _, err := s.SubmitIngestion(ctx, owner, IngestionInput{
			Origin: "owner", SourceURL: input.SourceURL, OriginalText: input.OriginalText, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		claim := claimIngestionJob(t, s)
		if err := s.RecordIngestionResult(ctx, claim, opportunity.ID, changeID); err != nil {
			t.Fatalf("map %s: %v", key, err)
		}
		if applied, err := s.CompleteJob(ctx, claim, JobResult{Ref: opportunity.ID}, time.Now()); err != nil || !applied {
			t.Fatalf("complete %s: %v %v", key, applied, err)
		}
		read, err := s.Ingestion(ctx, intake.ID)
		if err != nil || read.Status != "completed" || read.SourceID == "" || read.OrganisationJobID == "" {
			t.Fatalf("completed %s: %+v %v", key, read, err)
		}
		if firstJobID == "" {
			firstJobID, firstSourceID = read.OrganisationJobID, read.SourceID
		} else if read.OrganisationJobID != firstJobID || read.SourceID != firstSourceID {
			t.Fatalf("duplicate work or source for %s: %+v", key, read)
		}
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind=?`, OrganisationJobKind).Scan(&count); err != nil || count != 1 {
		t.Fatalf("organisation jobs: count=%d err=%v", count, err)
	}
}
