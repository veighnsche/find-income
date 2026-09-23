package store

import (
	"context"
	"testing"
)

func ingestedRecordInput(name string) IngestionRecordInput {
	return IngestionRecordInput{NewCompany: &CompanyInput{Name: name}, Opportunity: OpportunityInput{
		Title: "Backend Engineer", Kind: "employment", Compensation: AdvertisedCompensation{
			Currency: "unknown", Period: "unknown", Basis: "unknown"}}}
}

func TestSeparateIntakesReuseOnlyExactSource(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	base := IngestionInput{Origin: "owner", SourceURL: "https://jobs.example.test/backend/2",
		OriginalText: "Build durable Go APIs for the platform.", IdempotencyKey: "same-source-a"}
	first, created, err := s.SubmitIngestion(ctx, ownerActor(), base)
	if err != nil || !created {
		t.Fatalf("first source: %+v %v %v", first, created, err)
	}
	unchanged := base
	unchanged.IdempotencyKey = "same-source-b"
	second, created, err := s.SubmitIngestion(ctx, ownerActor(), unchanged)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("unchanged source duplicated: %+v %v %v", second, created, err)
	}
	changed := base
	changed.IdempotencyKey = "changed-source"
	changed.OriginalText = "This vacancy has materially different duties."
	third, created, err := s.SubmitIngestion(ctx, ownerActor(), changed)
	if err != nil || !created || third.ID == first.ID || third.SourceOpeningID != first.SourceOpeningID {
		t.Fatalf("changed source identity: %+v %v %v", third, created, err)
	}
	var jobs, sightings int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind=?`, IngestionJobKind).Scan(&jobs); err != nil || jobs != 2 {
		t.Fatalf("wrong inert intake job count: %d %v", jobs, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_sightings WHERE source_opening_id=?`, first.SourceOpeningID).Scan(&sightings); err != nil || sightings != 3 {
		t.Fatalf("source history lost: %d %v", sightings, err)
	}
}
