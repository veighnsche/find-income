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
	if err != nil || read.OrganisationJobID != "" {
		t.Fatalf("unconfigured categories queued a job: %+v %v", read, err)
	}
	set, err := s.UpdateOrganisationCategories(ctx, 0, []OrganisationCategory{
		{ID: "platform", Description: "Platform engineering opportunities."}}, owner)
	if err != nil || set.Version != 1 {
		t.Fatalf("configure: %+v %v", set, err)
	}
	read, err = s.Ingestion(ctx, intake.ID)
	if err != nil || read.OrganisationJobID == "" {
		t.Fatalf("completed intake not queued: %+v %v", read, err)
	}
	firstJobID := read.OrganisationJobID
	if _, err = s.UpdateOrganisationCategories(ctx, 0, nil, owner); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale owner version accepted: %v", err)
	}
	set, err = s.UpdateOrganisationCategories(ctx, 1, []OrganisationCategory{
		{ID: "backend", Description: "Backend engineering opportunities."}}, owner)
	if err != nil || set.Version != 2 {
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
	if err != nil || current.Version != 2 || current.Categories[0].ID != "backend" {
		t.Fatalf("current category set: %+v %v", current, err)
	}
}
