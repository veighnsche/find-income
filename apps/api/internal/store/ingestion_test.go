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

func TestAgentSubmissionPreservesAuthenticatedActor(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	input := IngestionInput{Origin: "agent", SourceURL: "https://jobs.example.test/agent-role", IdempotencyKey: "agent-discovery-1"}
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

func TestCollectorSourceIdentitySurvivesRestartAndOlderSighting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: "system", ID: "collector:board-one"}
	first := IngestionInput{Origin: "collector", ConnectorID: "lever:board-one", ExternalID: "job-7",
		SourceURL: "https://jobs.lever.co/example/job-7", OriginalText: `{"id":"job-7","text":"First"}`,
		DiscoveredAt: "2026-09-23T10:00:00Z", IdempotencyKey: "batch-1:0"}
	a, created, err := s.SubmitIngestion(ctx, actor, first)
	if err != nil || !created || a.SourceOpeningID == "" {
		t.Fatalf("first sighting: %+v %v %v", a, created, err)
	}
	changed := first
	changed.OriginalText = `{"id":"job-7","text":"Changed"}`
	changed.DiscoveredAt, changed.IdempotencyKey = "2026-09-23T10:05:00Z", "batch-2:0"
	b, created, err := s.SubmitIngestion(ctx, actor, changed)
	if err != nil || !created || b.ID == a.ID || b.SourceOpeningID != a.SourceOpeningID {
		t.Fatalf("changed revision: %+v %v %v", b, created, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	older := first
	older.DiscoveredAt, older.IdempotencyKey = "2026-09-23T10:02:00Z", "batch-older:0"
	current, created, err := s.SubmitIngestion(ctx, actor, older)
	if err != nil || created || current.ID != b.ID {
		t.Fatalf("out-of-order response displaced current source: %+v %v %v", current, created, err)
	}
	var currentID, digest, latest, decision string
	var openingCount, sightingCount, requestCount int
	if err := s.db.QueryRowContext(ctx, `SELECT current_ingestion_id,current_sha256,latest_observed_at FROM source_openings WHERE id=?`, a.SourceOpeningID).
		Scan(&currentID, &digest, &latest); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT decision FROM source_sightings WHERE observed_at=?`, "2026-09-23T10:02:00.000000000Z").Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_openings`).Scan(&openingCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source_sightings`).Scan(&sightingCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM ingestion_requests WHERE origin='collector'`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if currentID != b.ID || digest != sourceDigest(changed.OriginalText) ||
		latest != "2026-09-23T10:05:00.000000000Z" || decision != "older" ||
		openingCount != 1 || sightingCount != 3 || requestCount != 2 {
		t.Fatalf("source history changed: current=%s digest=%s latest=%s decision=%s openings=%d sightings=%d requests=%d",
			currentID, digest, latest, decision, openingCount, sightingCount, requestCount)
	}
}
