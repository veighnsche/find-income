package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

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

}

func TestURLOnlyIntakeIsDurableAndInert(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	input := IngestionInput{Origin: "owner", SourceURL: "https://jobs.example.test/role/1", IdempotencyKey: "url-only"}
	item, created, err := s.SubmitIngestion(ctx, ownerActor(), input)
	if err != nil || !created || item.Status != "pending" || item.OriginalText != "" {
		t.Fatalf("inert URL intake: %+v %v %v", item, created, err)
	}
	if _, found, err := s.ClaimNextJob(ctx, "worker", []string{IngestionJobKind}, time.Minute, time.Now()); err != nil || found {
		t.Fatalf("uncommissioned intake claimed: %v %v", found, err)
	}
}

func TestConcurrentOwnerSubmissionUsesOneDurableJob(t *testing.T) {
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
	actor := ownerActor()
	input := IngestionInput{Origin: "owner", SourceURL: "https://jobs.example.test/123", OriginalText: "Full vacancy", IdempotencyKey: "owner:123:digest"}
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

func TestAgentSubmissionPreservesAuthenticatedActor(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	input := IngestionInput{Origin: "agent", SourceURL: "https://jobs.example.test/agent-role", IdempotencyKey: "agent-source-1"}
	actor := Actor{Kind: "agent", ID: "agent-credential-42"}
	item, created, err := s.SubmitIngestion(ctx, actor, input)
	if err != nil || !created || item.Actor != actor || item.Origin != "agent" {
		t.Fatalf("agent attribution: %+v created=%v err=%v", item, created, err)
	}
	if _, _, err = s.SubmitIngestion(ctx, ownerActor(), input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("owner impersonated agent origin: %v", err)
	}
	input.Origin = "owner"
	if _, _, err = s.SubmitIngestion(ctx, actor, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent impersonated owner origin: %v", err)
	}
}
