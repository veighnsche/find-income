package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIngestionEvidenceIsBoundToLiveClaimAndSavedSource(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	intake, _, err := s.SubmitIngestion(ctx, ownerActor(), IngestionInput{
		Origin: "owner", OriginalText: "Build Go platform services at 32 hours.", IdempotencyKey: "intake-evidence"})
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
	read, err := s.Ingestion(ctx, intake.ID)
	if err != nil || read.SourceID == "" {
		t.Fatalf("saved intake source: %+v %v", read, err)
	}
	versions, err := s.QualificationInputVersions(ctx, opportunity.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := EvidenceInput{OpportunityID: opportunity.ID, SourceID: read.SourceID,
		Criterion: "role_criterion", CriterionID: "backend-platform", Presence: "explicit_presence",
		ObservedValue: "Backend and platform duties", SpanStart: 0, SpanEnd: len("Build Go platform services"),
		ExpectedPreferencesVersion: versions.PreferencesVersion, ExpectedEvidenceVersion: versions.EvidenceVersion}
	wrongSource := input
	wrongSource.SourceID = "unrelated-source"
	if _, _, err := s.AddIngestionEvidence(ctx, claim, wrongSource); !errors.Is(err, ErrConflict) {
		t.Fatalf("unrelated source accepted: %v", err)
	}
	stale := claim
	stale.LeaseToken = "stale-token"
	if _, _, err := s.AddIngestionEvidence(ctx, stale, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale claim accepted: %v", err)
	}
	evidence, changeID, err := s.AddIngestionEvidence(ctx, claim, input)
	if err != nil || evidence.SourceID != read.SourceID || evidence.SourceExcerpt != "Build Go platform services" || changeID == "" {
		t.Fatalf("live sourced claim: %+v %q %v", evidence, changeID, err)
	}
	if _, _, err := s.AddIngestionEvidence(ctx, claim, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated evidence version appended duplicate: %v", err)
	}
	if applied, err := s.CompleteJob(ctx, claim, JobResult{Ref: opportunity.ID}, time.Now()); err != nil || !applied {
		t.Fatalf("finish intake: %v %v", applied, err)
	}
	input.ExpectedEvidenceVersion = versions.EvidenceVersion + 1
	if _, _, err := s.AddIngestionEvidence(ctx, claim, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed claim wrote evidence: %v", err)
	}
}
