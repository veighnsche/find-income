package store

import (
	"context"
	"testing"
)

func TestUnchangedIntakeDoesNotQueueOrganisationWork(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	source := IngestionInput{Origin: "owner", SourceURL: "https://jobs.example.test/org",
		OriginalText: "Exact source", IdempotencyKey: "org-intake-a"}
	first, created, err := s.SubmitIngestion(ctx, ownerActor(), source)
	if err != nil || !created {
		t.Fatalf("first intake: %+v %v %v", first, created, err)
	}
	source.IdempotencyKey = "org-intake-b"
	second, created, err := s.SubmitIngestion(ctx, ownerActor(), source)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("duplicate intake: %+v %v %v", second, created, err)
	}
	var jobs int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind=?`, OrganisationJobKind).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("uncommissioned organisation job: %d %v", jobs, err)
	}
}
